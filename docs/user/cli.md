# CLIリファレンス

```text
masuda <command> [flags]
```

`serve`・`init`・`prime`・`version`・`doctor`・`completion`・`privileged-command run`以外のコマンドは、動いている`masuda serve`の公開APIを叩くだけのクライアント。`masuda serve`が起動していなければ接続エラーになる。

## 共通の約束

- `--socket <path>`: どのクライアントコマンドでも受け付ける。`masuda serve`の待ち受けるUnixソケット。既定は`$XDG_RUNTIME_DIR/masuda.sock`（`XDG_RUNTIME_DIR`が無ければ`/tmp/masuda-<uid>/masuda.sock`）
- フラグは`-flag`でも`--flag`でもよく、位置引数の前にも後にも書ける（`masuda gate approve <id> <occ> --hash h`も`masuda gate approve --hash h <id> <occ>`も同じ）
- `--repo <dir>`を省くと、今いる作業ツリーのトップを使い、作業ツリーの外ならエラーになる（`workflow`の3つ・`list`・`doctor`は省いたときの扱いが違う。各コマンドの節を参照）。明示するときは**作業ツリーのトップ**を渡す。サブディレクトリを渡すとエラーになる
- 終了コード: 成功で0、エラーで1、使い方の誤りで2（`privileged-command run`だけは特権コマンドの終了コードを返す）。各コマンドの`-h`で使い方が出る
- CLIが出す文（エラー・ヘルプ・`doctor`）は英語。エラーは`masuda: <何が誤りか>; <何をすればよいか>`の形で、使い方の全文は出さない（引数の数が合わなければ、誤りと使い方の1行だけ）
- `<id>`はワークスペースのID（`masuda run`・`masuda list`が出す12桁）、`<occurrence>`はゲートや質問の出現ID（`masuda gate list`・`masuda question list`が出す）

## 一覧

