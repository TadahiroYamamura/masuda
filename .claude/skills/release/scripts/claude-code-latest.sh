#!/usr/bin/env bash
# ゲストのClaude Codeの最新版を出す（SKILL.mdの1-0）。使い方:
#   claude-code-latest.sh             最新版（例 2.1.288）を1行で出す
#   claude-code-latest.sh --base-url  配布元のベースURLを出す（precheck.shが版の存在確認に使う）
#
# BASE_URLは https://claude.ai/install.sh のDOWNLOAD_BASE_URLの写し。install.shは引数なしのとき
# "$DOWNLOAD_BASE_URL/latest"が返す版を入れ、版を渡すと"$DOWNLOAD_BASE_URL/<版>/manifest.json"を引く。
# 写しが古くなったことに気づけるよう、毎回install.shを取ってきて定義が同じかを確かめ、違えば止まる。
set -euo pipefail

BASE_URL="https://downloads.claude.ai/claude-code-releases"
INSTALL_SH="https://claude.ai/install.sh"

if [ "${1:-}" = "--base-url" ]; then
  echo "$BASE_URL"
  exit 0
fi

upstream=$(curl -fsSL "$INSTALL_SH" | sed -n 's/^DOWNLOAD_BASE_URL="\([^"]*\)".*/\1/p' | head -1)
if [ "$upstream" != "$BASE_URL" ]; then
  echo "install.shのDOWNLOAD_BASE_URLが変わった: '$upstream'（このスクリプトは'$BASE_URL'）。BASE_URLを直す" >&2
  exit 1
fi

latest=$(curl -fsSL "$BASE_URL/latest")
if ! [[ "$latest" =~ ^[0-9]+\.[0-9]+\.[0-9]+ ]]; then
  echo "版の形でない応答: '$latest'" >&2
  exit 1
fi
echo "$latest"
