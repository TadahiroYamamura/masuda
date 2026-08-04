# 作業ループ仕様

## ループルール

masuda自身の制御ファイル（`TASK.md`・ゲートマーカー等）は`/workspace`（対象リポジトリの
worktree）ではなく`/masuda-state`配下に置かれる（下記「注意」参照）。以下の`TASK.md`は
すべて`/masuda-state/TASK.md`を指す。

1. `/masuda-state/TASK.md` が存在しなければ → LangGraph を起動して生成させ、2 へ
2. `/masuda-state/TASK.md` を読み、指示に従って作業を行う
3. 作業完了後、**終了条件** と **ゲート条件** を確認する
   - 終了条件を満たしていれば → 以下のコマンドを実行してセッションを終了する（コミット・質問・確認は不要）
     ```bash
     tmux kill-session -t $(tmux display-message -p '#S')
     ```
   - ゲート条件を満たしていれば → 4へ
   - どちらも満たしていなければ → LangGraph を起動して `/masuda-state/TASK.md` を上書きさせ、2 へ戻る
4. ゲートマーカー（`/masuda-state/.masuda-gate/<name>.json`、`<name>`はTASK.mdの`GATE:<name>`から読み取る）の
   `status`が`pending`でなくなるまで待機する。`while`ループ構文（Monitorツールの内部実装を含む）で
   ポーリングすると、動的な文字列を含むコマンドとして確認を求められ無人ループで詰まることが実機で
   確認されているため、`while`ループもMonitorツールも使わないこと。代わりに`inotifywait`を
   ループなしの単発ブロッキング呼び出しで使うこと（実機検証済み、確認プロンプトは発生しない）:
   ```bash
   inotifywait -e modify,close_write,move_self $GATE_FILE
   ```
   - 待機中に人間が`docker exec -it ... tmux attach`（`masuda chat`）で接続し、対話の中で「進めていい」と伝えられた場合は、上記の待機を打ち切り、自分自身で`$GATE_FILE`に以下の形式で承認マーカーを書いてよい（却下の場合は`status`を`"rejected"`にする）
     ```json
     {"status": "approved", "feedback": "<対話の要約>", "decided_at": "<ISO8601形式の現在時刻>"}
     ```
   - マーカーの`status`が`pending`でなくなったら（自分で書いた場合・別ターミナルの`masuda plan/review approve|reject`で書かれた場合のどちらでも）、2へ戻ってLangGraphを起動する

## 終了条件

`TASK.md` の本文に `DONE` という文字列が含まれていること（`GATE:<name>`とは排他）

## ゲート条件

`TASK.md` の本文に `GATE:<name>` という文字列が含まれていること（`<name>`は`plan`または`review`）

## LangGraph 起動コマンド

```bash
MASUDA_STATE_DIR=/masuda-state /opt/masuda/venv/bin/python /opt/masuda/orchestrator/implement_review_graph.py
```

## 注意

masuda自身の制御ファイル（TASK.md・`plan/`・`.masuda-gate/`・`review_results/`等）は
`/masuda-state`配下に置かれる（対象リポジトリ＝`/workspace`の`git status`を汚さない
ため）。ゲートマーカーの`$GATE_FILE`もこの配下（例: `/masuda-state/.masuda-gate/review.json`）
を指す。コード自体の実装・レビューはこれまで通り`/workspace`に対して行う。
