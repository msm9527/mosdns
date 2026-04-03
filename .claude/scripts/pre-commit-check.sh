#!/bin/zsh
emulate -L zsh
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

echo "=== 提交前检查 ==="

STAGED="$(git diff --cached --name-only --diff-filter=ACMR 2>/dev/null || true)"
if [[ -z "$STAGED" ]]; then
  echo "没有暂存文件，跳过检查"
  exit 0
fi

echo "[1/4] 检查敏感文件..."
SENSITIVE_PATTERNS=("\\.env$" "\\.env\\." "credentials" "secret" "\\.key$" "\\.pem$" "password" "token.*\\.json")
for pattern in "${SENSITIVE_PATTERNS[@]}"; do
  MATCHES="$(echo "$STAGED" | grep -iE "$pattern" || true)"
  if [[ -n "$MATCHES" ]]; then
    echo "❌ 检测到敏感文件："
    echo "$MATCHES"
    exit 1
  fi
done

echo "[2/4] 扫描硬编码密钥..."
STAGED_CONTENT="$(git diff --cached -U0 2>/dev/null || true)"
if echo "$STAGED_CONTENT" | grep -qiE '(sk-[A-Za-z0-9]{20,}|AKIA[A-Z0-9]{16}|ghp_[A-Za-z0-9]{36})'; then
  echo "❌ 检测到疑似硬编码密钥，请检查后再提交"
  exit 1
fi

echo "[3/4] 检查 Go 格式..."
STAGED_GO=()
while IFS= read -r line; do
  [[ -n "$line" ]] || continue
  STAGED_GO+=("$line")
done < <(git diff --cached --name-only --diff-filter=ACMR -- '*.go')
if [[ "${#STAGED_GO[@]}" -gt 0 ]]; then
  UNFORMATTED="$(gofmt -l "${STAGED_GO[@]}" || true)"
  if [[ -n "$UNFORMATTED" ]]; then
    echo "❌ 以下 Go 文件未格式化："
    echo "$UNFORMATTED"
    exit 1
  fi
fi

echo "[4/4] 校验变更包..."
if [[ "${#STAGED_GO[@]}" -gt 0 ]]; then
  packages=()
  seen=""
  for file in "${STAGED_GO[@]}"; do
    case "$file" in
      main.go)
        pkg="."
        ;;
      e2e/*)
        pkg="./e2e"
        ;;
      *)
        pkg="./$(dirname "$file")"
        ;;
    esac
    case " $seen " in
      *" $pkg "*) ;;
      *)
        packages+=("$pkg")
        seen="$seen $pkg"
        ;;
    esac
  done

  if ! go test "${packages[@]}" -count=1 2>&1 | head -80; then
    echo "❌ staged package go test failed"
    exit 1
  fi

  if ! go vet "${packages[@]}" 2>&1 | head -80; then
    echo "❌ staged package go vet failed"
    exit 1
  fi
fi

echo "✅ 提交前检查通过"
