#!/usr/bin/env bash
set -euo pipefail

# Run from the immutable upstream sing-box checkout. Patch a private dependency
# copy so the shared, verified Go module cache always remains unchanged.
patch_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/patches" && pwd)"
git apply --check "${patch_dir}/clash-api-context.patch"
git apply "${patch_dir}/clash-api-context.patch"

module='github.com/sagernet/sing-shadowsocks'
expected='v0.2.9-0.20260929204512-65740e0f0e3e'
test "$(go list -m -f '{{.Version}}' "$module")" = "$expected"
go mod download "$module"
module_source="$(go list -m -f '{{.Dir}}' "$module")"
dependency_dir="$(mktemp -d "${TMPDIR:-/tmp}/msm-singbox-shadowsocks.XXXXXXXX")"
cp -R "${module_source}/." "$dependency_dir/"
chmod -R u+w "$dependency_dir"
git -C "$dependency_dir" apply --check "${patch_dir}/shadowsocks-udp-family.patch"
git -C "$dependency_dir" apply "${patch_dir}/shadowsocks-udp-family.patch"
go mod edit "-replace=${module}=${dependency_dir}"
printf 'Applied instance context and UDP family patches to %s\n' "$(git rev-parse HEAD)"
