# インストール手順

masudaをローカルPCで動かすためのセットアップ手順。全体設計は
`docs/design/sandbox-workflow.md`、個々の設計判断は`docs/adr/`を参照。

## 前提条件

masudaはホスト側CLI（Go製）とDockerサンドボックスの2層構成。以下がホスト側に必要。

- **Go 1.26.3以上**（`go.mod`が要求するバージョン。CLIのビルドに使用）
- **Docker**（フェーズ4-5用サンドボックスコンテナの起動に使用）
- **git**（worktree操作。2.5以上、`git worktree`が使えるバージョン）
- **tmux**（フェーズ1-2のホスト側自己ループ用セッション。`masuda plan chat`でのアタッチにも使用）
- **Claude Code CLI**がホスト上にインストール済み、かつ`claude`でログイン済みであること
  - サブスクリプション認証をそのままDocker内のClaude Codeに引き継ぐ方式（ADR-0001、従量課金なし）のため、`~/.claude/.credentials.json`と`~/.claude.json`がホスト上に存在している必要がある。未ログインの場合、コンテナ起動時に`masuda sandbox start`がエラーで失敗する

## 1. リポジトリの取得

```bash
git clone <このリポジトリのURL> masuda
cd masuda
```

## 2. masuda CLIのビルド

```bash
go build -o masuda ./cmd/masuda
```

`$PATH`の通ったディレクトリに置くと以後`masuda`コマンドとして直接呼べる。

```bash
sudo mv masuda /usr/local/bin/masuda
# または
go install ./cmd/masuda   # $(go env GOPATH)/bin/masuda に配置される
```

## 3. サンドボックスDockerイメージのビルド

フェーズ4-5はDockerコンテナ内で動く。まずベースイメージをビルドする。

```bash
docker build -t masuda-loop .
```

対象リポジトリの言語に応じてLSPツール同梱の言語バリアント（ADR-0015）を追加でビルドできる。いずれもベースイメージ`masuda-loop:latest`から派生するため、先に上記のベースビルドを済ませておくこと。

```bash
docker build -f docker/go/Dockerfile         -t masuda-loop:go         .
docker build -f docker/python/Dockerfile     -t masuda-loop:python     .
docker build -f docker/typescript/Dockerfile -t masuda-loop:typescript .
docker build -f docker/full/Dockerfile       -t masuda-loop:full       .
```

言語バリアントを使わない場合、この手順は不要（`masuda-loop`のみで動く）。

## 4. 対象リポジトリ側の設定（任意）

masudaで作業したい対象リポジトリのルートに`.masuda.json`を置くと、`--image`・`--base`フラグの毎回指定を省略できる（ADR-0015）。

```json
{
  "image": "masuda-loop:go",
  "base": "main"
}
```

- `image`: `masuda sandbox start` / `masuda review start`が使うDockerイメージ（未指定時は`masuda-loop`）
- `base`: worktree作成・マージ先のデフォルトブランチ（未指定時は`develop`）

このファイルはmasuda自体のリポジトリではなく、**masudaで作業したい対象リポジトリ**の直下に置く。

## 5. 動作確認

```bash
cd <対象リポジトリのルート>
masuda workspace list
```

エラーなく（空でも）一覧が表示されればセットアップは完了。

## 6. 最短の使い方

```bash
# 1. 新しいタスクを開始（worktree作成 + フェーズ1-2の自己ループ起動）
masuda plan start <branch> "実装したいタスクの説明"

# 2. プラン(PLAN.md)を確認し、承認 or 差し戻し
masuda plan show <workspace-id>
masuda plan approve <workspace-id>
# もしくは対話で相談したい場合
masuda plan chat <workspace-id>

# 3. G1承認後、実装・レビュー用サンドボックスを起動（フェーズ4-5が自動で進む)
masuda sandbox start <workspace-id>

# 4. 最終レポート(final_report.md)を確認し、承認 or 差し戻し
masuda review show <workspace-id>
masuda review approve <workspace-id>   # ローカルマージ + worktree/コンテナ/状態ディレクトリの後片付け（push はしない)
```

既存ブランチをレビューだけしたい場合は、フェーズ0-4を全てスキップして直接フェーズ5に入る`masuda review start <branch>`が使える。

```bash
masuda review start <branch> --base develop
```

`workspace-id`は`masuda workspace list`で確認できる。各コマンドの詳細は`masuda <command> --help`、内部設計は`CLAUDE.md`の「現状」節を参照。
