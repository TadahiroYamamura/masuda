# 秘密・egress・特権コマンド

VMの中のエージェントは、初めは外のどこにも通信できず（Claude APIを除く）、秘密も持たず、rootでもない。必要な分だけ広げるには、どれも同じ形を取る。

1. `.masuda/settings.json`に**宣言**する（コミットされ、チームで共有する）
2. `masuda ... approve`で**承認**する（あなたの`.masuda/settings.local.json`に残る。コミットしない）
3. 通信先と秘密は、さらにワークフローの**ノードで選ぶ**。そのノードの間だけ効く

宣言だけでも、承認だけでも効かない。チームメイトが宣言を足したコミットを取り込んでも、あなたが承認するまであなたの環境では何も起きない。

## egress（外への通信） {#egress}

### 何が通るか

- 許可したホストへのHTTP/1.xとHTTPS。HTTPSはmasuda-sandboxが途中で復号して検査する（VMの中ではmasuda-sandboxのCAを信頼している）
- それ以外は通らない: HTTP/2だけを話すクライアント、HTTPのCONNECT、ssh、データベース等への生のTCP、DNS以外のUDP。名前解決はできても、許可していないホストへの接続は断られる
- Claude API（`api.anthropic.com`）は常に通る

### 手順

```json title=".masuda/settings.json"
{
  "egress": ["api.anthropic.com", "pypi.org", "*.pythonhosted.org"]
}
```

```sh
masuda egress list
# HOST                APPROVED
# api.anthropic.com   yes
# pypi.org            no
# *.pythonhosted.org  no

masuda egress approve pypi.org
masuda egress approve '*.pythonhosted.org'
```

ワークフローの、通信が要るノードに`egress:`を書く。

```yaml title=".masuda/workflows/develop.yaml（抜粋）"
  rework-test:
    type: exec
    command: ["/masuda/checks/test"]
    egress: [pypi.org, "*.pythonhosted.org"]
    next:
      done: rework-commit
      failed: rework
```

