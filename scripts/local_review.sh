#!/usr/bin/env bash
# ローカルでAI PRレビューの一連の流れ（メタ情報取得 → diff計算 → レンダリング → レビュー実行）を
# GitHub Actionsを介さずに試すためのラッパー。
#
# Usage:
#   scripts/local_review.sh                      # 現在のブランチ vs developブランチ の差分をレビュー
#   scripts/local_review.sh <base_ref> <head_ref>  # 任意のref同士の差分をレビュー
#   scripts/local_review.sh --pr <PR番号>          # 実際のPR(gh pr view)のメタ情報・diffを使う
#
# 事前準備:
#   - venv を有効化: source venv/bin/activate
#   - ANTHROPIC_API_KEY を環境変数に設定（従量課金APIを直接呼ぶため実行するとコストが発生する）
#   - --pr を使う場合は gh コマンドでログイン済みであること

set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
  echo "エラー: 環境変数 ANTHROPIC_API_KEY が未設定です" >&2
  exit 1
fi

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

meta_json="$WORKDIR/pr_meta.json"
diff_file="$WORKDIR/pr.diff"

if [ "${1:-}" = "--pr" ]; then
  pr_number="${2:?PR番号を指定してください: scripts/local_review.sh --pr <PR番号>}"
  gh pr view "$pr_number" --json title,body,comments,baseRefOid,headRefOid > "$meta_json"
  git diff "$(jq -r .baseRefOid "$meta_json")" "$(jq -r .headRefOid "$meta_json")" > "$diff_file"
else
  base_ref="${1:-develop}"
  head_ref="${2:-HEAD}"
  title="$(git log -1 --format=%s "$head_ref")"
  body="$(git log -1 --format=%b "$head_ref")"
  jq -n --arg title "$title" --arg body "$body" \
    '{title: $title, body: $body, comments: []}' > "$meta_json"
  git diff "$base_ref" "$head_ref" > "$diff_file"
fi

if [ ! -s "$diff_file" ]; then
  echo "エラー: diffが空です（比較対象のrefを確認してください）" >&2
  exit 1
fi

context_file="$WORKDIR/pr_context.md"
python render_pr_context.py --meta "$meta_json" --diff "$diff_file" -o "$context_file"
python langgraph_orchestrator.py --pr-file "$context_file"
