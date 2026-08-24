# ADR-0053: target repoが要求する特権操作は、宣言＋承認された使い捨てVMで実行し、メインサンドボックスVMには一切root/Docker権限を与えない

## Status

Accepted (2026-08-20)

## Context

[[0044-remove-docker-execution-runtime-vmbackend-only]]によりサンドボックスはCloud Hypervisor microVM（`VMBackend`）に一本化され、Issue #31の元々の主題（対象リポジトリのテストがDocker-in-Dockerを要求する場合の対応）が「VM移行で解決したか」を実機で確認する必要が生じた。

2026-08-19、トイプロジェクト（`.masuda/settings.json`にegressAllowlistを宣言・承認し、実際に`masuda workspace create`→`sandbox start`でVMを起動）による実機検証で、Issue #31が元々挙げていた2つの旧課題——sysbox（WSL2のseccomp notify衝突）・Docker Sandboxes（30秒アイドル自動停止）——はVM移行で実際に解消していることを確認した。VMは実マシンと同じLinuxカーネルで動く通常のゲストであり、コンテナのネスト固有の問題はそもそも発生しない。

しかし新しい壁が見つかった。メインサンドボックスVMのゲストイメージは`ubuntu`ユーザーに`systemctl poweroff`専用のNOPASSWD sudoしか与えておらず、一般sudoは意図的に許可していない。`download.docker.com`からDocker公式の静的バイナリをHTTPS経由で取得するところまでは成功したが、`dockerd`の起動は`dockerd needs to be started with root privileges`で拒否された。target repoの本来のテストスイート（testcontainers等）もmasudaのAIセッション自身も同じ`ubuntu`ユーザー・同じ制約下で動くため、これは現行のゲストイメージ設計そのものがDocker系ワークロードを一律ブロックしていることを意味する。

副次的に、rootfsの既定サイズ計算がDocker一式（展開後約226MB）を入れるだけで空き容量を逼迫させることも判明した。イメージの調達とそのビルドパラメータの宣言方法は[[0054-vm-images-declared-as-directories-under-masuda-images]]が扱い、本ADRはその上に乗る。

## Decision

メインサンドボックスVMには変更を加えない——`ubuntu`ユーザーへの一般sudo・Dockerデーモンのいずれも与えない。target repoが要求する特権操作（Docker-in-Dockerに限らず、sudoを要する操作全般）は、宣言・承認された上で、単機能・短命な別VMで実行する。

### 宣言（`.masuda/settings.json`）

```go
// PrivilegedCommands declares commands that require capabilities the main
// sandbox VM deliberately never grants (root, a Docker daemon, etc.).
// Each declared command runs in a fresh, single-purpose VM, destroyed
// immediately after. The main VM never gains this privilege itself.
PrivilegedCommands map[string]PrivilegedCommandDecl `json:"privilegedCommands,omitempty"`

type PrivilegedCommandDecl struct {
    Command        string   `json:"command"`
    Image          string   `json:"image"`             // 必須。ADR-0054の`.masuda/images/`配下のエントリ名
    TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
    Outputs        []string `json:"outputs,omitempty"` // `/workspace`相対。回収するパス
}
```

`MCPServerDecl`（[[0043-child-mcp-server-aggregator-with-project-user-config-split]]）と同じ宣言/承認分離パターンを踏襲する。`.masuda/settings.local.json`側に`PrivilegedCommandApproval{DeclHash string}`を持ち、宣言のハッシュと承認時のハッシュが一致する場合のみ実行を許可する。CLIは`masuda privileged-command approve/list/reject <name>`（`masuda mcp`/`masuda egress`と同型）。egressの到達可否は既存の`resolveEgressAllowlist(repoRoot)`をそのまま流用し、この機構専用の別宣言は作らない。

**このハッシュは宣言そのものだけでなく、`Image`が指すイメージディレクトリの内容（`Dockerfile`とイメージ側`settings.json`）のダイジェストも含める。** 宣言だけを固定すると、承認後に対象リポジトリがイメージの中身を差し替えることで、承認を受けていないものが特権付きで走る。何が実行されるかはコマンド文字列とイメージの中身の両方で決まるため、承認はその両方を覆う必要がある。

