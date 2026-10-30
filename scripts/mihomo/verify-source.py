#!/usr/bin/env python3
"""Apply the reviewed native patches and prove their regressions run and pass."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile

ASSETS = Path(__file__).resolve().parent / "patches"
MODULE = "github.com/metacubex/mihomo"
PROOF_FILE = ".msm-native-verification.json"
BASE_TESTS = {
    "component/dialer": [
        "TestSystemRouteSkipsOnlyInterfaceBinding",
        "TestSystemRoutePreservesRoutingMark",
    ],
    "adapter": [
        "TestParseDirectSystemPreservesOrdinaryDirect",
        "TestParsedDirectSystemUsesKernelRoute",
    ],
    "listener/sing_tun": [
        "TestSystemRouteRuleConflicts",
        "TestSystemRouteOptionsValidation",
    ],
    "config": ["TestTunSystemRouteMarkInference", "TestTunSystemRouteMarkParse",
               "TestTunSystemRouteFreshParseValidation",
               "TestDirectSystemExcludedFromAutomaticProxyGroups"],
}


def run(*command, **kwargs):
    return subprocess.run(command, check=True, text=True, **kwargs)


def apply_patch(source, name):
    patch = ASSETS / name
    if not patch.is_file():
        raise RuntimeError(f"missing reviewed patch: {patch}")
    check = subprocess.run(
        ["git", "apply", "--check", str(patch)], cwd=source,
        capture_output=True, text=True,
    )
    if check.returncode == 0:
        run("git", "apply", str(patch), cwd=source)
        print(f"Applied {name}", flush=True)
        return
    reverse = subprocess.run(
        ["git", "apply", "--reverse", "--check", str(patch)], cwd=source,
        capture_output=True, text=True,
    )
    if reverse.returncode == 0:
        print(f"Already applied: {name}", flush=True)
        return
    raise RuntimeError(
        f"upstream changed; refuse to publish without reviewing {name}:\n{check.stderr}"
    )


def patch_smart_stream(source):
    path = source / "transport/anytls/session/stream.go"
    original = path.read_text()
    method = r"(?m)^func \(s \*Stream\) NetConn\(\) net\.Conn \{\s*return s\.sess\.conn\s*\}\n"
    _, count = re.subn(method, "", original)
    if count != 1 and (count != 0 or "NetConn" in original):
        raise RuntimeError("Smart ANYTLS NetConn changed; manual review required")
    test_path = path.with_name("stream_isolation_test.go")
    expected = (ASSETS / "smart-anytls-isolation_test.go.txt").read_text()
    if test_path.exists() and test_path.read_text() != expected:
        raise RuntimeError("refusing to replace an upstream ANYTLS regression test")
    if count:
        apply_patch(source, "smart-anytls-isolation.patch")
        print("Removed Smart ANYTLS shared-session NetConn", flush=True)
    else:
        print("Smart ANYTLS NetConn already absent; still require regression", flush=True)
    test_path.write_text(expected)


def patch_files(channel):
    files = ["native-system-route-direct.patch", "native-system-route-tun.patch"]
    if channel == "smart":
        files.extend(["smart-anytls-isolation.patch", "smart-anytls-isolation_test.go.txt"])
    return files


def fingerprints(channel):
    return {
        name: hashlib.sha256((ASSETS / name).read_bytes()).hexdigest()
        for name in patch_files(channel)
    }


def source_fingerprints(source, channel):
    paths = set()
    for name in patch_files(channel):
        if name.endswith(".patch"):
            paths.update(re.findall(r"(?m)^\+\+\+ b/(.+)$", (ASSETS / name).read_text()))
    if channel == "smart":
        paths.add("transport/anytls/session/stream_isolation_test.go")
    return {name: hashlib.sha256((source / name).read_bytes()).hexdigest() for name in sorted(paths)}


def verify_tests(source, channel, unit_only, sudo_tests):
    tests = {name: list(names) for name, names in BASE_TESTS.items()}
    if not unit_only:
        tests["listener/sing_tun"].extend([
            "TestSystemRouteBypassNamespace",
            "TestSystemRouteJournalNamespace",
        ])
        tests["listener"] = ["TestTunSystemRouteReloadNamespace"]
    if channel == "smart":
        tests["transport/anytls/session"] = ["TestStreamDoesNotExposeSharedSessionCloseWrite"]
    required = {(f"{MODULE}/{package}", test) for package, names in tests.items() for test in names}
    env = os.environ.copy()
    for variable in ("GOOS", "GOARCH", "GOAMD64", "GOARM", "GOMIPS"):
        env.pop(variable, None)
    env["MIHOMO_TUN_RULES_INTEGRATION"] = "0" if unit_only else "1"
    names = "^(" + "|".join(re.escape(test) for _, test in sorted(required)) + ")$"
    command = ["go", "test", "-json", "-count=1", "-tags", env.get("MIHOMO_TAGS", "with_gvisor"),
               "-run", names, *[f"./{package}" for package in tests]]
    if sudo_tests:
        # Keep source/cache ownership with the runner; only the test executables
        # need Linux namespace and socket-mark capabilities.
        command[2:2] = ["-exec", "sudo --preserve-env=MIHOMO_TUN_RULES_INTEGRATION"]
    return run_test_gate(source, command, env, required, ".msm-native-tests.jsonl")


def run_test_gate(source, command, env, required, log_name):
    passed, skipped = set(), set()
    print("Running native socket, parser and TUN regression gates", flush=True)
    with (source / log_name).open("w") as log:
        process = subprocess.Popen(command, cwd=source, env=env, stdout=subprocess.PIPE,
                                   stderr=subprocess.STDOUT, text=True)
        for line in process.stdout:
            log.write(line)
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                print(line, end="", flush=True)
                continue
            package, test, action = event.get("Package"), event.get("Test", ""), event.get("Action")
            if action == "pass" and (package, test) in required:
                passed.add((package, test))
                print(f"PASS {package}: {test}", flush=True)
            if action == "skip" and any(package == p and (test == t or test.startswith(t + "/"))
                                        for p, t in required):
                skipped.add((package, test))
            if action == "output":
                print(event.get("Output", ""), end="", flush=True)
        process.stdout.close()
        code = process.wait()
    if code or required - passed or skipped:
        raise RuntimeError(f"native verification failed: exit={code}, missing={sorted(required-passed)}, "
                           f"skipped={sorted(skipped)}")
    return sorted([f"{package}:{test}" for package, test in passed])


def verify_nonlinux_options(source):
    # Explicit file arguments exercise the real !linux implementation on the
    # Linux CI host. The only harness substitute is the unused Listener type;
    # no platform validation logic is copied or replaced.
    directory = source / "listener/sing_tun"
    env = os.environ.copy()
    for variable in ("GOOS", "GOARCH", "GOAMD64", "GOARM", "GOMIPS"):
        env.pop(variable, None)
    with tempfile.TemporaryDirectory(prefix=".msm-nonlinux-", dir=source) as temporary:
        root = Path(temporary)
        for name in ("system_route_other.go", "system_route_options_other_test.go"):
            (root / name).write_bytes((directory / name).read_bytes())
        stub = root / "listener_stub_test.go"
        stub.write_text("package sing_tun\n\ntype Listener struct{}\n")
        command = ["go", "test", "-json", "-count=1", "-run",
                   "^TestSystemRouteNonLinuxStaticValidation$",
                   *[str(root / name) for name in
                     ("system_route_other.go", "system_route_options_other_test.go", stub.name)]]
        required = {("command-line-arguments", "TestSystemRouteNonLinuxStaticValidation")}
        return run_test_gate(source, command, env, required, ".msm-nonlinux-tests.jsonl")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("channel", choices=("meta", "alpha", "smart"))
    parser.add_argument("source", type=Path, help="isolated upstream Git checkout; modified in place")
    parser.add_argument("--unit-only", action="store_true", help="local use; does not authorize packaging")
    parser.add_argument("--apply-only", action="store_true", help="local use; does not authorize packaging")
    parser.add_argument("--sudo-tests", action="store_true", help="run test executables with sudo on a CI VM")
    args = parser.parse_args()
    source = args.source.resolve()
    if not re.search(r"(?m)^module github[.]com/metacubex/mihomo$", (source / "go.mod").read_text()):
        raise RuntimeError("source is not a Mihomo module")
    top = Path(run("git", "rev-parse", "--show-toplevel", cwd=source, capture_output=True).stdout.strip())
    if top.resolve() != source:
        raise RuntimeError("source must be an isolated upstream Git checkout root")
    proof = source / PROOF_FILE
    proof.unlink(missing_ok=True)
    patch_hashes = fingerprints(args.channel)
    for patch in ("native-system-route-direct.patch", "native-system-route-tun.patch"):
        apply_patch(source, patch)
    if args.channel == "smart":
        patch_smart_stream(source)
    if args.apply_only:
        return
    source_hashes = source_fingerprints(source, args.channel)
    tests = verify_tests(source, args.channel, args.unit_only, args.sudo_tests)
    if not args.unit_only:
        tests += verify_nonlinux_options(source)
    if fingerprints(args.channel) != patch_hashes or source_fingerprints(source, args.channel) != source_hashes:
        raise RuntimeError("patches or source changed while tests ran; rerun verification")
    record = {
        "schema": 1,
        "channel": args.channel,
        "source_repository": "vernesong/mihomo" if args.channel == "smart" else "MetaCubeX/mihomo",
        "source_commit": run("git", "rev-parse", "HEAD", cwd=source, capture_output=True).stdout.strip(),
        "patch_revision": "msm.1",
        "features": ["direct-system", "tun.system-route-mark"],
        "verified": not args.unit_only,
        "tests": tests,
        "patch_sha256": patch_hashes,
        "patched_file_sha256": source_hashes,
    }
    proof.write_text(json.dumps(record, sort_keys=True, indent=2) + "\n")
    print(f"Native verification passed (packaging enabled: {record['verified']})", flush=True)


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError, subprocess.CalledProcessError) as error:
        print(f"ERROR: {error}", file=sys.stderr)
        sys.exit(1)
