# CLIリファレンス

```text
masuda <command> [flags]
```

`serve`・`init`・`version`以外のコマンドは、動いている`masuda serve`の公開APIを叩くだけのクライアント。`masuda serve`が起動していなければ接続エラーになる。

## 共通の約束

- `--socket <path>`: どのクライアントコマンドでも受け付ける。`masuda serve`の待ち受けるUnixソケット。既定は`$XDG_RUNTIME_DIR/masuda.sock`（`XDG_RUNTIME_DIR`が無ければ`/tmp/masuda-<uid>/masuda.sock`）
- フラグは`-flag`でも`--flag`でもよく、位置引数の前にも後にも書ける（`masuda gate approve <id> <occ> --hash h`も`masuda gate approve --hash h <id> <occ>`も同じ）
- `--repo <dir>`の既定は今いるディレクトリ（`workflow`の3つだけ既定が違う）。どのコマンドでも**作業ツリーのトップ**を指す必要があり、サブディレクトリを渡すとエラーになる
- 終了コード: 成功で0、エラーで1、使い方の誤りで2。各コマンドの`-h`で使い方が出る
- `<id>`はワークスペースのID（`masuda run`・`masuda list`が出す12桁）、`<occurrence>`はゲートや質問の出現ID（`masuda gate list`・`masuda question list`が出す）

## 一覧