`Image`は必須フィールドとし、masudaのコード側に暗黙のfallbackイメージを持たせない。`ClaudeSettings`（[[0031-claude-settings-field-init-materialized-no-implicit-default]]）と同じ「masuda自身は暗黙の設定判断をしない」原則に従う——ADR-0054の`masuda image add <entry> --template docker`がエントリを実体化するため、宣言が指す名前はユーザーが見る・編集する・コミットするディレクトリとして必ずリポジトリ内に存在する。

### 呼び出し口: curated MCPツール `run_privileged_command(name)`

AIセッションはコマンド文字列を渡す自由を持たない。宣言済みの名前を指定して実行を要求するだけのツールとし、実際に実行されるコマンドは常にホスト側の承認済み宣言から読む。この一線がこの設計全体の安全性の核であり、「任意コマンドを特権付きで実行できる汎用ツール」として開放した場合、メインVMにrootを与えるのと実質的に同じになり効果が消える。

### 実行: 使い捨てVMの最小ライフサイクル

`VMBackend`の`rootfs.Build`・TAPプール・virtiofs起動ロジックを流用しつつ、メインVMより持ち物を絞る。

- **共有するもの**: `/workspace`の**都度スナップショット**（`git clone`ではなく単純なディレクトリコピー、未コミット変更を含む。ライブ共有にしないのは、メインVMのAIセッションとの書き込み競合を避け、特権コマンドの汚染・暴走の影響をそのVM専用のコピー内に閉じ込めるため）と、結果受け渡し用の新規共有ディレクトリのみ
- **共有しないもの**: `/masuda-secrets`（Claude OAuthトークン）・MCPリレー・SSH——対話アタッチ自体が不要（`--serial file=...`でコンソールログを拾えば足りる）
- **実行指示はrootfsではなく結果共有ディレクトリ経由でゲストへ渡す**: ホストがVM起動前にコマンド文字列・タイムアウト・ログ上限をファイルとして書き、ゲスト側のrunnerがそれを読む。カーネルコマンドラインに載せる案は、任意のシェルコマンドを空白区切りの1行へ埋め込む形になりクォートの問題を抱えるため採らない。runner本体（スクリプトとsystemd unit）は**masudaがrootfsへ注入**し、イメージのDockerfileには置かない——結果の返し方はmasudaのプロトコルであり、対象リポジトリが所有するファイルに依存させない
- **runnerの有効化はsystemdの`.wants`シンボリックリンクをrootfsへ注入して行う**: `systemd.wants=`をカーネルコマンドラインに渡す方が単純だが、**動かない**。`docker export`したファイルシステムには`/.dockerenv`が残るためゲストのsystemdは自分をコンテナだと判定し（`Detected virtualization docker.`）、コンテナ内のsystemdは「そのコマンドラインはホストのものだ」という理由で`systemd.*`オプションを一切読まない。VM起動・共有マウント・Dockerデーモン起動はすべて成功しているのに指定したunitだけが無言で起動しない、という形で実機で踏んだ。`/.dockerenv`自体を消すべきかはメインVMの挙動にも影響するため別途調査する（GitHub Issue #41）
- **使い捨てVMのegress到達可否は、実行中だけMACとリポジトリの対応を記録して解決する**: egressプロキシは「クライアントIP→DHCPリース→MAC→ワークスペース」で許可リストを引き、どのワークスペースにも一致しないIPは無条件で拒否する。使い捨てVMはワークスペースではないため、宣言済みの許可リストを流用するには対応表が要る。記録は実行中のみで、VMの後始末とともに削除する——終わった実行のMACが許可を持ち続けないため。ただし後始末はプロセスが強制終了されれば走らない（実際に踏んだ）ので、記録には監督プロセスのpidを含め、プロセスが消えている記録は不在として扱い削除する
- **ゲストカーネルのモジュールツリーはmasudaが注入する**: ホストの`/lib/modules/<version>`を、起動に使う`vmlinuz`と同じ版のままrootfsへ重ねる（既存の`virtiofs.ko`注入と同じ`ExtraFile`＋`depmod -b`の経路）。Ubuntuのディストリカーネルは`OVERLAY_FS`・`BRIDGE`・`VETH`・`NF_NAT`・`IP_NF_NAT`・`BRIDGE_NETFILTER`をいずれもモジュールとして持つため（`VIRTIO_NET`だけが`=y`で、メインVMがモジュール無しで通信できていたのはそのため）、ツリーが無いゲストでは`dockerd`が`iptables: Failed to initialize nft: Protocol not supported`で起動できないことを実機で確認した。Dockerが要求するモジュールだけを選んで入れる案は採らない——実行時に何がロードされるか（overlay・br_netfilter・ip_vs・各種ファイルシステム）を事前に列挙しきれないため、ツリーごと入れる

