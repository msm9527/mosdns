"""Regression tests for fail-closed native release gates (no Go toolchain needed)."""

import importlib.util
import contextlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location("verify_source", Path(__file__).with_name("verify-source.py"))
verify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verify)
target_spec = importlib.util.spec_from_file_location("targets", Path(__file__).with_name("targets.py"))
targets = importlib.util.module_from_spec(target_spec)
target_spec.loader.exec_module(targets)


class NativeTestGateTest(unittest.TestCase):
    def run_gate(self, events, unit_only=True, channel="meta"):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            executable = root / "go"
            executable.write_text("#!/usr/bin/env python3\nimport json\n"
                                  f"events = {events!r}\n"
                                  "for event in events: print(json.dumps(event))\n")
            executable.chmod(0o755)
            with mock.patch.dict(os.environ, {"PATH": str(root) + os.pathsep + os.environ["PATH"]}):
                with contextlib.redirect_stdout(io.StringIO()):
                    return verify.verify_tests(root, channel, unit_only, False)

    def passed_events(self):
        return [{"Package": f"{verify.MODULE}/{package}", "Test": name, "Action": "pass"}
                for package, names in verify.BASE_TESTS.items() for name in names]

    def test_empty_successful_go_run_is_rejected(self):
        with self.assertRaisesRegex(RuntimeError, "missing="):
            self.run_gate([])

    def test_missing_one_regression_is_rejected(self):
        with self.assertRaisesRegex(RuntimeError, "missing="):
            self.run_gate(self.passed_events()[:-1])

    def test_skipped_subtest_is_rejected_even_when_parent_passes(self):
        events = self.passed_events()
        skipped = dict(events[0], Action="skip", Test=events[0]["Test"] + "/requires_root")
        with self.assertRaisesRegex(RuntimeError, "skipped="):
            self.run_gate(events + [skipped])

    def test_all_required_regressions_pass(self):
        self.assertEqual(len(self.run_gate(self.passed_events())), sum(map(len, verify.BASE_TESTS.values())))

    def full_events(self):
        return self.passed_events() + [
            {"Package": f"{verify.MODULE}/{package}", "Test": name, "Action": "pass"}
            for package, name in [
                ("listener/sing_tun", "TestSystemRouteBypassNamespace"),
                ("listener/sing_tun", "TestSystemRouteJournalNamespace"),
                ("listener", "TestTunSystemRouteReloadNamespace"),
            ]
        ]

    def test_missing_crash_recovery_integration_is_rejected(self):
        events = [event for event in self.full_events()
                  if event["Test"] != "TestSystemRouteJournalNamespace"]
        with self.assertRaisesRegex(RuntimeError, "missing=.*TestSystemRouteJournalNamespace"):
            self.run_gate(events, unit_only=False)

    def test_skipped_crash_recovery_scenario_is_rejected(self):
        events = self.full_events()
        events.append({"Package": f"{verify.MODULE}/listener/sing_tun", "Action": "skip",
                       "Test": "TestSystemRouteJournalNamespace/kill_during_recovery"})
        with self.assertRaisesRegex(RuntimeError, "skipped=.*kill_during_recovery"):
            self.run_gate(events, unit_only=False)

    def test_full_integration_regressions_pass(self):
        events = self.full_events()
        self.assertEqual(len(self.run_gate(events, unit_only=False)), len(events))

    def smart_events(self):
        return self.passed_events() + [
            {"Package": f"{verify.MODULE}/{package}", "Test": name, "Action": "pass"}
            for package, names in verify.SMART_TESTS.items() for name in names
        ]

    def test_smart_missing_model_recovery_regression_is_rejected(self):
        events = [event for event in self.smart_events()
                  if event["Test"] != "TestSmartModelRecoveryAndAtomicDownload"]
        with self.assertRaisesRegex(RuntimeError, "missing=.*TestSmartModelRecoveryAndAtomicDownload"):
            self.run_gate(events, channel="smart")

    def test_smart_all_feature_lifecycle_regressions_pass(self):
        events = self.smart_events()
        self.assertEqual(len(self.run_gate(events, channel="smart")), len(events))

    def test_smart_additions_do_not_replace_shared_config_regressions(self):
        events = [event for event in self.smart_events()
                  if event["Test"] != "TestTunSystemRouteFreshParseValidation"]
        with self.assertRaisesRegex(RuntimeError, "missing=.*TestTunSystemRouteFreshParseValidation"):
            self.run_gate(events, channel="smart")


