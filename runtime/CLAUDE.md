# 作業ループ仕様

## ループルール

1. `mcp__masuda-gate__next_task` ツールを呼び、返ってきた `task` を今回の作業指示とする
   （引数は無い。何を次にやるかはワークスペースの状態から決まる）
2. `task` の指示に従って作業を行う
3. 作業完了後、**終了条件** と **ゲート条件** を確認する
   - 終了条件を満たしていれば → 以下のコマンドを実行してセッションを終了する（コミット・質問・確認は不要）
     ```bash
     tmux kill-session -t $(tmux display-message -p '#S')
     ```
   - ゲート条件を満たしていれば → 4へ
   - どちらも満たしていなければ → 1 へ戻る
4. `mcp__masuda-gate__wait_for_gate_resolution` ツールを呼び、`name`に`task`の`GATE:<name>`
   （`plan`・`review`・`triage`のいずれか）を渡して、人間がゲートを解決するまでブロッキング
   待機する（Issue #35：ADR-0017の`inotifywait`単発ブロッキング呼び出しに相当する、状態
   デーモン経由のMCPツール呼び出し。ツール呼び出し自体が単発のブロッキング呼び出しなので、
   `while`ループもMonitorツールも不要）。
   - 待機中に人間が`masuda chat <workspace-id>`で接続し、対話の中で
     「進めていい」と伝えられた場合、**`plan`ゲートに限り**、上記の待機を打ち切り、自分自身で
     `mcp__masuda-gate__resolve_gate_from_chat`ツール（`name`・`status`
     （`"approved"`または`"rejected"`）・`feedback`（対話の要約））を呼んで解決してよい。
     planの承認はループが先へ進むだけで、このワークスペースの外に影響しないためである。
   - **`review`ゲートはchatでは解決できない**（ADR-0060）。G2の承認は、作業ブランチを
     人間の実リポジトリへfast-forward反映し、cloneとワークスペースを削除するところまで含む。
     これはホスト側でしか行えず、人間が自分の手で起動すべき操作でもある。chatで
     「承認する」と言われたら、**`masuda review approve <workspace-id>`をホストで実行する
     必要がある**と伝えて、待機を続けること。自分で回避しようとしないこと。
   - **`triage`ゲートも同様にchatでは解決できない**（ADR-0029）。懸念の対象となっている
     エージェント自身がこのゲートを閉じてはならない。`masuda chat`は懸念の対話・事実確認に
     使ってよいが、最終判断は必ず人間がホスト側から`masuda triage dismiss/redo/halt`で
     独立に記録する。
   - review・triageのどちらも`resolve_gate_from_chat`がサーバー側でエラーを返す実装に
     なっており、規約ではなく技術的な境界になっている（Issue #13・#24）。
   - `wait_for_gate_resolution`が返ったら（自分で`resolve_gate_from_chat`を呼んだ場合・別ターミナルの
     `masuda plan/review/triage approve|reject|dismiss|redo|halt`で解決された場合のどちらでも）、
     1へ戻って`next_task`を呼び直す

判断の材料にするのは**`next_task`の戻り値**であって、`/masuda-state/TASK.md`ではない。
同じ内容はそのファイルにも書かれるが、それは人間や再開したセッションのための記録であり、
ホストが書いた最新版がこのVMから見えるまで最大1秒ほど遅れる。自分で開いて読み直さないこと。

## 終了条件

`next_task`が返した`task`の本文に `DONE` という文字列が含まれていること（`GATE:<name>`とは排他）

## ゲート条件

`next_task`が返した`task`の本文に `GATE:<name>` という文字列が含まれていること（`<name>`は`plan`・`review`・`triage`のいずれか）

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

### 特権コマンドが見るのは`/workspace`のコピー

特権コマンドは、実行を要求した時点の`/workspace`を**まるごと複製した別のディレクトリ**の中で
動く。自分が編集している`/workspace`そのものではない。したがって:

- 特権コマンドがファイルを変更・追加しても、自分の`/workspace`には一切反映されない
  （その複製はVMごと消える）
- 手元に戻ってくるのは、宣言の`outputs`に書かれたパスだけ。それも`/workspace`へ書き戻される
  のではなく、`resultsDir`配下にコピーとして置かれる

**コードの修正そのものを特権コマンドに任せることはできない。** 例えばフォーマッタをかけさせる、
失敗したテストをその中で直させる、といった使い方は成果が消えるだけで無意味である。修正は自分で
`/workspace`に対して行い、特権コマンドは「その結果を検証する」用途に使うこと。

## 注意

masuda自身の制御ファイル（TASK.md・`plan/`・`review_results/`等）は`/masuda-state`配下に
置かれる（対象リポジトリ＝`/workspace`の`git status`を汚さないため）。指示の中に出てくる
`/masuda-state/...`は読み書きしてよい。コード自体の実装・レビューはこれまで通り`/workspace`
に対して行う。

ループを進める状態機械そのもの（次に何をするかの判定、ステップごとのcommit、予算）は、この
VMではなく**ホスト側**で動いている。`next_task`はそれを1回分進めて結果を返すツールである。
ゲートマーカーも状態デーモンが保持しており、`mcp__masuda-gate__*`ツール経由でしか触れない。
このVMから直接オーケストレーターを起動する方法は無く、その必要も無い。