### 結果の回収

ゲスト側で宣言済みコマンドを実行した後、exit codeと標準出力・標準エラー（まとめて記録）を結果共有ディレクトリへ書き込んで`poweroff`する。ホスト側（状態デーモン）は、そのファイルと、`Outputs`が宣言するパスを`/workspace`スナップショットから回収する。スナップショットはホスト側のディレクトリコピーであるため、`poweroff`後もコマンドが書いたファイルはすべてホスト上に残っている——回収は新しい転送路の実装ではなく、ホストが既に持っているディレクトリからの選択とコピーである。

回収先は**実行ごとに独立したディレクトリ**とする。

```
/masuda-state/privilegedCommands/<entry-name>/<run-id>/
```

- `<run-id>`は実行ごとに生成する乱数（[[0030-workspace-id-drops-branch-name-prefix]]のワークスペースIDと同じ方式——短い16進、既存ディレクトリとの衝突時はリトライ）。実行内容のダイジェストにはしない。同じコマンドを同じ状態で2回走らせたら同じ値になり、2回目が1回目を上書きしてしまうため
- ワークスペースの状態ディレクトリ配下に置くため、既存のvirtiofs共有経由でメインVMからそのまま読める。回収したファイル自体をツールの戻り値に載せる必要がなく、AIは普通のファイル読み取りで扱える
- `privilegedCommands`という固定の一段を挟むのは、`<entry-name>`が対象リポジトリの`settings.json`由来の文字列であり、masuda自身が状態ディレクトリ直下に持つ名前（`plan/`・`triage_concern.json`等）と衝突させないため
- 自動削除しない。ワークスペースの状態ディレクトリごと、ワークスペース削除時に消える
- **ライブのworktreeへは書かない。** 回収先は状態ディレクトリであり`/workspace`ではない。フェーズ4/5のステップcommit（`git add -A`）が回収物を拾わないための一線でもある
- 回収するパスは相対のみとし、スナップショットのルート内に解決されることを確認する。外を指すsymlinkは辿らない（コピーを実行するのはホスト側のプロセスであるため）
- 回収の合計サイズとファイル数に上限を持ち、超過は黙って切り捨てずエラーとして返す。切り捨てられた成果物ファイルは壊れたファイルにしかならないため
- **ログ上限は2段構成**: (1) ホスト資源保護のためのハード上限（書き込み時点で強制打ち切り。使い捨てVMのディスク容量、および結果を中継するホスト側状態デーモンのメモリ使用量を守るためのもので、モデルのコンテキストウィンドウとは無関係）、(2) ツールの戻り値としてAIへ返す実用上の上限（モデルのコンテキストウィンドウに対して現実的な割合とするため）。ハード上限までのログは実行ディレクトリにファイルとして残るため、(2)で切れた続きはAI自身がファイルとして読める

`Outputs`の回収はVMが消えた後、ホストがスナップショットから行う（スナップショットはホスト自身が作ったディレクトリなので、転送ではなく単なるローカルコピーである）。回収時の規則:

