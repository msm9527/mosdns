#!/usr/bin/env bash
# Build a clean, pinned upstream checkout. All outputs stay outside the source tree.
set -euo pipefail

source_dir="$(realpath "$1")"
output_dir="$(realpath -m "$2")"
: "${EXPECTED_COMMIT:?required}"
: "${SOURCE_VERSION:?required}"
case "$output_dir/" in
  "$source_dir/"*) echo 'build output must be outside upstream source' >&2; exit 1 ;;
esac
cd "$source_dir"
test "$(git rev-parse HEAD)" = "$EXPECTED_COMMIT"
test -z "$(git status --porcelain --untracked-files=all)"
case "$SOURCE_VERSION" in *-msm.*) echo 'MSM patch versions are not allowed' >&2; exit 1 ;; esac

export CGO_ENABLED=0 GOWORK=off
mkdir -p "$output_dir"
tags="$(tr ',\n' '  ' < release/DEFAULT_BUILD_TAGS_OTHERS | xargs)"
ldflags="-X 'github.com/sagernet/sing-box/constant.Version=${SOURCE_VERSION}' $(cat release/LDFLAGS) -s -w -buildid="
output="$output_dir/sing-box"
if [ "${GOOS:-$(go env GOOS)}" = windows ]; then output+='.exe'; fi
go build -mod=readonly -buildvcs=true -trimpath -ldflags "$ldflags" -tags "$tags" -o "$output" ./cmd/sing-box
go mod verify
test -z "$(git status --porcelain --untracked-files=all)"
go version -m "$output" > "$output_dir/build-info.txt"
test "$(sed -n 's/.*vcs.revision=//p' "$output_dir/build-info.txt")" = "$EXPECTED_COMMIT"
test "$(sed -n 's/.*vcs.modified=//p' "$output_dir/build-info.txt")" = false

python3 - "$output_dir" "$tags" <<'PY'
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys

metadata = {
    'commit': os.environ['EXPECTED_COMMIT'],
    'version': os.environ['SOURCE_VERSION'],
    'patched': False,
    'source_modified': False,
    'dependency_files': {name: hashlib.sha256(Path(name).read_bytes()).hexdigest() for name in ('go.mod', 'go.sum')},
    'build_tags': sys.argv[2].split(),
    'build_info': (Path(sys.argv[1]) / 'build-info.txt').read_text(),
    'go_version': subprocess.check_output(['go', 'version'], text=True).strip(),
}
(Path(sys.argv[1]) / 'build.json').write_text(json.dumps(metadata, indent=2) + '\n')
PY
