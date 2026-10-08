# masuda（ホストでの使い方）

このリポジトリではmasudaを使う。masudaは、ワークフロー（どの役のエージェントに何をさせ、どこで人間に判断を仰ぐかを書いたYAML）に沿って、エージェントをこのPCの使い捨てVMで無人実行する。あなたはCLIでワークスペースを始め、様子を見て、人間の判断が要るところを人間に伝える。

## 前提

- `masuda-sandbox serve`と`masuda serve`が動いている。`masuda list`が接続エラーなら、`masuda-sandbox serve`→`masuda serve`の順にバックグラウンドで起動してよい（既に動いていれば起動しない。前提は`masuda doctor`で確かめられる）。起動したserveはこのセッションを閉じると止まりうる。止まるとワークスペースは`stopped`になり、`masuda resume <id>`で続けられる。判断待ちで長く置くなら、人間が別の端末で起動する方が確実
- 状況はログで分かる: `~/.local/share/masuda/logs/masuda-serve.log`、`~/.local/share/masuda-sandbox/logs/masuda-sandbox-serve.log`（こちらはJSON Lines）。どちらも起動時の`--log-file`・データディレクトリの指定で場所が変わる
- VMに渡るのはコミット済みの内容だけ。作業中の変更は渡らない
- 詳しくは`masuda doc`（使っているmasudaの版の文書。一覧から`masuda doc <page>`・`masuda doc <page>#<id>`で引く。ページの中の`troubleshooting.md#stalled`のようなリンクは、そのページのディレクトリからのパスに読み替える）
- masudaがこのリポジトリに書き込むのは、ワークフローが反映（publish）するときだけ。反映するのは人間が承認したコミットで、作業ツリーには触れない（チェックアウト中のブランチと同じ名前のときだけ、作業ツリーごとfast-forwardする）。実行中もこのリポジトリで別の作業をしてよい

## 流れ

1. 使うワークフローを選ぶ。`masuda workflow list`が同梱（`bundled`）とこのリポジトリの`.masuda/workflows/`（`repo`）のものと、受け取る入力を出す。中身は`masuda workflow show <workflow>`（図）か定義のYAMLを読む。同梱の主なもの:
   - `workflows/develop`: 指示（`instructions`）から計画・実装・レビューをして、承認されたコミットをブランチへ反映する
   - `workflows/fix`: 小さな修正向けの`develop`
   - `workflows/review`: コードを変えずにレビューし、結果を書き出す
2. 入力をファイルに書く。何をしてほしいか、範囲、してはいけないことを具体的に
3. `masuda run <workflow> --branch <ブランチ> --input <入力名>=@<ファイル>`。`<id> starting`を出してすぐ返る。`--branch`は反映するワークフローでは新しい名前、反映しないワークフローでは既にあるブランチも指せる
4. `masuda list`で状態を見る（終わったものも見るなら`--all`）。STATEの意味:
   - `starting`・`running`: 作業中。待つ
   - `waiting_gate`: `masuda gate show <id> <出現ID>`で中身を読み、要点を人間に伝えて判断を待つ
   - `waiting_question`: `masuda question list <id>`で問いを読む。文脈から答えられるなら`masuda question answer`で答えてよい（打つ前に人間に確かめられる）。判断がつかなければ人間に伝えて答えを待つ
   - `suspended`: 中断した。POSITIONに理由が出る。人間が原因を直せば`masuda resume <id>`で同じところから続けられるので、理由を人間に伝える
   - `blocked`: 行き止まりで、再開できない。POSITIONに理由が出る
   - `done`: 終わった。POSITIONに結果が出る
5. 結果を見る。反映するワークフローなら、承認されたコミットがこのリポジトリの`--branch`のブランチに入っている。ワークフローが書き出すデータ（計画、レポート等）と実行ログは`~/.local/share/masuda/workspaces/<id>/exports/`（`masuda serve`の`--data-dir`を変えていればその下）に残る
6. 止めるのは`masuda stop <id>`、続けるのは`masuda resume <id>`

## 人間の判断を代わりにしない

次のコマンドは人間が判断することを前提にしている。打たずに、人間に打ってもらう（`masuda init`がClaude Codeの権限でも止めている）。

- `masuda gate approve|reject|comment|dismiss|halt|redo`
- `masuda egress approve|reject`
- `masuda secret set|approve|reject`
- `masuda env import`（秘密の値を扱う）
- `masuda privileged-command approve|run`（`run`は特権VMをrootで動かす）
- `masuda remove`（ワークスペースの記録を消す）

## 使わないもの

- `masuda watch`: 終わらずに流れ続けるので、そのままは打たない。`list`より詳しく知りたいときは、ワークスペースの記録（`~/.local/share/masuda/workspaces/<id>/`）を読む
  - `records/execution-log.jsonl`: ワークフローの進み（1行1イベントのJSON）
  - `workspace.json`: 状態・結果・理由
  - `data/<出現ID>/<データ名>`: 各ノードの出力
  - `records/hooks.jsonl`: VMの中のClaude Codeのフック
  - `records/image-build.log`: イメージのビルド
  - VMの通信（許可・拒否）は記録に残らず`watch`にしか出ない。それが要るときは`masuda watch <id>`をバックグラウンドで動かして出力を読む
- `masuda chat`: VMの画面へ人間がアタッチするためのもの

各コマンドの詳細は`masuda <command> -h`。