class SmartPatchSafetyTest(unittest.TestCase):
    def test_all_channels_require_selection_patch_and_advance_revision(self):
        for channel in ("meta", "alpha"):
            self.assertEqual(verify.patch_revision(channel), "msm.2")
            self.assertIn("group-selection-concurrency.patch", verify.patch_files(channel))
            self.assertIn("group.selection-concurrency", verify.features(channel))
            self.assertNotIn("smart-feature-lifecycle.patch", verify.patch_files(channel))
            self.assertNotIn("smart-selection-concurrency.patch", verify.patch_files(channel))
        self.assertEqual(verify.patch_revision("smart"), "msm.3")
        self.assertIn("smart-feature-lifecycle.patch", verify.patch_files("smart"))
        self.assertIn("smart-selection-concurrency.patch", verify.patch_files("smart"))
        self.assertIn("smart.feature-lifecycle", verify.features("smart"))
        self.assertIn("smart.selection-concurrency", verify.features("smart"))
        self.assertIn("smart-transfer-lifecycle.patch", verify.patch_files("smart"))
        self.assertIn("smart.transfer-lifecycle", verify.features("smart"))
        for name in verify.SMART_TRANSFER_TESTS:
            self.assertIn(f"{verify.MODULE}/adapter/outboundgroup:{name}", verify.selection_test_names("smart"))

    def test_unknown_upstream_method_is_not_removed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "transport/anytls/session/stream.go"
            source.parent.mkdir(parents=True)
            content = "package session\nfunc (s *Stream) NetConn() net.Conn { return s.unknown }\n"
            source.write_text(content)
            with self.assertRaisesRegex(RuntimeError, "manual review"):
                verify.patch_smart_stream(root)
            self.assertEqual(source.read_text(), content)
            self.assertFalse(source.with_name("stream_isolation_test.go").exists())


class SelectionRaceGateTest(unittest.TestCase):
    def run_gate(self, channel, missing=None, skipped=None):
        events = [{"Package": name.split(":")[0], "Test": name.split(":")[1], "Action": "pass"}
                  for name in verify.selection_test_names(channel) if name != missing]
        if skipped:
            package, test = skipped.split(":")
            events.append({"Package": package, "Test": test + "/requires_network", "Action": "skip"})
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            command_record = root / "command.json"
            executable = root / "go"
            executable.write_text("#!/usr/bin/env python3\nimport json, os, sys\n"
                                  f"events = {events!r}\n"
                                  f"open({str(command_record)!r}, 'w').write(json.dumps("
                                  "{'argv': sys.argv, 'env': dict(os.environ)}))\n"
                                  "for event in events: print(json.dumps(event))\n")
            executable.chmod(0o755)
            with mock.patch.dict(os.environ, {"PATH": str(root) + os.pathsep + os.environ["PATH"],
                                              "GOOS": "windows", "CGO_ENABLED": "0"}):
                with contextlib.redirect_stdout(io.StringIO()):
                    result = verify.verify_selection_concurrency(root, channel)
            command = json.loads(command_record.read_text())
            self.assertIn("-race", command["argv"])
            self.assertEqual(command["env"]["CGO_ENABLED"], "1")
            self.assertNotIn("GOOS", command["env"])
            return result

    def test_missing_or_skipped_smart_transfer_gate_is_rejected(self):
        required = f"{verify.MODULE}/adapter/outboundgroup:TestSmartAutomaticSoftSkippedFailureClearsPin"
        with self.assertRaisesRegex(RuntimeError, "missing="):
            self.run_gate("smart", missing=required)
        with self.assertRaisesRegex(RuntimeError, "skipped="):
            self.run_gate("smart", skipped=required)

    def test_all_channels_run_required_native_race_tests(self):
        for channel in ("meta", "alpha", "smart"):
            with self.subTest(channel=channel):
                self.assertEqual(self.run_gate(channel), verify.selection_test_names(channel))

    def test_missing_required_race_test_is_rejected(self):
        for channel in ("meta", "smart"):
            with self.subTest(channel=channel), self.assertRaisesRegex(RuntimeError, "missing="):
                self.run_gate(channel, missing=verify.selection_test_names(channel)[-1])

    def test_skipped_race_subtest_is_rejected(self):
        with self.assertRaisesRegex(RuntimeError, "skipped=.*requires_network"):
            self.run_gate("smart", skipped=verify.selection_test_names("smart")[0])

