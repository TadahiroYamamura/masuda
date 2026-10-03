# はじめての1周

手元のリポジトリで、同梱の`develop`ワークフロー（調査→計画→計画の承認→実装→レビュー→レビューの承認→反映）を1回通す。

所要の目安は10〜30分。イメージの初回ビルドに数分、エージェントの作業に10〜20分かかる（小さなPythonのリポジトリに関数を1つ足す課題で、約18分だった）。人間の出番は計画の承認とレビューの承認の2回。

## 始める前に

- [導入](install.md)を終え、`masuda-sandbox serve`と`masuda serve`が起動している
- 対象リポジトリはgitの作業ツリーで、コミットが1つ以上ある。エージェントが見るのは**コミット済みの内容**（作業中の変更はVMへ渡らない）
- 対象リポジトリで`git config user.name`・`user.email`が引ける。masudaが作るコミットの作者になる

以下、コマンドはすべて対象リポジトリのトップで打つ。

## 1. `.masuda/`の雛形を置く

```sh
masuda init
```

```text
created .masuda/images/default/Dockerfile
created .masuda/settings.json
created .masuda/reviews/comment-history-leakage.md
created .masuda/reviews/dead-code.md
created .masuda/reviews/error-handling-gaps.md
created .masuda/reviews/heavy-loop-processing.md
created .masuda/reviews/implicit-type-conversion.md
created .masuda/reviews/injection-vulnerability.md
created .masuda/reviews/input-validation-gaps.md
created .masuda/reviews/logging-secrets.md
created .masuda/reviews/missing-tests-guard-clauses.md
created .masuda/reviews/missing-tests-new-code.md
created .masuda/reviews/n-plus-one-query.md
created .masuda/reviews/nil-check-gaps.md
created .masuda/reviews/resource-leak.md
created .masuda/reviews/secret-hardcode.md
created .gitignore (.masuda/settings.local.json)
note: the Claude API (api.anthropic.com) is always reachable; list other hosts the VM needs in egress of .masuda/settings.json
```

`masuda init`は`masuda serve`無しで動く。既にあるファイルは上書きせず、足りないものだけ足す。`.gitignore`には利用者ごとの承認を書く`.masuda/settings.local.json`を足す（`.masuda/`ごと無視している行があれば足さない）。

`.masuda/`はコミットしてもしなくてもよい。masudaは実行を始めるときに作業ツリーの`.masuda/`を読む。チームで共有するならコミットする。

## 2. イメージにプロジェクトの道具を足す

`.masuda/images/default/Dockerfile`がVMの中身になる。雛形にはmasudaが要るもの（Claude Code・tmux・git等）だけが入っているので、対象リポジトリのビルドとテストに要るものを足す。

Pythonの例（`apt-get install`の行に`python3`を足す）:

```dockerfile
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl git tmux openssh-server python3 \
 && rm -rf /var/lib/apt/lists/*
```

