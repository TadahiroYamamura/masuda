# masuda

AI（Claude）によるPRレビューを自動化するツール群。**このリポジトリ自体はレビュー対象のプロダクトコードを持たない** —
他リポジトリのGitHub Actionsから「レビューツール提供元」としてチェックアウトされ、そこから呼び出される。

## 全体の流れ

1. レビュー対象リポジトリのPRで `@ai/review` とコメントされる
2. そのリポジトリの `.github/workflows/review.yml`（`templates/github-workflows/review.yml` を配置したもの）が起動
3. ワークフローがこのmasudaリポジトリをサブディレクトリにチェックアウトし、`requirements.txt` をインストール
4. `render_pr_context.py` がPRのメタ情報（`gh pr view`の出力）と `git diff` を1つのMarkdownにまとめる
5. `langgraph_orchestrator.py` がそのMarkdownを読み、観点ごとにレビューを実行し `review_results/` に結果を出力

## 主要ファイル

| ファイル | 役割 |
|---|---|
| `render_pr_context.py` | `gh pr view --json ...` の出力とdiffを1つのテキスト(`pr_context.md`)にまとめる |
| `langgraph_orchestrator.py` | LangGraphでレビューグラフを構築・実行するエントリポイント |
| `perspectives/config.py` | レビュー観点(`PERSPECTIVES`)の定義。観点ごとに `review_prompt` / `checker_prompt` を持つ |
| `templates/github-workflows/review.yml` | **レビュー対象リポジトリ側に配置するワークフローのテンプレート**。このリポジトリの `.github/workflows/` には置かない（後述） |
| `scripts/local_review.sh` | ローカルでレビューを試すためのラッパースクリプト |

`agents/` ディレクトリは初期PoCの名残で現在未使用（サブエージェント委譲方式を廃止し、LangGraphの直接API呼び出し方式に移行したため）。

## レビューグラフの構造 (`langgraph_orchestrator.py`)

観点ごとに `review_node → check_node` を実行する。`check_node` がNGと判定した場合は同じ観点を
`MAX_RETRIES`回までやり直す（redo）。全観点が終わったら `synthesize_node` が最終レポート
(`review_results/final_report.md`)を生成する。

無限ループ対策として3種類の閾値を持つ（`ABORT_THRESHOLD` / `TOKEN_BUDGET` / `ITERATION_BUDGET`）。
観点数(`PERSPECTIVES`の要素数)を変更した場合は、`langgraph_orchestrator.py` 冒頭のコメントに従って
これらの閾値を手動で見直すこと。

- レビュー観点を追加・修正する場合は `perspectives/config.py` の `PERSPECTIVES` を編集する
- レビューア用モデルとチェッカー用モデルは環境変数 `REVIEW_MODEL` / `CHECK_MODEL` で上書き可能（デフォルトはコード参照）

## `.github/workflows/` を空にしている理由

`templates/github-workflows/review.yml` は**レビュー対象リポジトリに配置するファイル**であり、
masuda自身のCIワークフローではない。`.github/workflows/` に置くと、GitHub側やAIエージェントが
「masudaリポジトリ自身のPRに対して動く自動レビューワークフロー」と誤認してしまうため、
`templates/` 配下に退避している。レビュー対象リポジトリへ導入する際は、このファイルを
そのリポジトリの `.github/workflows/review.yml` としてコピーする。

## ローカルでの動作確認

`scripts/local_review.sh` を使う（詳細は当該スクリプトのコメント、またはREADME参照）。
`ANTHROPIC_API_KEY` が必要（Anthropicの従量課金APIを直接呼ぶため、実行するとコストが発生する）。

## 開発環境

- `venv/` にPython venvがあり、`requirements.txt` の依存関係をインストール済み
- テストは `pytest` を使う想定だが、現時点でテストコードは未整備
