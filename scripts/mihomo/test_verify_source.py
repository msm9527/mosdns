"""Regression tests for fail-closed native release gates (no Go toolchain needed)."""

import importlib.util
import contextlib
import io
import json
import os
from pathlib import Path
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
    def run_gate(self, events, unit_only=True):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            executable = root / "go"
            executable.write_text("#!/usr/bin/env python3\nimport json\n"
                                  f"events = {events!r}\n"
                                  "for event in events: print(json.dumps(event))\n")
            executable.chmod(0o755)
            with mock.patch.dict(os.environ, {"PATH": str(root) + os.pathsep + os.environ["PATH"]}):
                with contextlib.redirect_stdout(io.StringIO()):
                    return verify.verify_tests(root, "meta", unit_only, False)

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


class SmartPatchSafetyTest(unittest.TestCase):
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


class ArchitecturePackagingTest(unittest.TestCase):
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
