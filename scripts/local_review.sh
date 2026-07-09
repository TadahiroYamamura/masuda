#!/usr/bin/env bash
# ローカルでAI PRレビューの一連の流れ（メタ情報取得 → diff計算 → レンダリング → レビュー実行）を
# GitHub Actionsを介さずに試すためのラッパー。
#
# masudaのレビューワークフロー(templates/github-workflows/review.yml)と同じ構造で、
# 「レビュー対象リポジトリ」のディレクトリで実行し、masudaのスクリプトは絶対パスで呼び出す。
# レビュー対象リポジトリ自身をレビューしたい場合はそのリポジトリのルートで実行し、
# masuda自身をレビューしたい場合はmasudaのリポジトリルートで実行する。
#
# Usage (レビュー対象リポジトリのディレクトリで実行):
#   /path/to/masuda/scripts/local_review.sh                        # 現在のブランチ vs developブランチ の差分をレビュー
#   /path/to/masuda/scripts/local_review.sh <base_ref> <head_ref>    # 任意のref同士の差分をレビュー
#   /path/to/masuda/scripts/local_review.sh --pr <PR番号>            # 実際のPR(gh pr view)のメタ情報・diffを使う
#
# 事前準備:
#   - ANTHROPIC_API_KEY を環境変数に設定するか、masudaリポジトリ直下に .env として置いておく
#     （従量課金APIを直接呼ぶため実行するとコストが発生する）
#   - --pr を使う場合は gh コマンドでログイン済みであること
#   - masuda側のvenv (venv/bin/python) が作成済みであること（未作成ならシステムのpythonにフォールバック）

set -euo pipefail

MASUDA_HOME="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [ -x "$MASUDA_HOME/venv/bin/python" ]; then
  PYTHON="$MASUDA_HOME/venv/bin/python"
else
  PYTHON="python3"
fi

# ANTHROPIC_API_KEYが未設定なら、呼び出し元のシェルを汚さずmasuda/.envから読み込む
if [ -z "${ANTHROPIC_API_KEY:-}" ] && [ -f "$MASUDA_HOME/.env" ]; then
  set -a
  # shellcheck disable=SC1090,SC1091
  source "$MASUDA_HOME/.env"
  set +a
fi

if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
  echo "エラー: 環境変数 ANTHROPIC_API_KEY が未設定です（$MASUDA_HOME/.env にも見つかりません）" >&2
  exit 1
fi

if ! git rev-parse --show-toplevel > /dev/null 2>&1; then
  echo "エラー: gitリポジトリの中で実行してください（レビュー対象リポジトリのディレクトリに移動してから実行）" >&2
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
"$PYTHON" "$MASUDA_HOME/render_pr_context.py" --meta "$meta_json" --diff "$diff_file" -o "$context_file"

# langgraph_orchestrator.pyはカレントディレクトリ配下にreview_results/を作るので、
# レビュー対象リポジトリのルートに移動してから実行する（結果がそこに残る）
cd "$(git rev-parse --show-toplevel)"
"$PYTHON" "$MASUDA_HOME/langgraph_orchestrator.py" --pr-file "$context_file"
