#!/usr/bin/env bash
# Build an unmodified, pinned MetaCubeX/mihomo upstream checkout.
# All outputs stay outside the upstream source tree.
set -euo pipefail

source_dir="$(realpath "$1")"
output_dir="$(realpath -m "$2")"
asset_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

channel="${CHANNEL:?CHANNEL is required}"
version="${CHANNEL_VERSION:?CHANNEL_VERSION is required}"
expected_commit="${EXPECTED_COMMIT:?EXPECTED_COMMIT is required}"
suffix="${ASSET_SUFFIX:?ASSET_SUFFIX is required}"
package_format="${PACKAGE_FORMAT:?PACKAGE_FORMAT is required}"

case "$output_dir/" in
  "$source_dir/"*) echo 'build output must be outside upstream source' >&2; exit 1 ;;
esac
case "$version" in
  *-msm.*) echo 'MSM patch versions are not allowed for unmodified upstream builds' >&2; exit 1 ;;
esac

python3 - "$asset_dir" "$channel" "$suffix" "$package_format" <<'PY'
import importlib.util
import os
from pathlib import Path
import sys

sys.dont_write_bytecode = True
scripts, channel, suffix, package_format = sys.argv[1:]
if channel not in ("meta", "alpha"):
    raise SystemExit(f"unsupported upstream build channel: {channel}")
spec = importlib.util.spec_from_file_location("targets", Path(scripts) / "targets.py")
targets = importlib.util.module_from_spec(spec)
spec.loader.exec_module(targets)
targets.validate_target(channel, suffix, package_format, os.environ)
PY

cd "$source_dir"
actual_commit="$(git rev-parse HEAD)"
test "$actual_commit" = "$expected_commit"
test -z "$(git status --porcelain --untracked-files=all)"

export CGO_ENABLED=0 GOWORK=off
mkdir -p "$output_dir"
build_dir="$(mktemp -d)"
trap 'rm -rf -- "$build_dir"' EXIT

binary=mihomo
if [ "${GOOS:?GOOS is required}" = windows ]; then
  binary=mihomo.exe
fi
tags="${MIHOMO_TAGS:-with_gvisor}"
build_time="$(date '+%Y-%m-%d %H:%M:%S %Z')"
ldflags="-X 'github.com/metacubex/mihomo/constant.Version=${version}' -X 'github.com/metacubex/mihomo/constant.BuildTime=${build_time}' -s -w -buildid="

go build -v -mod=readonly -buildvcs=true -trimpath \
  -ldflags "$ldflags" \
  -tags "$tags" \
  -o "${build_dir}/${binary}"

go mod verify
test -z "$(git status --porcelain --untracked-files=all)"

go version -m "${build_dir}/${binary}" > "${build_dir}/build-info.txt"
test "$(sed -n 's/.*vcs.revision=//p' "${build_dir}/build-info.txt")" = "$expected_commit"
test "$(sed -n 's/.*vcs.modified=//p' "${build_dir}/build-info.txt")" = false

case "$channel/$package_format" in
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

if [ "$suffix" = linux-amd64 ]; then
  python3 - "$output_dir" "$build_dir/build-info.txt" "$channel" "$version" "$expected_commit" "$tags" <<'PY'
import hashlib
import json
from pathlib import Path
import subprocess
import sys

out_dir, build_info_path, channel, version, commit, tags = sys.argv[1:]
dep_files = {}
for name in ("go.mod", "go.sum"):
    p = Path(name)
    if p.is_file():
        dep_files[name] = hashlib.sha256(p.read_bytes()).hexdigest()
metadata = {
    "schema": 2,
    "channel": channel,
    "source_repository": "MetaCubeX/mihomo",
    "source_commit": commit,
    "version": version,
    "patched": False,
    "source_modified": False,
    "dependency_files": dep_files,
    "build_tags": tags.split(),
    "build_info": Path(build_info_path).read_text(),
    "go_version": subprocess.check_output(["go", "version"], text=True).strip(),
}
(Path(out_dir) / f"mihomo-{channel}-source.json").write_text(json.dumps(metadata, sort_keys=True, indent=2) + "\n")
PY
fi

if [ -n "${GITHUB_OUTPUT:-}" ]; then
  echo "asset_name=${asset_name}" >> "$GITHUB_OUTPUT"
fi
echo "Built unmodified upstream ${asset_name} (${version}, commit ${expected_commit})"