実行中のVMから外へ出られるのは許可したホストだけなので、依存パッケージはここで入れておく。書き方の注意（キャッシュの置き場所、環境変数）は[トラブルシューティング](troubleshooting.md#dockerfile-env)にもある。

## 3. テストのコマンドを書く

`develop`は実装の各ステップの後に`/masuda/checks/test`を走らせ、通らなければ実装をやり直させる。雛形の`checks.test`は「まだ設定していない」と言って必ず失敗するので、`.masuda/settings.json`で本物のテストのコマンドに書き換える。

```json
{
  "image": "default",
  "egress": [],
  "secrets": [],
  "envFiles": [],
  "checks": {
    "test": "python3 -m unittest discover -s tests -v"
  },
  "claudeSettings": {}
}
```

コマンドはVMの中の`/workspace`（対象リポジトリのclone）で、シェルの1行として動く。

## 4. トークンを確かめる

[導入](install.md#claude-token)でClaudeのトークンを登録していれば、ここですることは無い。まだなら導入の手順で登録する（`masuda secret set CLAUDE_CODE_OAUTH_TOKEN`）。

- Claude APIへの経路は宣言や承認に関係なく常に開いているので、雛形の`egress`は空。VMから他のホストへ出る必要があれば、`settings.json`の`egress`に宣言して`masuda egress approve <host>`で承認する（[秘密・egress・特権コマンド](secrets-and-egress.md)）
- トークンはユーザー単位に登録されていて、どのリポジトリでも使われる。このリポジトリだけ別のトークンにしたいときだけ、ここで`--repo .`を付けて登録する

```sh
masuda secret list
# NAME  MODE  HOSTS  VALUE  APPROVED
# Claude token: set
```

## 5. イメージをビルドする

```sh
masuda image build default
```

ビルドのログが流れ、最後にイメージのIDが出る。初回は数分かかる。`masuda run`も起動のたびにビルドする（変わっていなければすぐ終わる）が、Dockerfileの誤りは先にここで見つけておく方が早い。

## 6. 課題を書いて実行する

課題（エージェントへの指示書）をファイルに書く。何を作るか、どこを変えてよいか、何を変えてはいけないかを具体的に書く。

```markdown title="task.md"
`shapes/` に `shapes/triangle.py` を追加してください。

- `area(a, b, c)`: 3辺の長さから面積を返す（ヘロンの公式）
- `perimeter(a, b, c)`: 周の長さを返す
- 負の辺と、三角形が成り立たない辺には `ValueError` を投げる
- `tests/test_triangle.py` に unittest のテストを足す

標準ライブラリだけを使うこと。他のファイルは変更しないこと。
```

```sh
masuda run workflows/develop --branch feat/triangle --input instructions=@task.md
# 55a7dbe1b35f starting
```

- `--branch`は作るブランチの名前。対象リポジトリに既にある名前は使えない
- 分岐元は、今チェックアウトしているブランチ。変えるなら`--base main`のように渡す
- `--input instructions=@task.md`の`@`は「ファイルの中身」。`instructions=文字列`と直接書いてもよい
- 出てきた`55a7dbe1b35f`がワークスペースのID。以下`<id>`と書く
- typoの修正のような小さな修正なら、`workflows/develop`の代わりに`masuda run workflows/fix`を使える。調査と計画を1つのセッションで済ませ、横断チェックとレポートを省く（[ワークフロー](workflows.md#fix)）。以下の手順は同じで、9節のレポートが無い

`masuda run`はすぐ返る。VMの起動は裏で進む。設定に足りないもの（トークン未登録、テストのコマンド未宣言等）があれば、この時点でまとめてエラーになり、何も始まらない。

## 7. 様子を見る

```sh
masuda watch <id>
```

状態の変化、ワークフローの進み、VMからの通信が1行ずつ流れる。Ctrl-Cで見るのをやめても実行は続く。

```text
3 01:38:08 55a7dbe1b35f status running working
27 01:38:12 55a7dbe1b35f engine enter occ=0000001 workflows/develop/investigate
28 01:38:12 55a7dbe1b35f status running working agent investigator (occ 0000001)
31 01:38:13 55a7dbe1b35f http POST api.anthropic.com/v1/messages ...
32 01:38:14 55a7dbe1b35f http POST api.anthropic.com/v1/messages 200 869ms
33 01:38:14 55a7dbe1b35f hook PostToolUse Read
...
```

一覧で見るなら`masuda list`。

```text
ID            BRANCH         STATE         ACTIVITY              POSITION                 OPEN
55a7dbe1b35f  feat/triangle  waiting_gate  waiting_gate 10s ago  gate plan (occ 0000003)  gate:plan
```

各列の意味は[運用](operations.md#list)。VMの中のClaude Codeの画面を直接見たければ`masuda chat <id>`（`C-b d`で抜ける。抜けても実行は続く）。

## 8. 計画を承認する（plan gate）

調査と計画が終わると、計画の承認待ち（`waiting_gate`、OPENに`gate:plan`）で止まる。

計画を書いた役とは別の役が計画に問いを立て（「退化三角形を不正として扱うか」など）、計画を直す役がそれに答えてから、このゲートが開く。答えられない問い（`open`）が残ったときは、ゲートより前に質問（`waiting_question`、OPENに`question:<出現ID>`）として届くので、`masuda question list`で読んで`masuda question answer <id> <出現ID> SPEC-1=<答え> ...`で問いのidごとに答える（[途中で止まったら](#stuck)）。

```sh
masuda gate list
# WORKSPACE     OCCURRENCE  GATE  TARGET  OPENED
# 55a7dbe1b35f  0000003     plan  plan    10-03 01:38:54

masuda gate show <id> 0000003
```

`gate show`は計画を節に分けて出し、最後に打てるコマンドを添える。`goal`は計画が達成すること、`summary`はアプローチとテストの実行の仕方、`steps`は機能単位のステップごとに内容・そのステップで通すテスト・変更するファイル、`alternatives`は検討したが採らなかった案、`risks`は懸念、`expected byproducts`はビルド・テストが生む副産物として計画外の変更の検出から外すパターン。`checks`は計画に立てられた問いと、計画を直す役の答え（`[addressed]`は計画で扱った、`[out_of_scope]`は範囲外、`[open]`は判断できず人間に聞いたもの）。出力例（ヘッダーの`gate:`〜`opened:`の行は省略）:

```text
goal: 三角形の面積と周長を求めるモジュールを追加する

summary:
  shapes/triangle.py を新規追加し、ヘルパー _check で辺の検証を共通化する。テストは PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tests -v で実行し、追跡済みの pyc を汚さない

steps:
  1. 三角形モジュールの実装
     _check(a,b,c) で辺<=0 と三角不等式違反を ValueError にする。area はヘロンの公式、perimeter は和。標準ライブラリ math のみ
     tests:
       - 3,4,5 で area が 6、perimeter が 12
       - 負の辺・0 の辺で両関数が ValueError
       - 不成立 (1,2,10) と退化 (1,2,3) で ValueError
     files: shapes/triangle.py, tests/test_triangle.py

checks (questions raised about the plan, with the planner's answers):
  SPEC-1 [addressed] 退化三角形（1,2,3）を不正として扱うか
      ステップ1の _check で a+b>c の厳密不等式を要求する
  REGRESSION-1 [out_of_scope] 既存の shapes/circle.py・rectangle.py の呼び出し元に影響は無いか
      新規モジュールの追加だけで既存ファイルは変更しないため

alternatives (considered, not taken):
  - 退化三角形を許容する (<=): 面積 0 が無意味

risks:
  - 浮動小数の境界誤差は未対応

expected byproducts: **/__pycache__/**, **/*.pyc

approve: masuda gate approve 55a7dbe1b35f 0000003 --hash <target_hash> [--comment <text>]
reject:  masuda gate reject 55a7dbe1b35f 0000003 [--comment <text>]
```

計画は実行のたびにエージェントが書くので、ステップの分け方や文面は毎回変わる。

```sh
masuda gate approve <id> 0000003 --hash <gate showが出したtarget_hash>
```

- `--hash`を渡すと、あなたが読んだ内容と同じものだけを承認する。省くと、その時点で開いているゲートの内容を承認する
- 直してほしければ`masuda gate reject <id> 0000003 --comment "ステップ2でテストも書くこと"`。計画がコメントを踏まえて書き直され、もう一度このゲートが開く

承認すると、計画のステップごとに「実装→テスト→コミット」が進む。テストが通らなければ実装をやり直す。レビューは全ステップが終わった後に1回だけ行う（9節）。計画に無いファイルが変わっていれば、コミットの前に`deviation`ゲートでも止まる（gitで追跡している`__pycache__`等が典型。[トラブルシューティング](troubleshooting.md#deviation)）。

## 9. レビュー結果を承認する（review gate）

全ステップが終わると、全観点でのレビュー（とその指摘の確かめ）・横断的な問題の確認・指摘の一括修正と再確認・レポートの作成が走り、レビューの承認待ち（`gate:review`）で止まる。

```sh
masuda gate show <id> <出現ID>
```

`gate show`は分岐元から反映されるコミット（`commit:`）までの差分を出す。コミットされずに作業ツリーに残ったファイルがあれば、差分の後に「publishされない変更（未コミット）」として名前だけが並ぶ（これらは反映されない）。レビューの指摘はstagingのそのコミットへのコメントとしても残る。レビューのレポート（残っている指摘、自動で直した指摘）はホストのファイルで読める。

```sh
cat ~/.local/share/masuda/workspaces/<id>/data/*/report
```

承認すると、そのコミットが手元のリポジトリの`feat/triangle`ブランチへ反映（publish）され、VMは片付けられる。

```sh
masuda gate approve <id> <出現ID> --hash <target_hash>
```

却下（`reject --comment ...`）すると、実装した役の続きがコメントを踏まえて手直しし、テストとコミットの後にもう一度このゲートが開く。レビューの段はやり直さないので、指摘が直ったかを差分で確かめる。差分の特定の行を直してほしいときは、却下の前に`gate comment`で行コメントを付けておく。付けたコメントは`gate show`の差分の後に並び、却下したときに`--comment`の本文とともに手直しのエージェントへ届く（承認したときは届かない）。

```sh
masuda gate comment <id> <出現ID> shapes/triangle.py:12 "負の長さも弾く"
masuda gate reject <id> <出現ID> --comment "入力の検証を足す"
# エージェントへ届く差し戻し:
#   入力の検証を足す
#
#   ## 差分への行コメント
#   - shapes/triangle.py:12: 負の長さも弾く
```

## 10. 結果を確かめる

```sh
masuda list --all
# ID            BRANCH         STATE  ACTIVITY      POSITION      OPEN
# 55a7dbe1b35f  feat/triangle  done   idle 59s ago  outcome done  -

git log --oneline main..feat/triangle
git switch feat/triangle
python3 -m unittest discover -s tests -v
```

反映されるのはブランチだけで、今チェックアウトしているブランチや作業ツリーには触れない（チェックアウト中のブランチと同じ名前のときだけ、作業ツリーごとfast-forwardする）。

レビューのレポートと実行ログ、エージェントの会話ログは`~/.local/share/masuda/workspaces/<id>/exports/`に残る（[運用](operations.md#exports)）。ワークスペースが要らなくなったら`masuda remove <id>`（exportsだけは残る）。

## 途中で止まったら {#stuck}

- `masuda list`のSTATEが`blocked`: POSITIONに理由が出る。[トラブルシューティング](troubleshooting.md)
- ACTIVITYが`stalled`や`waiting_input`のまま: [トラブルシューティング](troubleshooting.md#stalled)
- 計画の承認より前に`done`で終わった: 計画を立てる役が「この依頼はこのリポジトリで扱うべきものではない」と判断した（`outcome out_of_scope`）。課題の書き方を見直す
- `develop`で、計画の承認より前に`waiting_question`（OPENに`question:<出現ID>`）になった: 計画に立てられた問いのうち、計画を直す役が判断できなかったものを聞いている。`masuda question list <id>`で問い（`SPEC-1`等のidと、問いと判断できなかった理由）を読み、`masuda question answer <id> <出現ID> SPEC-1=<答え> REGRESSION-2=<答え>`で**すべての問いに**答える（`question list`の最後の行に、そのまま埋めればよい形が出る）。答えを踏まえて計画が直され、plan gateが開く
- `workflows/fix`で、計画の承認より前に`outcome needs_human`で終わった: 指示が曖昧で計画を立てられなかった。POSITIONに役の疑問が出るので、答える形で課題を書き直す
