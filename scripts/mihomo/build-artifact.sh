#!/usr/bin/env bash
# Build only sources that passed verify-source.py with all native gates enabled.
set -euo pipefail

asset_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
channel="${CHANNEL:?CHANNEL is required}"
version="${CHANNEL_VERSION:?CHANNEL_VERSION is required}"
suffix="${ASSET_SUFFIX:?ASSET_SUFFIX is required}"
output_dir="${OUTPUT_DIR:?OUTPUT_DIR is required}"
package_format="${PACKAGE_FORMAT:?PACKAGE_FORMAT is required}"
source_commit="$(git rev-parse HEAD)"
source_short="${source_commit:0:12}"

python3 - "$asset_dir" "$channel" "$version" "$source_commit" "$suffix" "$package_format" <<'PY'
import importlib.util
import json
import os
from pathlib import Path
import re
import sys

sys.dont_write_bytecode = True
scripts, channel, version, commit, suffix, package_format = sys.argv[1:]
spec = importlib.util.spec_from_file_location("verify_source", Path(scripts) / "verify-source.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
target_spec = importlib.util.spec_from_file_location("targets", Path(scripts) / "targets.py")
targets = importlib.util.module_from_spec(target_spec)
target_spec.loader.exec_module(targets)
targets.validate_target(channel, suffix, package_format, os.environ)
proof = json.loads(Path(module.PROOF_FILE).read_text())
revision = module.patch_revision(channel)
expected = {"schema": 1, "channel": channel, "source_commit": commit,
            "patch_revision": revision, "features": module.features(channel),
            "verified": True, "patch_sha256": module.fingerprints(channel),
            "race_tests": module.selection_test_names(channel),
            "patched_file_sha256": module.source_fingerprints(Path("."), channel)}
if any(proof.get(key) != value for key, value in expected.items()):
    raise SystemExit("source has not passed the exact required native patch/test gate")
if not re.fullmatch(r"[0-9A-Za-z][0-9A-Za-z.+_-]*-" + re.escape(revision), version):
    raise SystemExit("version must explicitly identify the native MSM patch revision")
if channel == "smart" and version != f"alpha-smart-{commit[:12]}-{revision}":
    raise SystemExit("Smart version does not identify the verified upstream source")
PY

mkdir -p "$output_dir"
output_dir="$(cd -- "$output_dir" && pwd)"
build_dir="$(mktemp -d)"
trap 'rm -rf -- "$build_dir"' EXIT
binary=mihomo
if [ "${GOOS:?GOOS is required}" = windows ]; then
  binary=mihomo.exe
fi
build_time="$(date '+%Y-%m-%d %H:%M:%S %Z')"
go build -v -trimpath \
  -ldflags "-X 'github.com/metacubex/mihomo/constant.Version=${version}' -X 'github.com/metacubex/mihomo/constant.BuildTime=${build_time}' -s -w -buildid=" \
  -tags "${MIHOMO_TAGS:-with_gvisor}" \
  -o "${build_dir}/${binary}"

case "$channel/$package_format" in
  smart/gz)
    asset_name="mihomo-${suffix}-alpha-smart-${source_short}.gz"
    gzip -n -c "${build_dir}/${binary}" > "${output_dir}/${asset_name}"
    ;;
  meta/tar.gz|alpha/tar.gz)
    asset_name="mihomo-${channel}-${suffix}.tar.gz"
    chmod +x "${build_dir}/${binary}"
    tar -C "$build_dir" --no-same-owner -czf "${output_dir}/${asset_name}" "$binary"
    ;;
  meta/exe|alpha/exe)
    asset_name="mihomo-${channel}-${suffix}.exe"
    mv "${build_dir}/${binary}" "${output_dir}/${asset_name}"
    ;;
  *)
    echo "unsupported channel/package: ${channel}/${package_format}" >&2
    exit 1
    ;;
esac

if [ "$suffix" = linux-amd64 ] || [ "$suffix" = linux-amd64-v1 ]; then
  cp .msm-native-verification.json "${output_dir}/mihomo-${channel}-source.json"
fi
if [ -n "${GITHUB_OUTPUT:-}" ]; then
  echo "asset_name=${asset_name}" >> "$GITHUB_OUTPUT"
fi
echo "Built ${asset_name} (${version}, upstream ${source_commit})"