| コマンド | 用途 |
|---|---|
| [`serve`](#serve) | 公開APIを待ち受ける常駐プロセスを起動する |
| [`run`](#run) | ワークフローを新しいワークスペースで始める |
| [`resume`](#resume) | 止めた・中断したワークスペースを再開する |
| [`list`](#list) | ワークスペースの一覧 |
| [`watch`](#watch) | 状態とイベントを流し続ける |
| [`chat`](#chat) | VMの中のClaude Codeの画面にアタッチする |
| [`gate`](#gate) | ゲートの一覧・内容・判断 |
| [`question`](#question) | 質問の一覧・回答 |
| [`stop`](#stop) | VMを止める（記録は残す） |
| [`remove`](#remove) | ワークスペースを消す（exportsは残す） |
| [`init`](#init) | 対象リポジトリに`.masuda/`の雛形と、ホストのエージェント向けの案内を置く |
| [`prime`](#prime) | ホストのエージェント向けのmasudaの使い方を出す |
| [`egress`](#egress) | 通信先の宣言の一覧と承認 |
| [`secret`](#secret) | 秘密の一覧・値の登録・平文の承認 |
| [`env`](#env) | `.env`の値を秘密と公開値へまとめて取り込む |
| [`privileged-command`](#privileged-command) | 特権コマンドの一覧・承認・単体実行 |
| [`image`](#image) | イメージの一覧・ビルド |
| [`workflow`](#workflow) | ワークフローの一覧・図・検査 |
| [`version`](#version) | masudaと接続先のmasuda-sandboxのバージョンを出す |
| [`doctor`](#doctor) | 動かすための前提を確かめる |
| [`completion`](#completion) | シェルの補完スクリプトを出す |

## serve

```text
masuda serve [--socket <path>] [--data-dir <dir>] [--sandbox-socket <path>] [--stall-after <duration>] [--config <path>] [--log-file <path>] [--fake-sandbox]
```

常駐プロセス。Ctrl-C（SIGINT）かSIGTERMで止まる。止めると動いていたワークスペースは`stopped`になる。

| フラグ | 既定 | 意味 |
|---|---|---|
| `--socket` | `$XDG_RUNTIME_DIR/masuda.sock` | 公開APIを待ち受けるUnixソケット |
| `--data-dir` | `$XDG_DATA_HOME/masuda`（未設定なら`~/.local/share/masuda`） | ワークスペース・秘密・イメージの記録を置く場所 |
| `--sandbox-socket` | `config.json`の`sandboxSocket`、無ければ`$XDG_RUNTIME_DIR/masuda-sandbox.sock` | `masuda-sandbox serve`のソケット |
| `--stall-after` | `0` | 無活動がこれだけ続いたら活動を`stalled`にする（例 `15m`）。0なら各リポジトリの`settings.local.json`の`stallAfter`、無ければ`config.json`の`stallAfter`（既定`10m`）に従い、0以外なら全ワークスペースでこちらが勝つ |
| `--config` | `$XDG_CONFIG_HOME/masuda/config.json`（未設定なら`~/.config/masuda/config.json`） | serve全体の設定ファイル（[設定ファイル](settings.md#serve-config)）。無ければすべて既定 |
| `--log-file` | `<data-dir>/logs/masuda-serve.log` | ログの書き先。端末には起動したことと、ログの置き場所だけを出す。起動のたびに前のファイルを`.1`に回す（古いものは1つだけ残す）。`-`なら今までどおり端末（標準エラー出力）に出す |
| `--fake-sandbox` | `false` | VMを使わず、プロセス内のフェイクで動かす（開発・テスト用。エージェントは動かない） |

## run

```text
masuda run <workflow> --branch <name> [--repo <dir>] [--base <ref>] [--image <entry>] [--input name=value|name=@file]...
```

ワークフローを新しいワークスペースで始め、`<id> starting`を出してすぐ返る。VMの起動は裏で進み、どの段階にいるかは`masuda list`のPOSITIONに出る（[トラブルシューティング](troubleshooting.md#starting-long)）。

| フラグ | 既定 | 意味 |
|---|---|---|
| `<workflow>`（または`--workflow`） | なし（必須） | 例 `workflows/develop`。`.masuda/`からの拡張子なしのパス |
| `--branch` | なし（必須） | 作るブランチ。publishするワークフロー（`develop`等）では対象リポジトリに既にある名前は使えない。publishしないワークフロー（`review`等）では既にあるブランチを指定でき、その先頭から始まる |
| `--repo` | 今いる作業ツリーのトップ | 対象リポジトリ |
| `--base` | 新しいブランチなら今チェックアウトしているブランチ（detachedならそのコミット）。既にあるブランチならリポジトリの既定のブランチ | 分岐元。既にあるブランチでは、これとブランチの分岐点が差分の基準になる |
| `--image` | `settings.json`の`image`（既定`default`） | イメージのエントリ |
| `--input` | なし | ワークフローの入力。`name=value`で文字列、`name=@file`でファイルの中身。繰り返し可。ワークフローの`inputs`に並ぶ名前はすべて要る |

始める前に、定義の検査・設定の読み込み・秘密や承認の確認をまとめて行い、足りないものがあれば何も作らずにエラーで返す。

## resume

```text
masuda resume <id>
```

`stopped`と`suspended`のワークスペースを、記録から再開する（[運用](operations.md#resume)）。`suspended`でVMが残っていれば、会話ログと実行ログを書き出してから壊して作り直す。`blocked`は再開できない。

再開の前に、`run`と同じ前提（秘密の値、承認、ワークフローが届く特権ノードの宣言と承認等）を確かめ直す。承認は作業ツリーの`settings.local.json`から読むので、取り消していれば断る。断ったときは何も変えない（`suspended`のVMも残る）。

## list

```text
masuda list [--repo <dir>] [--all]
```

| フラグ | 既定 | 意味 |
|---|---|---|
| `--repo` | 空（全リポジトリ） | そのリポジトリのワークスペースだけを出す |
| `--all` | `false` | `done`・`stopped`も出す（`suspended`・`blocked`は常に出る） |

列の読み方は[運用](operations.md#list)。`end:<ラベル>`で終わったワークスペース（`outcome needs_human`等）は、POSITIONに結果と、終わらせたエージェントの理由の1行目が出る。

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

VMの中のClaude Code（tmuxの`claude-work`セッション）にsshでアタッチする。`C-b d`で切り離せば、セッションはそのまま動き続ける。runが終わる（publish・discard）までに切り離すこと。アタッチしたままだとVMの破棄が終わらない（[トラブルシューティング](troubleshooting.md#chat-blocks-destroy)）。chatからゲートを閉じることはできない。動いていないワークスペース（`blocked`と、ワークフローの途中で止まった`suspended`はVMが残っているので使える。起動に失敗した`suspended`にはVMが無い）、起動中のワークスペースには使えない。`done`ではVMを壊してあるので、会話は`exports/transcripts/`で読む。

## gate

```text
masuda gate list [<id>]
masuda gate show <id> <occurrence>
masuda gate approve <id> <occurrence> [--hash <h>] [--file <path>]... [--comment <text>]
masuda gate reject <id> <occurrence> [--comment <text>]
masuda gate comment <id> <occurrence> <path>:<line> <text>
masuda gate dismiss|halt|redo <id> <occurrence> [--comment <text>]
```

| サブコマンド | 動き |
|---|---|
| `list` | 開いているゲート（`<id>`を省くと全ワークスペース）。列はWORKSPACE・OCCURRENCE・GATE・TARGET・OPENED |
| `show` | ゲートの種類・`target_hash`・（あれば）反映されるcommit・判断済みなら判断、と中身。`triage`は懸念の本文、`deviation`は計画外で変わったファイルの一覧を出す。`target: plan`（`plan`）は計画のJSONを、goal・summary・ステップごとの内容とテストと対象ファイル・計画への問いと答え（`checks`。問いごとに`<id> [addressed|out_of_scope|open] <問い>`と、次の行に答え）・採らなかった案・リスク・想定する副産物の節に分けて出す（JSONとして読めなければ全文）。差分のゲートは見出しで区別する: `target: diff`（`review`）は「publishされる内容（コミット済み）」、`target: step-diff`（`interim`）は「このステップでこれからコミットされる内容（未コミット）」。差分のゲートでは、承認対象のコミットに人間が付けたコメントを差分の後に`comments (sent to the agent on reject):`の見出しで`<path>:<line>: <本文>`の形に並べる（無ければ見出しごと省く）。未判断なら、そのゲートで打てるコマンドを添える |
| `approve` | 承認する |
| `reject` | 却下する。差分のゲートなら、`comment`で付けた行コメントも`--comment`の本文とともにエージェントへ届く |
| `comment` | 差分のゲート（`review`・`interim`）の承認対象のコミットの行にコメントを付ける。`<path>`は差分の新しい側のファイル、`<line>`はその行番号（1から）。本文は引用符で囲まなくても残りの引数をつなげて1つにする。差分を対象にしないゲート（`plan`・`deviation`・`triage`）ではエラー。付けたコメントは却下したときだけエージェントへ届き、承認したときは差分ビュー用に残るだけ |
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

`list`は開いている質問を、質問ごとのidと本文、選択肢（あれば）とともに出し、最後にすべての問いに答えるコマンドの形（`answer: masuda question answer <id> <occurrence> '<question-id>=<answer>' ...`）を添える。1つの質問に複数の問いが入ることがあり、`answer`はそのすべてに答えを求める（`<質問のid>=<答え>`を問いの数だけ並べる。足りなければエラー）。

`develop`では、計画の承認（plan gate）の前に、計画についての質問が来ることがある。計画に立てられた問いのうち計画を直す役が判断できなかったものを、`SPEC-1`・`REGRESSION-2`のような問いのidでまとめて聞く（[develop](workflows.md#develop)）。

```text
$ masuda question list 4f1c2a9e8b3d
4f1c2a9e8b3d 0000006 (opened 10-03 01:40:12)
  SPEC-1: ステップ1: 退化三角形（1,2,3）を ValueError にするか
    指示書は「三角不等式を満たさない」とだけ書いており、等号の扱いが決められない
  REGRESSION-2: shapes/__init__.py に triangle を公開するか
    既存の circle・rectangle は __init__.py で公開しておらず、指示書にも記述が無い
answer: masuda question answer 4f1c2a9e8b3d 0000006 'SPEC-1=<answer>' 'REGRESSION-2=<answer>'
```

```sh
masuda question answer 4f1c2a9e8b3d 0000006 "SPEC-1=不正とする" "REGRESSION-2=公開しない"
```

## stop

```text
masuda stop <id>
```

会話ログと実行ログを`exports/`へ書き出してからVMを壊し、`stopped`にする。stagingと記録は残り、`resume`で再開できる。既に`done`のワークスペースはエラー（VMは終わったときに壊してある）。`suspended`はVMを片付けて`stopped`にする（そのまま`resume`できる）。ワークフローが記録した`blocked`（triageの`halt`等）はVMを片付けるだけで`blocked`のまま（再開はできない）。

## remove

```text
masuda remove <id> [--force]
```

ワークスペースを消す。消す前に実行ログを`exports/`へ写し（VMが残っていれば会話ログも）、`workspaces/<id>/`のうち`exports/`だけを残す。動いているワークスペースは`--force`を付けたときだけ、止めてから消す。

## init

```text
masuda init [--repo <dir>]
```

対象リポジトリに`.masuda/`の雛形（`settings.json`、`images/default/Dockerfile`、同梱の14のレビュー観点`reviews/*.md`）を置き、`.gitignore`に`.masuda/settings.local.json`を足す。`masuda serve`は要らない。既にあるファイルは上書きしない。雛形の`settings.json`の`egress`は空（Claude APIへの経路は常に開いているので宣言しない）。`.gitignore`に`.masuda/`ごと無視する行（`.masuda`・`.masuda/`・`.masuda/*`・`.masuda/**`、先頭`/`付きも）があれば、行を足さない。

ホストでClaude Codeを使うときのために、次の2つも足す。どちらも個人用でコミットしないファイルにする。コミットしたものは`/workspace`のcloneでVMにも届くが、VMにはmasudaのバイナリが無く、VMのエージェントにホストの使い方は要らないため。

| ファイル | 足すもの |
|---|---|
| `CLAUDE.local.md` | `<!-- BEGIN MASUDA -->`〜`<!-- END MASUDA -->`で囲んだ短い節（`masuda prime`への案内）。この印があれば足さない |
| `.claude/settings.local.json` | `hooks.SessionStart`に`masuda prime --hook-json`。`permissions.deny`に、人間が判断することを前提にしたコマンド（`secret set・approve・reject`、`env import`、`egress approve・reject`、`gate approve・reject・comment・dismiss・halt・redo`、`privileged-command approve`、`remove`）。`permissions.ask`に`question answer`。既にあるものは足さず、他の設定・フック・規則は残す（足したときはキーの並びが変わる）。JSONとして読めなければ書き換えずにエラーにする |

`.gitignore`にはこの2つも足す（`.claude/`ごと無視する行があれば`.claude/settings.local.json`は足さない）。

## prime

```text
masuda prime [--hook-json]
```

ホストのエージェント（Claude Code等）向けに、masudaの使い方（ワークスペースの始め方、状態の読み方、エージェントが代わりに打ってはいけない判断のコマンド）を標準出力に出す。`masuda serve`は要らない。文面はバイナリに埋め込んであり、masudaの版とともに変わる。

`--hook-json`を付けると、Claude CodeのSessionStartフックの形（`{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":...}}`）で出す。`masuda init`が登録するフックはこれを使い、セッションの開始時とcompactionの後に読み込ませる。

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
| `set` | 値を標準入力から読んで登録する。端末なら入力を画面に出さずに1行、パイプなら全部を読んで末尾の改行1つを落とす。宣言した名前と、Claudeのトークンの名前（`CLAUDE_CODE_OAUTH_TOKEN`、または`claudeToken`で選んだ名前）だけを受け付ける。`CLAUDE_CODE_OAUTH_TOKEN`は`--repo`を付けなければユーザー単位（どのリポジトリでも使う）に、付ければそのリポジトリだけに登録する |
| `approve` | `plaintext`モードの秘密を、本物の値をVMに置いてよいと承認する。`placeholder`モードの秘密は承認が要らないのでエラー |
| `reject` | `plaintext`の承認を取り消す |

値をコマンドライン引数で受け取らないのは、シェルの履歴やプロセス一覧に残さないため。

```sh
masuda secret set LINEAR_API_KEY < ~/linear-key.txt
```

## env

```text
masuda env import <file> [--repo <dir>]
```

`.env`形式のファイルの値を、名前ごとに振り分けて取り込む（[秘密・egress・特権コマンド](secrets-and-egress.md#env-import)）。

| 名前 | 行き先 | RESULT |
|---|---|---|
| `settings.json`の`secrets`で宣言した名前 | 秘密ストア（`secret set`と同じ。`plaintext`の承認は別に`secret approve`） | `secret` |
| 秘密ではないが、`envFiles`のどれかの`vars`にある名前 | `settings.local.json`の`vars` | `var` |
| `CLAUDE_CODE_OAUTH_TOKEN`と、`claudeToken`で選んだ名前 | 取り込まない（`secret set`で登録する） | `skipped: Claude token ...` |
| どちらでもない名前 | 取り込まない。標準エラーに一覧が出る。秘密なら`secrets`に宣言、そうでなければ`envFiles`の`vars`に足してから打ち直す | `skipped: not declared` |

- 値はどこにも表示しない。出すのは名前と行き先だけ
- 秘密を含むときだけ`masuda serve`が要る。`vars`だけならserveを通さず`settings.local.json`を直接書く（他の項目は残す）
- 書式の誤り（行番号付き）・同じ名前の2回目・宣言した秘密の空の値があれば、何も取り込まずにエラーにする
- 秘密の登録が途中で失敗したら、そこで止める（それより前の秘密は登録済み、後の秘密と`vars`は書かない）。どこまで入ったかは名前ごとのRESULT（`secret`・`failed`・`not imported`）で分かる。登録はどれも上書きなので、原因を直して同じコマンドを打ち直せばよい

ファイルの書き方:

- 1行に`NAME=VALUE`。空行と`#`で始まる行は無視し、先頭の`export `は読み飛ばす。`NAME`と`=`の前後の空白は無視する
- クォートしない値は前後の空白を落とし、空白に続く`#`から後をコメントとして捨てる（`a#b`はそのまま）
- `'...'`は中身をそのまま使う。`"..."`は`\n`・`\r`・`\t`・`\\`・`\"`・`\$`・`` \` ``だけを解き、それ以外の`\`はエラー。`masuda run`がVMに生成する`.env`と同じ書き方
- クォートは1行の中で閉じる（複数行の値は書けない。改行は`"..."`の中の`\n`で書く）。閉じた後には空白とコメントだけを置ける
- `${VAR}`等の展開はしない
- 値が空の行は、`vars`なら空文字として書く。宣言した秘密ならエラー

## privileged-command

```text
masuda privileged-command list [--repo <dir>]
masuda privileged-command approve <name> [--repo <dir>]
masuda privileged-command run <name> [--repo <dir>] [--ref <branch|commit>] [--out <dir>] [--sandbox-socket <path>] [--config <path>] [--data-dir <dir>]
```

`settings.json`の`privilegedCommands`の一覧と承認の有無。APPROVEDが`stale`のものは、承認した後に宣言が変わったので承認が効いていない。`approve`はその時点の宣言の内容に承認を結びつける。取り消すコマンドは無い（`settings.local.json`の`privilegedCommandsApproved`から消す）。

### run

特権コマンドを、ワークフローの外から実機の特権VMで1回動かす。スクリプト（`.masuda/images/`の中身や`command`）が特権VMで動くかを、ワークフローを回す前に確かめるためのもの（[秘密と通信](secrets-and-egress.md#privileged-command-run)）。`masuda serve`は要らない。`doctor`・`version`と同じく`masuda-sandbox serve`へ直接つなぐ。

- 宣言・承認・通信先は作業ツリーの`.masuda/settings.json`と`settings.local.json`から読む。宣言が無い・承認されていない・承認の後に宣言が変わったなら断る
- イメージは作業ツリーの`.masuda/images/<image>/`からビルドする（ログは標準エラー）。ビルドの記録は`--data-dir`に書く（`masuda image list`に出る）
- VMの`/workspace`に置くのは、既定では作業ツリーの今の状態（追跡しているファイルの未コミットの変更と、gitignoreされていない未追跡のファイル。ワークフローのスナップショットと同じ範囲）。`--ref`ならそのコミットのツリー
- `inputs`は、作業ツリーの**gitignoreされた**ファイルのうち宣言のglobに当たるものを、同じパスへ置く（`.git`の中は見ない、シンボリックリンクは辿らない、許可ビットを保つ）
- 利用者のリポジトリにはrefもオブジェクトも書かない（一時ディレクトリのリポジトリでツリーを作る）
- コマンドの標準出力・標準エラーは、そのまま端末へ流れる。終わると、拒否した通信先があればその一覧と、結果のディレクトリ（`exit-code`・`log`（末尾200KiB）・`outputs/`）の場所を標準エラーへ出す

| フラグ | 既定 | 意味 |
|---|---|---|
| `--repo` | 今いる作業ツリーのトップ | 対象リポジトリ（作業ツリーのトップ） |
| `--ref` | 空 | 作業ツリーの今の状態の代わりに渡すコミット（ブランチ名・コミット） |
| `--out` | 新しい一時ディレクトリ | 結果を置くディレクトリ |
| `--sandbox-socket` | `config.json`の`sandboxSocket`、無ければ`$XDG_RUNTIME_DIR/masuda-sandbox.sock` | `masuda-sandbox serve`のソケット |
| `--config` | `$XDG_CONFIG_HOME/masuda/config.json`（未設定なら`~/.config/masuda/config.json`） | serve全体の設定ファイル（`sandboxSocket`を読む） |
| `--data-dir` | `$XDG_DATA_HOME/masuda` | イメージのビルドの記録を書く先（`masuda serve`の`--data-dir`と揃える） |

終了コード:

| コード | 意味 |
|---|---|
| 特権コマンドの終了コード | コマンドが終わった |
| 128+番号 | コマンドがシグナルで終わった |
| 124 | `timeoutSeconds`を過ぎた |
| 1 | masuda自体の失敗（未承認、イメージのビルドの失敗、ジョブを動かせなかった等） |
| 2 | 使い方の誤り |

ホストのエージェントには打たせない（`masuda init`がdenyに入れる）。特権VMはrootで動き、承認した通信先へ出られるため。

## image

```text
masuda image list [--repo <dir>]
masuda image build [<entry>] [--repo <dir>]
```

`list`は`.masuda/images/`のエントリと、ビルド済みかどうか・最後のビルドのID。`build`は`.masuda/images/<entry>/Dockerfile`をビルドし、ログを標準エラーへ、ビルドのIDを標準出力へ出す。`<entry>`を省くと`settings.json`の`image`。Dockerfileが入れるClaude Codeの版が`masuda version`の検証済みの版と違えば（版を書いていないものも）、始めに標準エラーへ`note:`の1行を出す（[設定のimage](settings.md#image)）。

## workflow

```text
masuda workflow list [--repo <dir>]
masuda workflow show <workflow> [--repo <dir>]
masuda workflow check [<workflow>] [--repo <dir>]
```

`--repo`を省くと今いる作業ツリーのトップを使い、作業ツリーの外なら同梱の定義だけを見る。`run`と違い、作業ツリーの`.masuda/`をその場で読む。`masuda serve`は要らない（CIでも、serveを起動せずに`check`を流せる）。

| サブコマンド | 動き |
|---|---|
| `list` | ワークフローごとに、どこから来たか（ORIGIN: `bundled`は同梱、`repo`は`.masuda/workflows/`）と受け取る入力 |
| `show` | ワークフローの図をMermaidで出す。呼び出す部品のワークフローと、masudaが差し込むゲート（`deviation`・`triage`）も描く |
| `check` | 定義の検査。`.masuda/settings.json`の読み込みと、`agents`の役の名前が定義にあるかの照合も行う。問題を1行ずつ出し、1つでもあれば終了コード1。無ければ`ok` |

`workflow check`の引数を省くと、rootのワークフロー（他のどのワークフローからも呼ばれないもの）をそれぞれ検査する。部品として呼ばれるワークフロー（`workflows/implement/build-step`等）は、呼び出し元から辿って検査される。

## version

```text
masuda version [--sandbox-socket <path>] [--config <path>]
```

`masuda --version`・`masuda -v`も同じ。

ビルドに埋め込んだバージョン（ソースからビルドしたものは`dev`）、Goの版、masudaが前提にするsandboxの契約（`sandbox.proto`のSHA-256）、このmasudaが実機で検証したVMのClaude Codeの版（`masuda init`の雛形が入れる版）を出す。`masuda-sandbox serve`に届けば、そのバージョン・プラットフォーム・Gondolinの版と、契約がmasudaと合っているか（`contract: ok`か`contract: MISMATCH`）も出す。届かなくても終了コードは0。

```text
masuda __MASUDA_VERSION__ (go1.26.3 linux/amd64)
  sandbox contract sha256: 495d81…
  claude code: 2.1.287 (guest, verified)
masuda-sandbox __MASUDA_VERSION__ (linux/amd64, gondolin 0.12.0)
  sandbox contract sha256: 495d81…
  contract: ok
```

`--sandbox-socket`の既定は`masuda serve`と同じ（`config.json`の`sandboxSocket`、無ければ`$XDG_RUNTIME_DIR/masuda-sandbox.sock`）。

契約が合わないと`masuda serve`は起動せず、`run`・`resume`も断られる。masudaとmasuda-sandboxは同じバージョンのリリースを組で入れる（[導入](install.md)）。

## doctor

```text
masuda doctor [--sandbox-socket <path>] [--config <path>] [--data-dir <dir>] [--repo <dir>]
```

masudaを動かす前提を1項目ずつ確かめ、`[ok  ]`・`[warn]`・`[NG  ]`で出す。足りないもの（NG・warn）には直し方を添える。NGが1つでもあれば終了コード1（warnだけなら0）。`masuda serve`は要らない。

| 項目 | 確かめること |
|---|---|
| config.json | 読めるか（無ければ既定で可） |
| git | `git --version` |
| docker | sudo無しでdockerデーモンに繋がるか |
| node | 22.19以上か。24.17以上は既知の問題（Gondolin #134）でwarn |
| qemu | `qemu-system-x86_64`（arm64なら`qemu-system-aarch64`）と`qemu-img`（Gondolinが起動のたびに使う）。どちらか無ければNG |
| /dev/kvm（Linux）・HVF（macOS） | KVMを読み書きできるか、`kern.hv_support`が1か |
| masuda-sandbox | `masuda-sandbox serve`に届き、`GetServerInfo`の契約がmasudaと同じか |
| Claude token | ユーザー単位（`--repo`を付ければそのリポジトリの登録も）に登録されているか |

```text
[ok  ] git: git version 2.43.0
[NG  ] masuda-sandbox: /run/user/1000/masuda-sandbox.sock: sandbox service is not reachable (...); start masuda-sandbox serve, or check sandboxSocket in config.json
       start `masuda-sandbox serve --socket /run/user/1000/masuda-sandbox.sock`; if it is not installed, ...
```

## completion

```text
masuda completion bash|zsh
```

bashまたはzshの補完スクリプトを標準出力に出す。`masuda serve`には繋がない。シェルの起動ファイルに次の1行を書くと、コマンド・サブコマンド・フラグが補完される。

```sh
# ~/.bashrc
source <(masuda completion bash)

# ~/.zshrc
source <(masuda completion zsh)
```

ワークスペースのID・ワークフロー名・イメージ名は、補完のたびに`masuda list --all`・`masuda workflow list`・`masuda image list`を呼んで取る。そのため`masuda serve`が動いているときだけ候補に出る。繋がらないときは候補が空になり、エラーは出ない。`--socket`を入力済みなら、その値で呼ぶ。
