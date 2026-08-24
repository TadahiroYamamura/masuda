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
4. `mcp__masuda-gate__wait_for_gate_change` ツールを呼び、`name`にTASK.mdの`GATE:<name>`
   （`plan`・`review`・`triage`のいずれか）を渡して、人間がゲートを解決するまでブロッキング
   待機する（Issue #35：ADR-0017の`inotifywait`単発ブロッキング呼び出しに相当する、状態
   デーモン経由のMCPツール呼び出し。ツール呼び出し自体が単発のブロッキング呼び出しなので、
   `while`ループもMonitorツールも不要）。
   - 待機中に人間が`docker exec -it ... tmux attach`（`masuda chat`）で接続し、対話の中で
     「進めていい」と伝えられた場合は、上記の待機を打ち切り、自分自身で
     `mcp__masuda-gate__resolve_gate_from_chat`ツール（`name`・`status`
     （`"approved"`または`"rejected"`）・`feedback`（対話の要約））を呼んでゲートを解決してよい。
   - ただし`triage`ゲート（ADR-0029）はこの限りではない。懸念の対象となっている
     エージェント自身が、chatでの会話を理由に自分自身でこのゲートを閉じることは
     絶対にしないこと。`resolve_gate_from_chat`は`name`に`"triage"`を渡すとサーバー側で
     エラーを返す実装になっており、この一点についてはIssue #13が指摘する権限境界の欠如が
     技術的に埋まっている（ただしオーケストレーターとClaudeセッションが同一ユーザー・同一
     コンテナで実行されているという、より広い意味での権限境界の欠如自体は残っている）。
     `masuda chat`は懸念の対話・事実確認に使ってよいが、最終判断は必ず人間がホスト側から
     `masuda triage dismiss/redo/halt`で独立に記録する。
   - `wait_for_gate_change`が返ったら（自分で`resolve_gate_from_chat`を呼んだ場合・別ターミナルの
     `masuda plan/review/triage approve|reject|dismiss|redo|halt`で解決された場合のどちらでも）、
     2へ戻ってLangGraphを起動する

## 終了条件

`TASK.md` の本文に `DONE` という文字列が含まれていること（`GATE:<name>`とは排他）

## ゲート条件

`TASK.md` の本文に `GATE:<name>` という文字列が含まれていること（`<name>`は`plan`・`review`・`triage`のいずれか）

## LangGraph 起動コマンド

```bash
MASUDA_STATE_DIR=/masuda-state /opt/masuda/venv/bin/python /opt/masuda/orchestrator/implement_review_graph.py
```

## rootやDockerを要する処理

このVMには**root権限もDockerデーモンも無い**。`sudo`は`systemctl poweroff`以外では通らず、
`dockerd`は起動できない。インストールや起動を試みても解決しないので、試さないこと。

対象リポジトリのテストがDockerを要求する場合（testcontainers等）、その実行は
**宣言・承認された特権コマンド**として、rootとDockerデーモンを持つ使い捨てのVMで行う
（ADR-0053）。

1. `/workspace/.masuda/settings.json`の`privilegedCommands`を読み、必要な処理が宣言されて
   いるか確認する（キーが名前）
2. 宣言されていれば`mcp__masuda-gate__run_privileged_command`ツールを呼び、`name`にその
   キーを渡す。実行が終わるまでブロッキングし、数分かかることがある
3. 戻り値は`exitCode`・`log`（末尾を切り詰めたもの）・`resultsDir`・`outputs`。全文のログと
   回収された成果物は`resultsDir`配下にあるので、必要なら**ファイルとして読む**

渡せるのは宣言の名前だけで、コマンド文字列は渡せない。実際に実行されるのは人間が承認した
宣言そのものであり、こちらから内容を変えることはできない。

**必要な処理が宣言されていない場合、または「承認されていない」というエラーが返った場合、
自分では解決できない。** 承認はホスト側で人間が`masuda privileged-command approve <name>`を
実行する操作であり、`/workspace`側のファイルを編集しても承認にはならない。何が必要かを
報告し、人間の対応を待つこと（ゲート待機と同じ扱い）。

なお、そのVMが見るのは`/workspace`の**スナップショット（コピー）**である。特権コマンドが
書いたファイルはこちらのworktreeには現れない——戻ってくるのは宣言された`outputs`だけで、
それも`resultsDir`配下に置かれる。コードの修正を特権コマンドにやらせても意味が無い。

## 注意

masuda自身の制御ファイル（TASK.md・`plan/`・`review_results/`等）は`/masuda-state`配下に
置かれる（対象リポジトリ＝`/workspace`の`git status`を汚さないため）。ゲートマーカーは
Issue #35以降ファイルではなく状態デーモンが保持しており、`mcp__masuda-gate__*`ツール経由で
しか触れない。コード自体の実装・レビューはこれまで通り`/workspace`に対して行う。
