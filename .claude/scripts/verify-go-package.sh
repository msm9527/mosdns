#!/bin/zsh
emulate -L zsh
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel)"
FILE="${1:-}"

if [[ -z "$FILE" ]]; then
  echo "usage: .claude/scripts/verify-go-package.sh <file>"
  exit 1
fi

cd "$ROOT"

if [[ "$FILE" == "$ROOT/"* ]]; then
  REL="${FILE#"$ROOT"/}"
else
  REL="$FILE"
fi

if [[ "$REL" != *.go ]]; then
  echo "skip: $REL is not a Go file"
  exit 0
fi

case "$REL" in
  main.go)
    PKG="."
    ;;
  e2e/*)
    PKG="./e2e"
    ;;
  *)
    PKG="./$(dirname "$REL")"
    ;;
esac

echo "=== Go package verify ==="
echo "file: $REL"
echo "pkg:  $PKG"

echo "[1/3] gofmt..."
UNFORMATTED="$(gofmt -l "$REL" || true)"
if [[ -n "$UNFORMATTED" ]]; then
  echo "❌ gofmt required:"
  echo "$UNFORMATTED"
  exit 1
fi

echo "[2/3] go test $PKG..."
if ! go test "$PKG" -count=1 2>&1 | head -40; then
  echo "❌ go test failed: $PKG"
  exit 1
fi

echo "[3/3] go vet $PKG..."
if ! go vet "$PKG" 2>&1 | head -40; then
  echo "❌ go vet failed: $PKG"
  exit 1
fi

echo "✅ package verify passed"
