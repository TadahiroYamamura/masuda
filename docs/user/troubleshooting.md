# トラブルシューティング

まず見る場所:

- `masuda doctor`（前提がそろっているか。足りないものと直し方が出る）
- `masuda list --all`のSTATEとPOSITION（`suspended`・`blocked`なら理由が出る。違いは下の[止まった](#suspended-or-blocked)）
- `masuda watch <id>`の流れ
- `masuda chat <id>`でVMの中のClaude Codeの画面
- ホストの記録`~/.local/share/masuda/workspaces/<id>/records/`（`execution-log.jsonl`・`image-build.log`）
- `masuda serve`と`masuda-sandbox serve`のログ（`~/.local/share/masuda/logs/masuda-serve.log`と`~/.local/share/masuda-sandbox/logs/masuda-sandbox-serve.log`。置き場所は`masuda doctor`でも出る。前回の起動の分は末尾が`.1`のファイル）

## `masuda run`がすぐエラーになる

`masuda run`は始める前に設定と定義をまとめて確かめ、足りないものを一度に返す。よく出るもの:

| メッセージ（抜粋） | 直し方 |
|---|---|
| `connection refused`、`no such file or directory`（ソケット） | `masuda serve`が起動していない、または`--socket`の場所が違う |
| `sandbox service is not reachable` | `masuda-sandbox serve`が起動していない、またはソケットの場所が`masuda serve`の`--sandbox-socket`（`config.json`の`sandboxSocket`）と違う |
| `was built from a different sandbox.proto than masuda`、`does not implement GetServerInfo` | masudaとmasuda-sandboxのバージョンが組になっていない。`masuda version`で両方を確かめ、同じバージョンのリリースを入れ直す（[導入](install.md)）。`masuda serve`も同じ理由で起動しない |
| `the Claude API token CLAUDE_CODE_OAUTH_TOKEN has no value` | `masuda secret set CLAUDE_CODE_OAUTH_TOKEN`（ユーザー単位。`claudeToken`で別名を選んでいるならその名前で） |
| `the workflow runs /masuda/checks/test but checks.test is not declared` | `settings.json`の`checks`に`test`を書く |
| `the workflow has a privileged node, but privileged command "X" is not approved`（`is not declared`・`changed since it was approved`も） | ワークフロー（呼び出す部品のワークフローを含む）の`privileged`ノードが動かす特権コマンドの宣言・承認が足りない。宣言を書き、`masuda privileged-command approve X`。`masuda resume`でも同じ検査をする |
| `image default: .masuda/images/default/Dockerfile is missing` | `masuda init`するか、Dockerfileを置く |
| `branch already exists` | publishするワークフローでは、`--branch`に対象リポジトリにまだ無い名前を使う（既にあるブランチを指定できるのはpublishしないワークフローだけ） |
| `repo_root ... is not the top of its work tree` | `--repo`にサブディレクトリを渡している。トップを渡すか、`--repo`を省く（今いる作業ツリーのトップを使う） |
| `workflow ... needs inputs [instructions]` | `--input instructions=@task.md`を渡す |
| `workflow ... has problems:` | 定義の検査で落ちた。`masuda workflow check <workflow>`で同じ問題が出る |
| `json: unknown field "..."` | `settings.json`・`settings.local.json`に知らないキーがある（綴りの誤り）。[設定ファイル](settings.md) |
| `secret X is plaintext ... and is not approved` | `masuda secret approve X` |
| `envFiles ...: X is not a declared secret and has no value in vars` | `settings.local.json`の`vars`に値を書くか、秘密として宣言する |

## 止まった（`suspended`と`blocked`） {#suspended-or-blocked}

| STATE | 意味 | 続けるには |
|---|---|---|
| `suspended` | ワークフローの記録の外で止まった。理由が`sandbox boot failed: `ならVMの起動の失敗（[下](#boot-failed)）、`engine: `ならホストで動かすノードを動かせなかった（特権コマンドの承認が取り消されていた・宣言が変わった、ノードが選んだ通信先が承認されていない、publishが対象リポジトリに書けなかった、sandbox serviceがコマンドを動かせなかった等） | 理由を読んで直し、`stop`を挟まずに`masuda resume <id>`。止まったノードからやり直す。直す前に中を見たければ`masuda chat`（VMが残っていれば） |
| `blocked` | ワークフローが行き止まりを記録した（進入回数の上限を使い切って行き先が無い、出力が検証で落ち続けた、triageの`halt`等） | 再開できない。原因を調べて（[下](#why-stopped)）、定義や指示を直して`run`し直す |

## VMが起動しない {#boot-failed}

`masuda list`で`suspended`、理由が`sandbox boot failed: `で始まる。原因を直したら、`stop`を挟まずにそのまま`masuda resume <id>`できる。

- **`masuda-sandbox serve`に繋がらない**: 起動しているか、`--socket`のパスが`masuda serve --sandbox-socket`（既定`$XDG_RUNTIME_DIR/masuda-sandbox.sock`）と同じかを確かめる
- **KVMが使えない**（Linux）: `ls -l /dev/kvm`で存在とパーミッションを、`id -nG`で`kvm`グループに入っているかを確かめる（[導入](install.md)）。WSL2なら入れ子の仮想化が有効か
- **`disk_mib needs resize2fs in the image (install e2fsprogs)`**: masudaはVMのディスクを`diskMiB`（既定4096MiB）まで広げて起動するので、イメージに`resize2fs`が要る。`ubuntu:24.04`には入っているが、軽量なベースイメージには無いことがある。Dockerfileで`e2fsprogs`を入れる
- **イメージのビルドに失敗した**: `records/image-build.log`を読む。`masuda image build`で同じビルドを手元で繰り返せる
- **VMからの通信が軒並み502になる**: Nodeが24.17以上だと、Gondolinの既知の問題に当たる。`masuda-sandbox serve`を22.19以上・24.17未満のNodeで動かす

## `starting`のまま進まない {#starting-long}

`masuda list`のPOSITION（`masuda watch`なら`status`の行）に、起動のどの段階にいるかが出る。

| 段階 | 時間がかかる理由と見るもの |
|---|---|
| `building image (log: <パス>)` | Dockerfileのビルド。初回や`Dockerfile`を変えた後は数分かかることがある。進み具合は括弧の中のログを`tail -f`で見る |
| `booting the VM` | VMの起動とディスクの拡張。普段は数秒〜数十秒。長いなら`masuda-sandbox serve`のログ（`masuda doctor`が場所を出す）を見る |
| `preparing the guest` | VMの中にブランチをcloneし、設定とチェックを置く。リポジトリが大きいと長くなる |
| `starting Claude Code` | VMの中でClaude Codeを起動している |

## `No space left on device`

VMのルートディスクが足りない。ビルドのキャッシュ（Goの`GOCACHE`、pip・npmのキャッシュ）やテストの生成物で埋まりやすい。gitのスナップショットも取れなくなり、`suspended`で止まることがある。

- `settings.json`で、使うイメージのエントリのディスクを増やす。次の`run`・`resume`から効く

    ```json
    { "images": { "default": { "diskMiB": 8192 } } }
    ```

- キャッシュを`/tmp`（メモリ上）へ向ける。チェックのコマンドには頭に書き、エージェントには`claudeSettings.env`で渡す（次の節）

    ```json
    {
      "checks": { "test": "GOCACHE=/tmp/go-cache go test ./..." },
      "claudeSettings": { "env": { "GOCACHE": "/tmp/go-cache" } }
    }
    ```

## Dockerfileの`ENV`が効かない {#dockerfile-env}

DockerfileのENVはVMの中のプロセスに引き継がれ、PATHの先頭には`~/.local/bin`が足される。それでも`ENV PATH=/usr/local/go/bin:$PATH`等が届かないときは、次を確かめる。

- **PATH**: チェック（`/masuda/checks/*`）とメインのClaude Codeはログインシェルで動く。`/etc/profile`がPATHを置き換えるイメージ（Debian系のベースイメージ等。`ubuntu`は置き換えない）では、`ENV PATH`の追加分が消える。道具は`/usr/local/bin`に置くか、リンクを張る

    ```dockerfile
    RUN ln -s /usr/local/go/bin/go /usr/local/bin/go
    ```

- **masuda-sandboxが古い**: ENVを引き継がない版がある。`masuda version`でmasudaと同じバージョンか確かめる
- **確実に渡すには**: チェックはコマンドの頭に（`"test": "GOCACHE=/tmp/go-cache go test ./..."`）、エージェントには`settings.json`の`claudeSettings.env`に書く

## Dockerfileで置いたファイルがVMの中に無い {#vm-tmpfs}

`/run`・`/root`・`/tmp`・`/var/tmp`・`/var/cache`・`/var/log`は、VMの起動のたびに空になる。ホストの`docker run`では見えていたものが、VMでは無いことがある（例: PostgreSQLが`/var/run/postgresql`にロックファイルを作れず起動しない）。置き場所を変えるか、起動のたびに作る（[設定のimage](settings.md#image)）。

## イメージのビルドが`EACCES`で失敗する（`-modcacherw`）

非rootでGoのモジュールを`go mod download`したイメージ（モジュールのキャッシュが読み取り専用になる）は、古い`masuda-sandbox`ではビルドが`Build failed: EACCES, Permission denied: /tmp/gondolin-build-XXXX`で失敗する。

- masudaと同じバージョンの`masuda-sandbox`を入れ直し（[導入](install.md)）、`masuda-sandbox serve`を起動し直す
- 更新できないなら、Dockerfileで`go mod download -modcacherw all`のように`-modcacherw`を付けてキャッシュを書き込める形で取る

## 証明書のエラー・`NODE_EXTRA_CA_CERTS`の警告

VMからのHTTPSは、masuda-sandboxが途中で復号して検査する（MITM）。そのためのCAは、VMの起動時にシステムのCAバンドルへ加えられる。

- Claude Codeが`NODE_EXTRA_CA_CERTS=/etc/gondolin/mitm/ca.crt`を読めないと警告することがある。元のファイルが非rootから読めないためで、通信はシステムのCAバンドルで成り立っている。無視してよい
- イメージに`ca-certificates`が入っていないと、どのツールもHTTPSに失敗する。雛形のDockerfileには入っている
- システムのCAバンドル（`/etc/ssl/certs/ca-certificates.crt`）を読まず、自前の証明書の束を持つツールは、証明書の検証に失敗する。そのツールの設定でシステムのバンドルを指す（例: Pythonのrequestsなら`REQUESTS_CA_BUNDLE`、Nodeなら`NODE_EXTRA_CA_CERTS`）。エージェントには`claudeSettings.env`、チェックにはコマンドの頭で渡す
- 許可していないホストへの通信は証明書の問題ではなく拒否される。`masuda watch`に`http denied <host>`が出ていれば[egress](secrets-and-egress.md#egress)の設定を見る

## 依存をイメージに入れたのに、VMの中で取りに行って失敗する {#offline-deps}

実行中のVMは許可したホストへしか通信できない。イメージのビルド時に入れた依存でも、ツールによっては実行時にネットワークへ取りに行き、そこで失敗する。`masuda watch`に`http denied <host>`が出ているか、名前解決・通信のエラーで止まっていればこれ。

| ツール | 実行時に取りに行くもの | 対処（Dockerfile） |
|---|---|---|
| `npx <パッケージ>` | `npm install -g`で入れてあっても、レジストリのメタデータ | 下を参照 |
| Go | `go.mod`の`toolchain`行が指す版のツールチェーン、モジュール | `ENV GOTOOLCHAIN=local`と`ENV GOPROXY=off`。依存はビルド時に`go mod download`しておく |

npxは、ビルド時に一度呼んでnpmのキャッシュにメタデータを残し、実行時はキャッシュだけで解決させる。

```dockerfile
USER ubuntu
# 実行時と同じ状況（package.jsonはあるがnode_modulesは無いディレクトリ）で一度呼ぶ。
# package.jsonの無い場所で呼ぶと、別の場所のnode_modulesの版で解決してしまい、実行時に要るメタデータが残らない
RUN mkdir -p /tmp/npx-warmup \
 && cp <リポジトリのpackage.jsonの写し> /tmp/npx-warmup/ \
 && cd /tmp/npx-warmup && npx @redocly/cli --version \
 && rm -rf /tmp/npx-warmup
ENV npm_config_offline=true
```

- npmのキャッシュはユーザーごとなので、エージェントが動く`ubuntu`で呼ぶ
- `npm_config_offline=true`にしたのにキャッシュに無いと`ENOTCACHED`で、設定していないと通信のエラー（`EAI_AGAIN`など）で失敗する

## 動いているはずなのに進まない（`stalled`・`waiting_input`） {#stalled}

- **`stalled`**: VMの中のClaude Codeは生きているが、しきい値（既定10分）を超えて、Claude APIへの通信もツールの使用も無い。masudaは何もしない（表示だけ）。`masuda chat <id>`で画面を見る
    - 長いビルドやテストを動かしているだけなら、待てば戻る。マシンが遅くて頻繁に出るなら、`settings.local.json`の`stallAfter`を長くする（例 `"20m"`。次の`run`・`resume`から効く）。どのリポジトリでも長くしたいなら[`config.json`](settings.md#serve-config)の`stallAfter`（`masuda serve`の再起動で効く）
- **`waiting_input(idle)`**: メインのClaude Codeがターンを終え、人の入力を待っている。多くは、エージェントがmasudaの決まり（質問は`question`ノードでだけ聞く）を外れて、画面の上であなたに問いかけて止まっている。`masuda chat`で読み、続けてよければ「続けて」等と答える。直らなければ`masuda stop`→`masuda resume`で、そのタスクをやり直させる
- **`waiting_input(permission)`**: 道具の使用の許可を待っている。`chat`で答える
- **`dead`**: Claude Codeのセッションが無くなった。`masuda stop`→`masuda resume`

## 終わるはずのrunが`running`のまま進まない（chatにアタッチしたまま） {#chat-blocks-destroy}

ワークフローがpublish・discardに着いたのに、`masuda list`が`running`のまま変わらない。`masuda chat`でアタッチしたままだと、VMの破棄が終わらない（masuda-sandboxの不具合、TadahiroYamamura/masuda-sandbox#8）。

- chatの画面で`C-b d`を押して切り離す。すぐに破棄が進み、`done`になる
- runが終わりそうなとき（reviewゲートを承認した後など）は、先に切り離しておく

## 止まった・終わった原因を調べる {#why-stopped}

ワークスペースの`exports/`（`~/.local/share/masuda/workspaces/<id>/exports/`）に、実行ログ（`execution-log.jsonl`）とVMの中のClaude Codeの会話ログ（`transcripts/`）が残る。どの終わり方（`done`・`stop`・`remove`）でも書き出され、`remove`の後も消えない（[exports](operations.md#exports)）。

- どのノードでどう終わったかは実行ログで追う。`jq -c 'select(.kind=="finish" or .kind=="blocked" or .kind=="invalid") | {time, node, outcome, detail}' exports/execution-log.jsonl`
- エージェントが何を考えて止まったかは会話ログで読む。メインのセッションが`transcripts/-workspace/<session>.jsonl`、サブエージェントがその下の`subagents/agent-*.jsonl`
- `blocked`と、ワークフローの途中で止まった`suspended`はVMを残しているので、会話ログはまだ書き出されていない。`masuda chat`で画面を見るか、`masuda stop`で書き出してから読む
- `done`ではVMが壊れているので`masuda chat`は使えない。会話ログを読む

## `deviation`ゲートが思わぬファイルで開く（`__pycache__`等） {#deviation}

書き込めない役（調査・レビュー等）がテストを走らせただけで、追跡しているファイルが書き換わると、`deviation`ゲートが開く。例: gitで追跡している`__pycache__/*.pyc`、ロックファイル、テストが更新するスナップショット。

- その場は`masuda gate approve <id> <出現ID>`（`--file`無し）で続けられる。変更は計画に加わらず、コミットされない。却下すると実行全体が`blocked`で止まる
- 根本的には、生成物をgitで追跡しない。`git rm -r --cached __pycache__`して`.gitignore`に足し、コミットしてから`masuda run`する（VMへ渡るのはコミット済みの内容だけ）
- 計画の対象外のファイルを実装が変えた場合も、コミットの直前に開く。必要な変更なら`--file <path>`で計画に加え、そうでなければ却下して実装をやり直させる

## planゲートで止まらない・思った所で止まらない

| 症状 | 原因と対処 |
|---|---|
| planゲートの前に`done`で終わった（`outcome out_of_scope`） | 計画を立てる役が、依頼をこのリポジトリで扱うものではないと判断した。`exports/`には実行ログと会話ログだけが残る。役が書いた理由（`feedback`）は`masuda list --all`のPOSITIONに1行目が出て、全文は`workspace.json`の`reason`にある。課題の書き方を直して`run`し直す |
| planゲートの前に`done`で終わった（`outcome needs_human`） | `workflows/fix`の計画を立てる役が、指示が曖昧で計画を立てられないと判断した。役が確かめたい疑問は`masuda list --all`のPOSITIONに1行目が出て、全文は`workspace.json`の`reason`にある。疑問に答える形で指示書を直して`run`し直す |
| planゲートの前に`blocked` | POSITIONの理由を読む。出力が検証で落ち続けた（`invalid`）等 |
| planゲートの前に`suspended` | POSITIONの理由を読む。許可されていない通信を選んだノードがある（`masuda egress approve`してから`resume`）等 |
| `waiting_input`のまま、ゲートが開かない | 上の`waiting_input(idle)`。エージェントが画面の上で問いかけている |
| 計画を承認したのに、もう一度planゲートが開く | あるステップの実装が行き詰まった（`stuck`）か、テストが3回通らなかった。`checks.test`を雛形のまま（必ず失敗する）にしていないか確かめる。理由（実装の役の`feedback`）は`records/engine.json`にある。計画を却下（コメント付き）して直させる |
| ゲートがまったく開かない | 自分のワークフローに`approval`ノードが無い。`masuda workflow show`で確かめる |
| `interim`ゲートで止まる | 同梱のワークフローでは開かない。自分のワークフローに`gate: interim`のノードがあり、そこに達した（例: ステップの途中レビューで、自動では直しきれない指摘が残った）。`masuda gate show`で差分（このステップでこれからコミットされる内容）を見て、承認（そのままコミット）か却下（実装をやり直す）を選ぶ |

## publishに失敗する

reviewゲートを承認した後に`suspended`になり、理由に`has diverged from staging; not a fast-forward`等が出る。

- 対象リポジトリに、同じ名前のブランチが後から作られていて、fast-forwardにならない
- チェックアウト中のブランチと同じ名前で、作業ツリーに衝突する変更がある

masudaはリポジトリを無理に書き換えない。原因を直せば`masuda resume <id>`でpublishからやり直せる。結果はstagingに残っているので、別の名前で取り込んでもよい。

```sh
git fetch ~/.local/share/masuda/workspaces/<id>/staging.git feat/triangle:feat/triangle-masuda
```

## `masuda workflow check`が同梱の定義で失敗する

引数無しの`masuda workflow check`は、部品のワークフローも単独で検査するので、同梱の定義だけでも問題が出て終了コード1になる。`masuda workflow check workflows/develop`のように、始めるワークフローを指定する（[CLIリファレンス](cli.md#workflow)）。