- ノードの`egress:`に書けるのは、宣言かつ承認済みのホストだけ。範囲外のホストを書いたノードに入ると、その実行は止まる
- 書かなかったノードでは、Claude API以外に届かない。**同梱のワークフローはどのノードにも`egress:`を書いていない**ので、同梱のまま使う限り、承認したホストへも届かない。使うにはワークフローを上書きする（[ワークフロー](workflows.md#override)）
- 依存パッケージのように前もって取れるものは、通信を開けるより、イメージのビルド（Dockerfile）で取っておく方が簡単で安全

断られた通信は`masuda watch`に`http denied <host>`と出る。

### 承認を取り消す

```sh
masuda egress reject pypi.org
```

取り消しは、次の`masuda run`・`masuda resume`から効く。

## 秘密

### placeholder（既定）

本物の値をVMに入れない方式。

```json title=".masuda/settings.json"
{
  "egress": ["api.linear.app"],
  "secrets": [
    { "name": "LINEAR_API_KEY", "hosts": ["api.linear.app"] }
  ]
}
```

```sh
masuda secret set LINEAR_API_KEY      # 値を聞いてくる。パイプでも渡せる
masuda secret list
# NAME            MODE         HOSTS           VALUE  APPROVED
# LINEAR_API_KEY  placeholder  api.linear.app  set    -
# Claude token: set
```

- VMの中では、環境変数`LINEAR_API_KEY`にプレースホルダ（本物とは違う、ランダムな値）が入る
- VMから`hosts`のホストへの通信が、ヘッダー（`in`に`"body"`を足せば本文も）にプレースホルダを含んでいると、masuda-sandboxが本物の値に置き換えて送る
- 置き換えが働くのは、ワークフローのノードの`secrets:`に名前を書いたノードの間だけ。通信先もそのノードの`egress:`で開ける

```yaml
  fetch-task:
    type: exec
    command: ["/usr/bin/python3", "/workspace/scripts/fetch-task.py"]
    outputs: [task]
    egress: [api.linear.app]
    secrets: [LINEAR_API_KEY]
    next: {done: investigate, failed: end:fetch_failed}
```

`placeholder`の秘密は承認が要らない（`approve`するとエラーになる）。本物の値が宛先以外へ出ていく経路が無いため。

### plaintext {#plaintext}

本物の値をそのままVMの環境変数に置く方式。値をローカルで使う（署名の計算に使う、SDKが値の形を検査する等）ため、プレースホルダでは動かないときの例外。

```json
{ "name": "LEGACY_SDK_KEY", "mode": "plaintext" }
```

```sh
masuda secret set LEGACY_SDK_KEY
masuda secret approve LEGACY_SDK_KEY
masuda secret list
# NAME            MODE       HOSTS  VALUE  APPROVED
# LEGACY_SDK_KEY  PLAINTEXT  -      set    yes
```

- 承認が無ければ`masuda run`が断る
- 値は実行の間ずっと、VMの中のすべてのプロセスから読める。VMの中のエージェントが汚染されれば、許可しているホストへ持ち出されうる。使うのは本当に必要なときだけにする
- 承認の取り消しは`masuda secret reject <NAME>`

### 値の置き場所

- 値はホストの`~/.local/share/masuda/secrets/<リポジトリのパスのハッシュ>/<NAME>`（パーミッション0600）に置く。リポジトリにも設定ファイルにも書かない。チームメイトはそれぞれ自分の値を登録する
- 置き場所はリポジトリの**絶対パス**で決まる。リポジトリを移動・複製したら、その場所でもう一度`masuda secret set`する
- 値を読み戻すコマンドは無い。変えるときは`set`し直す
- Claudeのトークンも同じ仕組みの1つで、名前は`CLAUDE_CODE_OAUTH_TOKEN`、送り先は`api.anthropic.com`。どのノードでも有効。ただし置き場所は既定でユーザー単位（`~/.local/share/masuda/secrets/_user/`）で、`--repo`無しの`masuda secret set CLAUDE_CODE_OAUTH_TOKEN`が置く。リポジトリごとに登録したもの（`--repo`付き）があればそちらが優先される。複数のアカウントを使い分けるときは、`settings.local.json`の`claudeToken`で別の名前を選ぶ（[設定ファイル](settings.md#settings-local)）

### `.env`を生成する

手元の`.env`はVMへ写さない。必要な変数を`envFiles`で宣言し、VMの作業ツリーに毎回生成する。

```json title=".masuda/settings.json"
{
  "secrets": [{ "name": "LINEAR_API_KEY", "hosts": ["api.linear.app"] }],
  "envFiles": [
    { "path": ".env", "vars": ["LINEAR_API_KEY", "APP_ENV"] }
  ]
}
```

```json title=".masuda/settings.local.json"
{
  "vars": { "APP_ENV": "development" }
}
```

VMの`/workspace/.env`は次のようになる。

```sh
LINEAR_API_KEY="<プレースホルダ>"
APP_ENV="development"
```

- 秘密として宣言した名前はプレースホルダ（`plaintext`なら本物の値）、それ以外は`settings.local.json`の`vars`の値。どちらでもない変数があれば`masuda run`が断る
- 生成したファイルはVMのgitから除外され、コミットされない

## 特権コマンド {#privileged}

テストがrootやDockerを要するとき、そのコマンドだけを**別の使い捨てVM（rootで動く）**で実行させる仕組み。エージェントが動くVMはrootにしない。

!!! note "実機での確認"
    特権VMの起動、作業ツリーの受け渡し、rootでのコマンドの実行（PostgreSQLを起動してテストを流す）、結果の回収は、実際のVMで確かめてある。特権VMの中でdockerdを動かす使い方（testcontainers等）は、まだ実機で確かめていない。

### 宣言と承認

```json title=".masuda/settings.json"
{
  "privilegedCommands": {
    "integration-test": {
      "command": "./scripts/integration-test.sh",
      "image": "privileged",
      "inputs": ["testdata/large/**"],
      "outputs": ["coverage.out", "reports/**"],
      "timeoutSeconds": 1800
    }
  }
}
```

```sh
masuda privileged-command list
# NAME              IMAGE       APPROVED  COMMAND
# integration-test  privileged  no        ./scripts/integration-test.sh

masuda privileged-command approve integration-test
```

- `image`は特権VMのイメージのエントリ（`.masuda/images/privileged/Dockerfile`等）。エージェントのVMと同じエントリでもよい
- 承認は、その時点の宣言の内容（`command`・`image`・`inputs`・`outputs`・`timeoutSeconds`）に結びつく。宣言が1文字でも変わると、`list`のAPPROVEDが`stale`になり、承認し直すまで使えない。コミットで宣言をすり替えられないようにするため
- 承認はイメージの中身（`.masuda/images/<image>/`のDockerfileや、イメージに入れたスクリプト）を含まない。特権コマンドは`/workspace`のコード（エージェントが書き換えられる）も動かすので、イメージだけを縛っても守れるものは増えないため。承認が守るのは「どのイメージのVMで、どの通信先を開けて、何を動かすか」で、`.masuda/images/`の変更はレビューで見る
- 特権VMが通信できるのは、宣言かつ承認済みのegressのホスト

### 呼ばれ方

- エージェントは`run_privileged_command(<名前>)`というツールで、名前を指定して呼ぶことしかできない。コマンドの文字列を渡す口は無い
- 同梱の実装の役（`implementer`）は、VMのcloneの`.masuda/settings.json`を読んで宣言を知る。使わせるなら`.masuda/settings.json`をコミットしておく。宣言が無い・承認されていないときは、あなたに承認を求めるfeedbackを書いて`stuck`で終える
- 呼ばれるたびに、宣言は実行開始時の写しから、承認は作業ツリーの`settings.local.json`から読む。実行中にエージェントがVMの中の宣言を書き換えても、承認と食い違って断られるだけ
- 同じワークスペースでは1つずつ動く

### 何が渡り、何が返るか

1. 呼ばれた時点のVMの作業ツリーのスナップショット（gitで追跡しているもの）を特権VMへ渡し、`/workspace`に展開する
2. gitで運ばれないファイル（gitignoreされた生成物やデータ）のうち、`inputs`のglobに当たるものをエージェントのVMから特権VMへ写す。gitignoreの中身が黙って境界を決めることは無い
3. rootで`command`を`/workspace`から動かす（`timeoutSeconds`で打ち切り、既定1時間）
4. 終了コード・ログの末尾・`outputs`に当たるファイルを回収し、写しをエージェントのVMの`/masuda/privileged/<run-id>/`（`exit-code`・`log`・`outputs/`）へ置く。特権VMは壊す

特権VMはClaudeのトークンも、masudaへの経路も持たない。記録はホストの`workspaces/<id>/records/privileged/<run-id>/`に残る（`result.json`に名前・宣言のハッシュ・時刻・終了コード・回収したファイル・回収できなかったものの説明）。