- **symlinkは決して辿らない**。スナップショットの中身は信頼していないVM内でrootが書いたものであり、`/etc/shadow`を指すリンクがmasudaに`/etc/shadow`を読ませる経路になってはならない。通常ファイルとディレクトリ以外は、宣言されたパス自身がリンクである場合も含めてスキップし、スキップした事実を結果として報告する
- **上限（合計サイズ・ファイル数）の超過は打ち切りではなくエラー**。半分だけのcoverage報告書は「小さい結果」ではなく壊れたファイルであり、回収できたと伝えられた側はそれを使ってしまう
- **回収先ディレクトリは作り直す**。ゲストは結果共有ディレクトリを読み書き可能でマウントしており、同じ名前のディレクトリを自分で作れる。masudaが回収したと報告するものは、masudaが回収したものだけでなければならない
- **タイムアウトした実行でも回収する**。コマンドは途中で殺されているため成果物が半端な可能性はあるが、止まったコマンドが何を作りかけていたかを捨てる理由はなく、呼び出し側にはタイムアウトしたことが伝わっている

`run_privileged_command`の戻り値は、exit code・(2)の上限までのログ・実行ディレクトリのパス・回収したファイルの一覧である。

**ワークスペース全体の差分を持ち帰ることはしない。** 回収するのは宣言されたパスだけであり、スナップショット全体をメインVM側のworktreeへ反映する経路は持たない。

## Alternatives Considered

- **`ubuntu`をdockerグループに追加**（`dockerd`はsystemdでroot常駐、CLIだけ非rootから叩く一般的な構成）: `docker run --privileged`はゲストカーネルへの`insmod`等を含む全capabilityを付与し、コンテナはゲストカーネルを共有するため実質的にroot権限と同等になる。一般sudoを渋る意味がそもそも無いため却下。
- **`ubuntu`への一般sudo解禁**: 上記と実質同じ天井（ゲストカーネルレベルの完全な特権）に達するため、dockerグループ限定より安全というわけではない。cloud-hypervisorのVMエスケープを突かれた場合、cloud-hypervisorプロセス自体はホスト側でrootではなく開発者アカウント権限で動いているため「ホストroot奪取」ではないが、そのホストアカウントが到達できる他の全リポジトリ・認証情報（git・SSH・GH_TOKEN等）を失う前提で考える必要があり、「被害が限定的」という前提には根拠が無いと判断し却下。
- **rootless docker**（`newuidmap`/`newgidmap`・`slirp4netns`・`fuse-overlayfs`・`rootlesskit`一式をゲストイメージに焼き込む）: user namespace内のuid 0はカーネルモジュールロードや生のMMIOアクセスを実際には行えないため、上記2案と違いVMエスケープの試行手段を技術的に塞げる、実質的な効果のある選択肢ではある。ただし(1)必要パッケージが軒並み未導入かつegressフィルタが443番以外を通さないためビルド時ステージへの追加が必要、(2)masudaのゲストは対話ログイン前提を持たないため`systemctl --user`起動に`loginctl enable-linger`等の追加配線が要る、(3)cgroup v2 delegation等rootless特有の実機検証項目が多い、という理由で実装コストがメインVM改修としては見合わないと判断し、今回は採用しなかった。使い捨てVM方式は単機能・短命という別の性質でこの脆弱性の露出を絞り込むため、rootless化の複雑さを負わずに済む。
- **カーネルモジュールをイメージ側で用意する（ユーザーのDockerfileに`apt-get install linux-modules-*`を書かせる）**: `docker build`が動くのはホストのビルド環境であり、`uname -r`はVMが起動するカーネルではなくビルド環境自身のカーネルを返すため、素直に書くと別物を取りに行く。バージョンを直書きする形はvermagicの一致を人手の同期に委ねることになり、ホストが`apt upgrade`した瞬間に「Dockerfileは変えていないのに次のVMからdockerdが起動しない」という壊れ方をする。masudaがビルド引数でバージョンを渡す形にすればバージョン一致は解決するが、古いABIのパッケージはアーカイブから消えること、イメージのベースをUbuntu系に強制すること、イメージビルドにネットワークが必須になることが残る。起動するカーネルの出所を知っているのはmasudaだけであり、モジュールはそのカーネルと1対1でなければならないため、注入する側をmasudaに寄せた。
- **必要なカーネル機能を`=y`で持つカーネルを自前ビルドする**: モジュールツリーの注入自体が不要になるが、masudaは`scripts/setup-vm-host.sh`でホストのディストリカーネルをそのまま使うことでカーネルの保守を負わない設計を採っている。どのワークロードがどの機能を要求するかを事前に列挙できない以上、ビルド設定の維持そのものが継続的な負債になるため却下。
- **宣言をJSON（データ）ではなくTypeScript等の実行可能コードで表現する**: `.masuda/settings.json`は対象リポジトリがコミットする、信頼していない側が書けるファイルである（Issue #19）。宣言を読み込む処理自体がコードの実行を要する形式にすると、承認ゲートを通す前にホスト上で対象リポジトリ由来のコードを実行してしまうことになり、この設計全体が依存する「宣言と承認の分離」を宣言を読む最初の一歩で破壊する。表現力の不足が理由ではなく、環境・実行・期待する出力のいずれも「手続きではなく参照・データ」としてJSONで表現しきれると判断し、コード形式は採用しなかった。
- **承認ハッシュを宣言だけに固定し、イメージの中身は対象にしない**: 実装は単純になるが、承認後にイメージディレクトリを差し替えれば承認を受けていないものが特権付きで走る。承認が実際の実行内容を覆えていない状態になるため却下。
- **スナップショット全体の差分をメインVM側のworktreeへ反映する**: 特権コマンドがコードを変更する用途（formatter・codegen）まで扱えるが、スナップショット取得後にメインVMのAIセッションが同じファイルを触っている可能性があり、上書きは作業を失う経路になる。宣言されたパスを別ディレクトリへ回収する形なら、この衝突問題自体が発生しない。
- **回収先をワークスペースのworktree内（`/workspace`配下）にする**: AIから見て成果物が作業ツリーの中にある方が自然だが、フェーズ4/5のステップcommitが`git add -A`で回収物をブランチ履歴へ混入させる。状態ディレクトリ側に置けばこの経路が塞がる。
- **実行ごとのディレクトリ名を実行内容のダイジェストにする**: 同一の宣言・同一の状態での再実行が同じディレクトリに落ち、2回目が1回目を上書きする。実行を区別することが目的であるため乱数を採った。
- **回収した結果を直近N件だけ残して自動削除する**: ディスク使用量が有界になるが、「さっき見たディレクトリが消えている」という挙動をAIと人間の両方に見せることになる。ワークスペースの寿命自体が有界（`review approve`で削除される）であるため、自動削除を持たない方を選んだ。

