#!/bin/zsh
emulate -L zsh
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

echo "=== mosdns repo verify ==="

run_and_print_head() {
  local label="$1"
  shift

  local tmp
  tmp="$(mktemp)"
  if ! "$@" >"$tmp" 2>&1; then
    sed -n '1,80p' "$tmp"
    rm -f "$tmp"
    echo "❌ ${label} failed"
    exit 1
  fi

  sed -n '1,80p' "$tmp"
  rm -f "$tmp"
}

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
run_and_print_head "go test" go test ./... -count=1

echo "[3/4] go vet ./..."
run_and_print_head "go vet" go vet ./...

echo "[4/4] go build ./..."
run_and_print_head "go build" go build ./...

echo "✅ repo verify passed"
