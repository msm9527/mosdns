#!/bin/zsh
emulate -L zsh
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

echo "=== mosdns repo verify ==="

echo "[1/4] gofmt..."
CHANGED_GO=()
while IFS= read -r line; do
  [[ -n "$line" ]] || continue
  CHANGED_GO+=("$line")
done < <(
  {
    git diff --name-only --diff-filter=ACMR HEAD -- '*.go'
    git ls-files --others --exclude-standard -- '*.go'
  } | awk 'NF' | sort -u
)
if [[ "${#CHANGED_GO[@]}" -gt 0 ]]; then
  UNFORMATTED="$(gofmt -l "${CHANGED_GO[@]}" || true)"
  if [[ -n "$UNFORMATTED" ]]; then
    echo "❌ gofmt required for changed files:"
    echo "$UNFORMATTED"
    exit 1
  fi
else
  echo "skip: no changed Go files"
fi

echo "[2/4] go test ./..."
if ! go test ./... -count=1 2>&1 | head -80; then
  echo "❌ go test failed"
  exit 1
fi

echo "[3/4] go vet ./..."
if ! go vet ./... 2>&1 | head -80; then
  echo "❌ go vet failed"
  exit 1
fi

echo "[4/4] go build ./..."
if ! go build ./... 2>&1 | head -80; then
  echo "❌ go build failed"
  exit 1
fi

echo "✅ repo verify passed"
