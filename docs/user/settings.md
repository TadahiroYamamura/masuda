# 設定ファイル

対象リポジトリの`.masuda/`に2つ置く（ほかに任意の[`pitfalls.jsonl`](#pitfalls)）。リポジトリに依らない`masuda serve`全体の設定は、別に[`config.json`](#serve-config)に置く。

| ファイル | 誰のものか | コミット | 中身 |
|---|---|---|---|
| `settings.json` | チーム | する | **宣言**。使うイメージ、通信先、秘密の名前と送り先、生成する`.env`、特権コマンド、チェック、Claude Codeの設定、役ごとのモデル |
| `settings.local.json` | あなた | しない（`masuda init`が`.gitignore`に足す） | **承認と手元の値**。どの宣言を承認したか、`.env`の公開値、無活動のしきい値など |

このほか任意で、プロジェクト固有の落とし穴を[`pitfalls.jsonl`](#pitfalls)に書ける（チームのもの。コミットする）。VMの中のClaude Codeに渡すルールやスキルは[`claude/`と`claude.local/`](#claude-dir)に置ける。

通信先・平文の秘密・特権コマンドは、`settings.json`で宣言され、かつ`settings.local.json`で承認されたときだけ効く（[概念](concepts.md#declare-approve)）。秘密の値はどちらにも書かない（`masuda secret set`でホストの秘密ストアへ）。

## 共通の約束

- どちらもJSON。どの項目も省略できる。ファイルが無ければすべて既定値
- **知らないキーがあると読み込みを断る**。綴りを間違えた設定が黙って無視されないように。例: `json: unknown field "egressAllowlist"`
- 読むタイミング: `masuda run`は作業ツリーの`.masuda/`をワークスペースへ写し、`settings.json`はその写しを実行の終わりまで使う。`settings.local.json`は`run`と`resume`のたびに作業ツリーから読み直す（承認の取り消しや値の入れ替えを、再開で反映するため）。`egress`・`secret`等のコマンドはその場の作業ツリーのものを読み書きする
- `settings.local.json`は`masuda egress approve`等のコマンドが書き換える（パーミッション0600）。手で編集してもよい

## settings.json {#settings-json}

```json
{
  "image": "default",
  "images": {
    "default": { "diskMiB": 8192 }
  },
  "egress": ["api.anthropic.com", "pypi.org", "*.pythonhosted.org"],
  "secrets": [
    { "name": "LINEAR_API_KEY", "hosts": ["api.linear.app"] },
    { "name": "NPM_TOKEN", "hosts": ["registry.npmjs.org"], "in": ["header", "body"] },
    { "name": "LEGACY_SDK_KEY", "mode": "plaintext" }
  ],
  "envFiles": [
    { "path": ".env", "vars": ["LINEAR_API_KEY", "APP_ENV"] }
  ],
  "privilegedCommands": {
    "integration-test": {
      "description": "PostgreSQLを起動して結合テストを流す",
      "command": "./scripts/integration-test.sh",
      "image": "privileged",
      "inputs": ["testdata/fixtures/**"],
      "outputs": ["coverage.out"],
      "timeoutSeconds": 1800
    }
  },
  "checks": {
    "test": "GOCACHE=/tmp/go-cache go test ./...",
    "lint": "go vet ./..."
  },
  "claudeSettings": {
    "model": "sonnet",
    "env": { "GOCACHE": "/tmp/go-cache" }
  },
  "agents": {
    "reviewer": { "model": "opus" },
    "implementer": { "effort": "high" }
  },
  "publish": { "remote": "origin" }
}
```

### image

| 型 | 既定 |
|---|---|
| 文字列 | `"default"` |

VMのイメージのエントリ。`.masuda/images/<image>/Dockerfile`からビルドする。名前は英数字で始まり、英数字・`.`・`_`・`-`だけ。`masuda run --image`で実行ごとに変えられる。

VMの起動のたびに、次のディレクトリは空のtmpfsになる（VMの基盤であるGondolinの起動処理による）。Dockerfileでここに置いたものはVMの中では見えないので、別の場所に置くか、起動のたびに作る。エージェントのVMも特権コマンドのVMも同じ。

| ディレクトリ | よくあるつまずき |
|---|---|
| `/run`（`/var/run`） | デーモンのソケット・ロックファイルの置き場所（PostgreSQLの`/var/run/postgresql`等）が無い。起動するスクリプトで`mkdir -p`するか、データの隣に置く |
| `/root` | rootで入れた道具の設定やキャッシュ（`/root/go`、`/root/.npm`等）が消える。特権コマンドはrootで動くので効いてくる。`/usr/local`や`/opt`に置く |
| `/tmp`・`/var/tmp`・`/var/cache`・`/var/log` | イメージに置いたものは消える（`/var/cache/apt`は空になる）。大きくなるキャッシュの置き場所としては向いている（メモリを使う） |

`/home`・`/etc`・`/usr`・`/opt`・`/var/lib`（`/var/lib/docker`等）は、イメージの中身がそのまま残る。

VMのClaude Codeの版は、Dockerfileのinstall行で固定されている。

```dockerfile
RUN curl -fsSL https://claude.ai/install.sh | bash -s -- 2.1.287
```

masudaの各リリースは、その時点の最新のClaude Codeで実機検証し、`masuda init`の雛形にその版を書く（検証した版は`masuda version`の`claude code:`の行）。上げるときはこの行の数字を変えて`masuda image build`する。`masuda init`は既にあるDockerfileを書き換えないので、masudaを上げても版は変わらない。Dockerfileの版が検証済みの版と違うとき（引数なしで最新版を入れる形も含む）は、`masuda image build`が始めに`note:`の1行で知らせる（ビルドは止めない）。

### images

| 型 | 既定 |
|---|---|
| オブジェクト（エントリ名→設定） | 空 |

イメージのエントリごとのVMの設定。書かなかったエントリは既定値で動く。

| キー | 型 | 既定 | 意味 |
|---|---|---|---|
| `diskMiB` | 整数（MiB） | `4096` | VMの書き込めるルートディスクの最小容量。ビルドのキャッシュやテストの生成物が多いリポジトリでは増やす。イメージに`resize2fs`（e2fsprogs）が要る（`ubuntu:24.04`には入っている） |
| `memoryMiB` | 整数（MiB） | `4096` | VMのメモリ。テストを並列に流すと足りなくなるリポジトリでは増やす。ホストの物理メモリの総量を超えると、`run`・`resume`がワークスペースを作る前に断る。総量以内でも、他のプロセスが使っていれば足りないことはある |
| `cpus` | 整数 | `4` | VMのCPU数。ホストのCPU数を超えると、`run`・`resume`がワークスペースを作る前に断る |

特権コマンドのVMも、そのコマンドが使うイメージのエントリの値で作る。

### egress

| 型 | 既定 |
|---|---|
| 文字列の配列（ホスト名） | 空 |

VMから届いてよいホストの宣言。`*.example.com`のように先頭の`*.`だけワイルドカードにでき、`a.example.com`には当たるが`example.com`自身には当たらない。ポートは書けない。`masuda egress approve`で承認したものだけが使え、さらにワークフローの各ノードが`egress:`でその中から選んだものだけが、そのノードの間だけ開く（[秘密・egress・特権コマンド](secrets-and-egress.md#egress)）。

Claude APIのホスト（`api.anthropic.com`）は、宣言や承認に関係なく常に開いている。

### secrets

| 型 | 既定 |
|---|---|
| オブジェクトの配列 | 空 |

秘密の宣言。値はここに書かず、`masuda secret set <name>`で登録する。

| キー | 型 | 既定 | 意味 |
|---|---|---|---|
| `name` | 文字列 | （必須） | VMの中で環境変数の名前になる。英字か`_`で始まり英数字と`_`だけ。重複不可。`CLAUDE_CODE_OAUTH_TOKEN`は予約済みで宣言できない |
| `hosts` | 文字列の配列 | 空 | 本物の値を送ってよいホスト（`egress`と同じ書き方）。`placeholder`モードでは1つ以上必須 |
| `mode` | `"placeholder"`か`"plaintext"` | `"placeholder"` | `placeholder`: VMにはプレースホルダを置き、`hosts`宛ての通信でだけ本物に置き換える。`plaintext`: 本物の値をVMの環境変数に置く（`masuda secret approve`が要る） |
| `in` | `"header"`・`"body"`の配列 | `["header"]` | プレースホルダを置き換える場所。リクエストの本文に値を入れるAPIなら`"body"`を足す |

### envFiles

| 型 | 既定 |
|---|---|
| オブジェクトの配列 | 空 |

VMの作業ツリーに生成するdotenv形式のファイル。手元の`.env`をVMへ写すのではなく、ここから毎回作る。

| キー | 型 | 意味 |
|---|---|---|
| `path` | 文字列 | `/workspace`からの相対パス（例 `.env`、`config/app.env`）。絶対パス・作業ツリーの外・`.git/`の下は不可。重複不可 |
| `vars` | 文字列の配列 | 書き出す変数の名前。`secrets`で宣言した名前ならプレースホルダ（`plaintext`なら本物の値）、それ以外は`settings.local.json`の`vars`の値 |

値は常に`NAME="value"`の形で書かれる。生成したファイルはVMの`.git/info/exclude`に足されるので、コミットや計画外の変更には入らない。

### privilegedCommands

| 型 | 既定 |
|---|---|
| オブジェクト（コマンド名→宣言） | 空 |

rootやDockerが要るコマンドを、別の使い捨てVM（root）で動かすための宣言。エージェントは名前で呼ぶことしかできない（[秘密・egress・特権コマンド](secrets-and-egress.md#privileged)）。コマンド名は`image`と同じ文字種。

| キー | 型 | 既定 | 意味 |
|---|---|---|---|
| `description` | 文字列 | 空 | エージェント向けの説明（何をするか、いつ使うか）。VMの`/masuda/privileged-commands.json`に写る。承認の対象ではない |
| `command` | 文字列 | （必須） | rootのシェルで`/workspace`から動かすコマンド |
| `image` | 文字列 | （必須） | 特権VMのイメージのエントリ。`.masuda/images/<image>/Dockerfile`が要る |
| `inputs` | globの配列 | 空 | gitで運ばれない（gitignoreされた）ファイルのうち、特権VMへ運ぶもの |
| `outputs` | globの配列 | 空 | 終わった後に特権VMから回収するファイル |
| `timeoutSeconds` | 整数（秒） | `0`（= 1時間） | 打ち切りまでの時間。負は不可 |

globは`/workspace`からの相対パスで、`*`・`?`・`[...]`は1つの階層の中、`**`は0個以上の階層に当たる。`..`・`.`・空の階層は書けない。ディレクトリ名だけのパターンは中のファイルに当たらないので、中身は`dir/**`と書く。

### checks

| 型 | 既定 |
|---|---|
| オブジェクト（チェック名→シェルコマンド） | 空 |

VMの中に`/masuda/checks/<チェック名>`という実行可能スクリプトとして置かれる。ワークフローの`exec`ノードが`command: ["/masuda/checks/test"]`のように呼ぶ。

- コマンドはシェルの1行として、ログインシェル（`sh -el`）で`/workspace`から動く。途中のコマンドが失敗すればそこで終わる
- 終了コード0で成功（`done`）、それ以外で失敗（`failed`）
- ワークフローが呼ぶチェックが宣言されていなければ、`masuda run`が始める前に断る。同梱の`develop`は`test`を使う
- チェック名は`image`と同じ文字種

### claudeSettings

| 型 | 既定 |
|---|---|
| JSONオブジェクト | 空 |

VMの中のClaude Codeの`~/.claude/settings.json`へ合成する内容。`env`（エージェントのプロセスに渡す環境変数）や`permissions`などを書ける。`hooks`はmasudaが自分のものを置くので、masudaのものが優先される。

`model`を書くと、メインセッションと、`model`を書いていない役（[エージェントの書き方](workflows.md#write-agent)）のモデルになる。役のサブエージェントはメインセッションのモデルを継承するため。役ごとに変えるなら[`agents`](#agents)に書く。

### agents

| 型 | 既定 |
|---|---|
| オブジェクト（役の名前→`{"model": ..., "effort": ...}`） | 空 |

役ごとのモデルと推論の努力量の上書き。同梱の役（`reviewer`等）にも`.masuda/agents/`の役にも効く。同梱の役を定義ごと`.masuda/agents/`へ写して`model`を書き足すと、masudaを上げても本文の更新が届かなくなるので、モデルだけ変えたいときはここに書く。

```json
{
  "claudeSettings": { "model": "sonnet" },
  "agents": {
    "reviewer": { "model": "opus" },
    "implementer": { "model": "opus", "effort": "high" }
  }
}
```

この例では、メインセッションと上書きの無い役はSonnet、`reviewer`はOpus、`implementer`はOpusでeffort `high`で動く。

| キー | 型 | 意味 |
|---|---|---|
| `model` | 文字列（空不可） | 役定義のfrontmatterの`model`と同じ値（`sonnet`・`opus`・`haiku`等の別名、フルのモデルID、`inherit`） |
| `effort` | `low`・`medium`・`high`・`xhigh`・`max`のいずれか | 役定義のfrontmatterの`effort`と同じ |

- どちらも任意だが、少なくとも一方を書く（`{}`は断る）。書かなかった方は役定義のままになる
- 優先順位は「`agents`の値 ＞ 役定義のfrontmatterの`model`・`effort` ＞ 省略時（`model`はメインセッションのモデル＝`claudeSettings`の`model`、`effort`はセッションの既定）」
- キーは役の名前（エージェント定義の`name`）。`reviewer`と書き、`agents/reviewer`とは書かない。名前は`masuda workflow show <ワークフロー>`の図の`agents/<名前>`、または同梱のエージェントの一覧（[masuda-engineの`engine/defaults/agents/`](https://github.com/TadahiroYamamura/masuda-engine/tree/main/engine/defaults/agents)のファイル名）で分かる
- 定義に無い名前があると、打ち間違いを黙って無視しないよう、`masuda run`が始める前に断り（知っている役の名前を並べる）、`masuda workflow check`も問題として出す
- 効くのはVMの中のサブエージェントの定義だけで、ワークフローの検査や`continues`の判定は役定義のままで行う。`continues`で続きが成立したサブエージェントは、起動時の値のまま動く

### publish

| キー | 型 | 既定 | 意味 |
|---|---|---|---|
| `remote` | 文字列 | `"origin"` | ワークフローの`publish`ノードが`target: remote`のとき、pushする先のremoteの名前（あなたのリポジトリの`git remote`の名前）。`target: local`（既定）では使わない |

## settings.local.json {#settings-local}

```json
{
  "egressApproved": ["api.anthropic.com", "pypi.org", "*.pythonhosted.org"],
  "secretsApproved": ["LEGACY_SDK_KEY"],
  "privilegedCommandsApproved": {
    "integration-test": { "declHash": "3b1f...e9" }
  },
  "claudeToken": "CLAUDE_CODE_OAUTH_TOKEN",
  "vars": { "APP_ENV": "development" },
  "stallAfter": "15m"
}
```

| キー | 型 | 既定 | 意味 | 書くコマンド |
|---|---|---|---|---|
| `egressApproved` | 文字列の配列 | 空 | `egress`のうち承認したホスト。宣言と承認の両方にあるものだけが使える | `masuda egress approve`・`reject` |
| `secretsApproved` | 文字列の配列 | 空 | `plaintext`モードの秘密のうち、本物の値をVMに置いてよいと承認した名前 | `masuda secret approve`・`reject` |
| `privilegedCommandsApproved` | オブジェクト（コマンド名→`{"declHash": "..."}`） | 空 | 特権コマンドの承認。承認した時点の宣言の内容のハッシュで、宣言が変わると効かなくなる | `masuda privileged-command approve` |
| `claudeToken` | 文字列 | `"CLAUDE_CODE_OAUTH_TOKEN"` | Claudeのトークンとして使う秘密の名前。複数のアカウントを使い分けるとき、別の名前で登録したトークンを選ぶ | 手で書く |
| `vars` | オブジェクト（名前→値） | 空 | `envFiles`の公開値（秘密として宣言していない変数の値）。`envFiles`の変数で、秘密でもなくここにも無いものがあれば`masuda run`が断る | 手で書く |
| `stallAfter` | 文字列（Goのduration。`10m`・`1h30m`等） | `config.json`の`stallAfter`（それも無ければ`"10m"`） | 無活動がこれだけ続いたら活動を`stalled`と表示する、このリポジトリでの上書き。何分黙れば異常かはマシンの速さやClaudeのプランで変わるので、ここに置ける。正でない値・読めない値は`run`・`resume`がエラーにする。`masuda serve --stall-after`があればそちらが勝つ | 手で書く |

`claudeToken`を変えたら、その名前で`masuda secret set <名前>`して値を登録する。

## pitfalls.jsonl {#pitfalls}

`develop`の計画に問いを立てる役（plan-questions）に渡す、**プロジェクト固有の落とし穴**。過去に踏んだ失敗を「どんな変更のときに（`trigger`）、何を問うか（`question`）」の形で残しておくと、計画がそれに当たるとき、計画への問いとして挙がる。問いには計画を直す役が答え、答えられなければplan gateの前に人間に聞かれる（[develop](workflows.md#develop)）。任意のファイルで、`masuda init`は作らない。同梱の落とし穴は無い。

1行に1件のJSONオブジェクト（JSON Lines）。空行と`#`で始まる行は読み飛ばす。

```text title=".masuda/pitfalls.jsonl"
# 日時まわり
{"id": "tz-naive", "category": "data", "trigger": "日時を保存・比較・集計する変更", "question": "タイムゾーンの無い日時が混ざらないか", "background": "2025年3月、夏時間の切り替えの日に日次集計が1時間分ずれた"}
{"id": "migration-order", "category": "release", "trigger": "DBのスキーマを変える変更", "question": "旧版のアプリが新しいスキーマで動き続けられるか", "background": "デプロイ中に旧版が新しい列を知らずにINSERTして失敗した"}
```

| キー | 型 | 意味 |
|---|---|---|
| `id` | 文字列（空白を含まない） | 落とし穴の名前 |
| `category` | `spec`・`security`・`data`・`release`・`regression`・`performance`・`maintainability`・`other`のいずれか | 見落とすと起きる被害の種類。問いの分類になる |
| `trigger` | 文字列 | この落とし穴が当てはまる変更（きっかけ）。自然文で書く |
| `question` | 文字列 | 当てはまったときに立てる問い。役が計画に合わせて具体化する |
| `background` | 文字列 | この落とし穴が生まれた背景 |

すべて必須で、空にできない。ほかのキーがあると読み込みを断る。`masuda run`は`.masuda/`を写すときにこのファイルも写して1行ずつ検査し、誤りがあれば実行を始めずに行番号と理由を返す（例: `.masuda/pitfalls.jsonl: line 3: category "edge" is not one of spec, ...`）。`masuda workflow check`も同じ検査をして、誤りの行ごとに問題として出す。検査を通ったものが、注釈の行を除いてVMの`/masuda/pitfalls.jsonl`に置かれる。

[レビュー観点](reviews.md)との違い: 観点は実装した差分を**判定する**ためのもの、落とし穴は実装の前の計画に**問う**ためのもの。

## claude/ と claude.local/ {#claude-dir}

VMの中のClaude Code（メインセッションとサブエージェント）の`~/.claude/`に置くもの。どちらも任意で、`masuda init`は作らない。

| ディレクトリ | 誰のものか | コミット |
|---|---|---|
| `.masuda/claude/` | チーム | する |
| `.masuda/claude.local/` | あなた | しない（`masuda init`が`.gitignore`に`.masuda/claude.local/`を足す） |

置けるもの:

| パス | VMでの置き場所 |
|---|---|
| `CLAUDE.md` | `~/.claude/CLAUDE.md`の後ろ。masudaのループ規約の後に、見出し`# プロジェクトのルール（.masuda/claude）`と「ループ規約と矛盾するときはループ規約が優先」の1行を挟んで連結する |
| `rules/*.md` | `~/.claude/rules/` |
| `skills/<名前>/`（`SKILL.md`と、それが使うファイル） | `~/.claude/skills/<名前>/`（ディレクトリごと） |

- 同じ相対パスのファイルは`claude.local/`が勝つ。`CLAUDE.md`も連結ではなく、`claude.local/`のもので置き換わる
- これ以外のもの（`agents/`・`settings.json`・`commands/`等）は無視し、`masuda serve`の標準エラーに1行の警告を出す。サブエージェントの定義は`.masuda/agents/`、Claude Codeの設定は`settings.json`の[`claudeSettings`](#claudesettings)に書く（`~/.claude/agents/`と`~/.claude/settings.json`はmasudaが作る）
- シンボリックリンクは写さない
- 読むタイミング: `masuda run`が`.masuda/`を写すときに2つを重ねた結果を写し、その実行は終わりまで（`resume`しても）同じ中身を使う
- サブエージェントがスキルを呼ぶには、役の`tools`に`Skill`が要る。同梱の役は持っている。自分で書いた役で`tools`を限っているなら`Skill`を足す（[エージェントの書き方](workflows.md#write-agent)）

どこに何を書くかは[概念](concepts.md#where-to-write)の対応表を参照。

## config.json {#serve-config}

リポジトリに依らない`masuda serve`全体の設定。置き場所は`$XDG_CONFIG_HOME/masuda/config.json`（未設定なら`~/.config/masuda/config.json`）で、`masuda serve --config <path>`で変えられる。無ければすべて既定。読むのは`masuda serve`の起動時だけなので、書き換えたらserveを起動し直す。

```json title="~/.config/masuda/config.json"
{
  "listen": "127.0.0.1:7788",
  "stallAfter": "15m",
  "diskWarnBytes": 32212254720
}
```

| キー | 型 | 既定 | 意味 |
|---|---|---|---|
| `listen` | 文字列（`<IP>:<ポート>`） | 空（UDSだけ） | UDSに加えて、このループバックのアドレスでも公開APIを待ち受ける。ブラウザのGUIを使うときに設定する。ループバック以外（`0.0.0.0`等）は書けない。注意点は[接続](../api/connect.md) |
| `sandboxSocket` | 文字列（パス） | `$XDG_RUNTIME_DIR/masuda-sandbox.sock` | `masuda-sandbox serve`のソケット。`--sandbox-socket`を指定すればそちらが勝つ |
| `stallAfter` | 文字列（Goのduration） | `"10m"` | 無活動のしきい値の既定。リポジトリの`settings.local.json`の`stallAfter`が上書きする |
| `diskWarnBytes` | 整数（バイト） | `21474836480`（20GiB） | ワークスペース置き場（`~/.local/share/masuda/workspaces/`）の使用量の警告しきい値 |

知らないキー・読めない値があると`masuda serve`は起動しない。