class ArchitecturePackagingTest(unittest.TestCase):
    def test_old_or_incomplete_race_proof_cannot_authorize_packaging(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            paths = set()
            for name in verify.patch_files("meta"):
                paths.update(re.findall(r"(?m)^\+\+\+ b/(.+)$", (verify.ASSETS / name).read_text()))
            for name in paths:
                target = root / name
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_text("source fixture\n")
            executable = root / "git"
            commit = "a" * 40
            executable.write_text("#!/bin/sh\necho " + commit + "\n")
            executable.chmod(0o755)
            proof = {"schema": 1, "channel": "meta", "source_commit": commit,
                     "patch_revision": verify.patch_revision("meta"),
                     "features": verify.features("meta"), "verified": True,
                     "patch_sha256": verify.fingerprints("meta"),
                     "patched_file_sha256": verify.source_fingerprints(root, "meta")}
            environment = {**os.environ, "PATH": str(root) + os.pathsep + os.environ["PATH"],
                           "CHANNEL": "meta", "CHANNEL_VERSION": "v1.19.33-msm.2",
                           "ASSET_SUFFIX": "linux-amd64", "PACKAGE_FORMAT": "tar.gz",
                           "OUTPUT_DIR": str(root / "artifacts"), "GOOS": "linux",
                           "GOARCH": "amd64", "GOAMD64": "v1", "CGO_ENABLED": "0",
                           "PYTHONDONTWRITEBYTECODE": "1"}
            for race_tests in (None, verify.selection_test_names("meta")[:-1]):
                with self.subTest(race_tests=race_tests):
                    candidate = dict(proof)
                    if race_tests is not None:
                        candidate["race_tests"] = race_tests
                    (root / verify.PROOF_FILE).write_text(json.dumps(candidate))
                    result = subprocess.run(["bash", str(Path(__file__).with_name("build-artifact.sh"))],
                                            cwd=root, env=environment, capture_output=True, text=True)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn("exact required native patch/test gate", result.stderr)
                    self.assertFalse((root / "artifacts").exists())

    def test_baseline_asset_cannot_contain_v3_binary(self):
        for channel, suffix, package in (("meta", "linux-amd64", "tar.gz"),
                                         ("alpha", "darwin-amd64", "tar.gz"),
                                         ("smart", "linux-amd64-v1", "gz")):
            with self.subTest(channel=channel), self.assertRaisesRegex(ValueError, "GOAMD64=v1"):
                targets.validate_target(channel, suffix, package,
                                        {"GOOS": suffix.split("-")[0], "GOARCH": "amd64",
                                         "GOAMD64": "v3", "CGO_ENABLED": "0"})

    def test_smart_implicit_amd64_baseline_is_rejected(self):
        with self.assertRaisesRegex(ValueError, "unsupported asset platform"):
            targets.validate_target("smart", "linux-amd64", "gz", {})

    def test_arm_and_mips_cpu_variants_cannot_be_mislabeled(self):
        for suffix, environment in (("linux-armv5", {"GOOS": "linux", "GOARCH": "arm", "GOARM": "7"}),
                                    ("linux-mipsle-softfloat", {"GOOS": "linux", "GOARCH": "mipsle", "GOMIPS": "hardfloat"})):
            with self.subTest(suffix=suffix), self.assertRaises(ValueError):
                targets.validate_target("meta", suffix, "tar.gz", environment)

    def test_supported_matrix_sizes_are_preserved(self):
        self.assertEqual(len(targets.allowed_targets("meta")), 15)
        self.assertEqual(len(targets.allowed_targets("alpha")), 15)
        self.assertEqual(len(targets.allowed_targets("smart")), 10)


if __name__ == "__main__":
    unittest.main()