| コマンド | 用途 |
|---|---|
| [`serve`](#serve) | 公開APIを待ち受ける常駐プロセスを起動する |
| [`run`](#run) | ワークフローを新しいワークスペースで始める |
| [`resume`](#resume) | 止めたワークスペースを再開する |
| [`list`](#list) | ワークスペースの一覧 |
| [`watch`](#watch) | 状態とイベントを流し続ける |
| [`chat`](#chat) | VMの中のClaude Codeの画面にアタッチする |
| [`gate`](#gate) | ゲートの一覧・内容・判断 |
| [`question`](#question) | 質問の一覧・回答 |
| [`stop`](#stop) | VMを止める（記録は残す） |
| [`remove`](#remove) | ワークスペースを消す（exportsは残す） |
| [`init`](#init) | 対象リポジトリに`.masuda/`の雛形を置く |
| [`egress`](#egress) | 通信先の宣言の一覧と承認 |
| [`secret`](#secret) | 秘密の一覧・値の登録・平文の承認 |
| [`privileged-command`](#privileged-command) | 特権コマンドの一覧・承認 |
| [`image`](#image) | イメージの一覧・ビルド |
| [`workflow`](#workflow) | ワークフローの一覧・図・検査 |
| [`version`](#version) | バージョンを出す |

## serve

```text
masuda serve [--socket <path>] [--data-dir <dir>] [--sandbox-socket <path>] [--stall-after <duration>] [--config <path>] [--fake-sandbox]
```

常駐プロセス。Ctrl-C（SIGINT）かSIGTERMで止まる。止めると動いていたワークスペースは`stopped`になる。

| フラグ | 既定 | 意味 |
|---|---|---|
| `--socket` | `$XDG_RUNTIME_DIR/masuda.sock` | 公開APIを待ち受けるUnixソケット |
| `--data-dir` | `$XDG_DATA_HOME/masuda`（未設定なら`~/.local/share/masuda`） | ワークスペース・秘密・イメージの記録を置く場所 |
| `--sandbox-socket` | `config.json`の`sandboxSocket`、無ければ`$XDG_RUNTIME_DIR/masuda-sandbox.sock` | `masuda-sandbox serve`のソケット |
| `--stall-after` | `0` | 無活動がこれだけ続いたら活動を`stalled`にする（例 `15m`）。0なら各リポジトリの`settings.local.json`の`stallAfter`、無ければ`config.json`の`stallAfter`（既定`10m`）に従い、0以外なら全ワークスペースでこちらが勝つ |
| `--config` | `$XDG_CONFIG_HOME/masuda/config.json`（未設定なら`~/.config/masuda/config.json`） | serve全体の設定ファイル（[設定ファイル](settings.md#serve-config)）。無ければすべて既定 |
| `--fake-sandbox` | `false` | VMを使わず、プロセス内のフェイクで動かす（開発・テスト用。エージェントは動かない） |

## run

```text
masuda run <workflow> --branch <name> [--repo <dir>] [--base <ref>] [--image <entry>] [--input name=value|name=@file]...
```

ワークフローを新しいワークスペースで始め、`<id> starting`を出してすぐ返る。VMの起動は裏で進む。

| フラグ | 既定 | 意味 |
|---|---|---|
| `<workflow>`（または`--workflow`） | なし（必須） | 例 `workflows/develop`。`.masuda/`からの拡張子なしのパス |
| `--branch` | なし（必須） | 作るブランチ。publishするワークフロー（`develop`等）では対象リポジトリに既にある名前は使えない。publishしないワークフロー（`review`等）では既にあるブランチを指定でき、その先頭から始まる |
| `--repo` | `.` | 対象リポジトリ |
| `--base` | 新しいブランチなら今チェックアウトしているブランチ（detachedならそのコミット）。既にあるブランチならリポジトリの既定のブランチ | 分岐元。既にあるブランチでは、これとブランチの分岐点が差分の基準になる |
| `--image` | `settings.json`の`image`（既定`default`） | イメージのエントリ |
| `--input` | なし | ワークフローの入力。`name=value`で文字列、`name=@file`でファイルの中身。繰り返し可。ワークフローの`inputs`に並ぶ名前はすべて要る |

始める前に、定義の検査・設定の読み込み・秘密や承認の確認をまとめて行い、足りないものがあれば何も作らずにエラーで返す。

## resume

```text
masuda resume <id>
```

`stopped`のワークスペース、またはVMの起動に失敗して`blocked`になったワークスペースを、記録から再開する（[運用](operations.md#resume)）。

## list

```text
masuda list [--repo <dir>] [--all]
```

| フラグ | 既定 | 意味 |
|---|---|---|
| `--repo` | 空（全リポジトリ） | そのリポジトリのワークスペースだけを出す |
| `--all` | `false` | `done`・`stopped`も出す（`blocked`は常に出る） |

列の読み方は[運用](operations.md#list)。

## watch

```text
masuda watch [<id>] [--after <seq>]
```

状態の変化とイベントを1行ずつ流し続ける。`<id>`を省くと全ワークスペース。Ctrl-Cで終わる（実行には影響しない）。

| フラグ | 既定 | 意味 |
|---|---|---|
| `--after` | `0` | このイベント番号より後から再送する。0なら、今の状態を1行ずつ出してから新しいものだけ |

各行の先頭はイベント番号・時刻・ワークスペースID。種類ごとの形は[運用](operations.md#watch)。

## chat

```text
masuda chat <id>
```

VMの中のClaude Code（tmuxの`claude-work`セッション）にsshでアタッチする。`C-b d`で切り離せば、セッションはそのまま動き続ける。chatからゲートを閉じることはできない。動いていないワークスペース、起動中のワークスペースには使えない。

## gate

```text
masuda gate list [<id>]
masuda gate show <id> <occurrence>
masuda gate approve <id> <occurrence> [--hash <h>] [--file <path>]... [--comment <text>]
masuda gate reject <id> <occurrence> [--comment <text>]
masuda gate dismiss|halt|redo <id> <occurrence> [--comment <text>]
```

| サブコマンド | 動き |
|---|---|
| `list` | 開いているゲート（`<id>`を省くと全ワークスペース）。列はWORKSPACE・OCCURRENCE・GATE・TARGET・OPENED |
| `show` | ゲートの種類・`target_hash`・（あれば）反映されるcommit・判断済みなら判断、と中身。`triage`は懸念の本文、`deviation`は計画外で変わったファイルの一覧を出す。未判断なら、そのゲートで打てるコマンドを添える |
| `approve` | 承認する |
| `reject` | 却下する |
| `dismiss`・`halt`・`redo` | `triage`ゲートへの判断。懸念を退けて続ける／実行を止める／その出現をやり直させる |

| フラグ | 使えるサブコマンド | 意味 |
|---|---|---|
| `--hash` | `approve` | 承認する内容の`target_hash`。`show`が出したものを渡すと、見た内容と違っていれば断られる。省くと、その時点で開いているゲートのものを使う |
| `--file` | `approve` | `deviation`ゲートで計画に加えるファイル。繰り返し可。並べなかったファイルはコミットされない |
| `--comment` | すべての判断 | 判断に添えるコメント。却下のときはエージェントへのやり直しの指示になる |

判断済みのゲートにもう一度判断するとエラーになる。

## question

```text
masuda question list [<id>]
masuda question answer <id> <occurrence> <question-id>=<answer>...
```

`list`は開いている質問を、質問ごとのidと本文、選択肢（あれば）とともに出す。`answer`は`<質問のid>=<答え>`を1つ以上並べる。

```sh
masuda question answer 4f1c2a9e8b3d 000012 scope=yes "reason=既存のAPIは変えない"
```

## stop

```text
masuda stop <id>
```

VMを止めて`stopped`にする。stagingと記録は残り、`resume`で再開できる。既に`done`のワークスペースはエラー。masudaが実行を止めた`blocked`（triageの`halt`等）はVMを片付けるだけで`blocked`のまま（再開はできない）。

## remove

```text
masuda remove <id> [--force]
```

ワークスペースを消す。`workspaces/<id>/`のうち`exports/`だけを残す。動いているワークスペースは`--force`を付けたときだけ、止めてから消す。

## init

```text
masuda init [--repo <dir>]
```

対象リポジトリに`.masuda/`の雛形（`settings.json`、`images/default/Dockerfile`、同梱の14のレビュー観点`reviews/*.md`）を置き、`.gitignore`に`.masuda/settings.local.json`を足す。`masuda serve`は要らない。既にあるファイルは上書きしない。`.gitignore`に`.masuda/`ごと無視する行（`.masuda`・`.masuda/`・`.masuda/*`・`.masuda/**`、先頭`/`付きも）があれば、行を足さない。

## egress

```text
masuda egress list [--repo <dir>]
masuda egress approve <host> [--repo <dir>]
masuda egress reject <host> [--repo <dir>]
```

`settings.json`の`egress`に宣言したホストと、あなたの承認の有無を出す。`approve`は宣言にあるホストだけを承認できる。`reject`は承認を取り消す（宣言から消えたホストの承認も消せる）。承認は`settings.local.json`の`egressApproved`に書かれる。

## secret

```text
masuda secret list [--repo <dir>]
masuda secret set <NAME> [--repo <dir>]
masuda secret approve <NAME> [--repo <dir>]
masuda secret reject <NAME> [--repo <dir>]
```

| サブコマンド | 動き |
|---|---|
| `list` | 宣言した秘密の名前・モード・送り先・値の有無・承認の有無と、Claudeのトークンの有無。`plaintext`の秘密はMODEが`PLAINTEXT`と大文字で出る |
| `set` | 値を標準入力から読んで登録する。端末なら入力を画面に出さずに1行、パイプなら全部を読んで末尾の改行1つを落とす。宣言した名前と、Claudeのトークンの名前（`CLAUDE_CODE_OAUTH_TOKEN`、または`claudeToken`で選んだ名前）だけを受け付ける |
| `approve` | `plaintext`モードの秘密を、本物の値をVMに置いてよいと承認する。`placeholder`モードの秘密は承認が要らないのでエラー |
| `reject` | `plaintext`の承認を取り消す |

値をコマンドライン引数で受け取らないのは、シェルの履歴やプロセス一覧に残さないため。

```sh
masuda secret set LINEAR_API_KEY < ~/linear-key.txt
```

## privileged-command

```text
masuda privileged-command list [--repo <dir>]
masuda privileged-command approve <name> [--repo <dir>]
```

`settings.json`の`privilegedCommands`の一覧と承認の有無。APPROVEDが`stale`のものは、承認した後に宣言が変わったので承認が効いていない。`approve`はその時点の宣言の内容に承認を結びつける。取り消すコマンドは無い（`settings.local.json`の`privilegedCommandsApproved`から消す）。

## image

```text
masuda image list [--repo <dir>]
masuda image build [<entry>] [--repo <dir>]
```

`list`は`.masuda/images/`のエントリと、ビルド済みかどうか・最後のビルドのID。`build`は`.masuda/images/<entry>/Dockerfile`をビルドし、ログを標準エラーへ、ビルドのIDを標準出力へ出す。`<entry>`を省くと`settings.json`の`image`。

## workflow

```text
masuda workflow list [--repo <dir>]
masuda workflow show <workflow> [--repo <dir>]
masuda workflow check [<workflow>] [--repo <dir>]
```

`--repo`を省くと今いる作業ツリーのトップを使い、作業ツリーの外なら同梱の定義だけを見る。`run`と違い、作業ツリーの`.masuda/`をその場で読む。

| サブコマンド | 動き |
|---|---|
| `list` | ワークフローごとに、どこから来たか（ORIGIN: `bundled`は同梱、`repo`は`.masuda/workflows/`）と受け取る入力 |
| `show` | ワークフローの図をMermaidで出す。呼び出す部品のワークフローと、masudaが差し込むゲート（`deviation`・`triage`）も描く |
| `check` | 定義の検査。問題を1行ずつ出し、1つでもあれば終了コード1。無ければ`ok` |

`workflow check`の引数を省くと、rootのワークフロー（他のどのワークフローからも呼ばれないもの）をそれぞれ検査する。部品として呼ばれるワークフロー（`workflows/implement/build-step`等）は、呼び出し元から辿って検査される。

## version

```text
masuda version
```

ビルドに埋め込んだバージョンを出す。ソースからビルドしたものは`dev`。