## Consequences

- 使い捨てVM化はcloud-hypervisor自体のVMエスケープ脆弱性の発生確率を下げない。下がるのは露出時間とそのVMから盗める資産の量（`/masuda-secrets`・SSH・MCPリレーを共有しないため）であり、脆弱性そのものの深刻度ではない
- 特権操作向けの専用VMイメージを保守する必要があるが、ADR-0054によりメインVMのイメージと同じ機構（`.masuda/images/<name>/`）に載るため、イメージのライフサイクルが2系統に分岐することはない
- 特権VMのrootfsはモジュールツリーのぶん（Ubuntu 24.04の`linux-modules`で約155MB）メインVMより大きくなる。メインVMには従来どおり`virtiofs.ko`のみを注入し、この非対称は維持する——メインVMはそれ以外のモジュールを必要とせず、rootfsは起動のたびに作り直されるため
- `internal/rootfs.Build`の自動サイズ計算はDockerイメージのtarのみを見ており、注入するファイルの容量を数えていない。モジュールツリーのような大きな注入物が現状の余白（実サイズの20%＋固定マージン）に収まるかは偶然に依存するため、注入分をサイズ計算に含める必要がある
- 実行結果はワークスペースの寿命の間、実行回数分だけ状態ディレクトリに積もる。1実行あたりの回収サイズには上限があるが、総量には上限がない
- 回収の上限を超えた場合は結果がエラーになる。成果物の一部だけを受け取って続行する経路は用意しない
- イメージディレクトリの内容を変更すると承認が失効する（ADR-0054のConsequencesにも記載）。Dockerfileを直すたびに再承認が必要になる
- `run_privileged_command`が実際にどのコマンドをどんな出力で成功と判定するかはAI自身のログ解釈に委ねる。masuda側は構造化された成否判定ロジックを持たない
