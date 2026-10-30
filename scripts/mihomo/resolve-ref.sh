#!/usr/bin/env bash
set -euo pipefail
repository="${1:?repository is required}"
ref="${2:?fully qualified ref is required}"
refs="$(git ls-remote --exit-code "https://github.com/${repository}.git" "$ref" "${ref}^{}")"
commit="$(printf '%s\n' "$refs" | awk -v ref="$ref" '$2 == ref { plain=$1 } $2 == ref "^{}" { peeled=$1 } END { print peeled ? peeled : plain }')"
if ! [[ "$commit" =~ ^[0-9a-f]{40}$ ]]; then
  echo "failed to resolve immutable ${repository} ${ref}" >&2
  exit 1
fi
printf '%s\n' "$commit"
