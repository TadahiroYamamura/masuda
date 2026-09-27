# ADR-0083: サンドボックスのVM層は既存のmicroVMツールを採用せず自前で保守し、masudaに依存しない`internal/microvm`として分離する

## Status

Accepted (2026-09-27)

## Context

[[0044-remove-docker-execution-runtime-vmbackend-only]]でサンドボックスの実行基盤はCloud Hypervisor microVMだけになった。その実装である`internal/sandbox`は、VMそのものの起動（Dockerイメージからのrootfs構築、TAPデバイス、virtiofsd、SSH鍵、DHCPリースからのゲストIP解決、pidfileによるプロセス管理）と、masuda固有の準備（`.masuda/`のイメージエントリと許可リストの解決、Claude OAuthトークンの共有、git identity、MCPリレー、特権コマンドの承認）を1つのパッケージに抱えていた。`vmStart`は`internal/config`・`internal/workspace`・`internal/statedaemon`を直接読み、ブリッジ名やゲストユーザーもハードコードされていた。使い捨て特権VM（[[0053-privileged-commands-declared-and-run-in-disposable-vm]]）の`bootPrivilegedVM`は、cloud-hypervisorの引数組み立てとロールバックを`vmStart`と重複して持っていた。

2026-09-25、この部分を汎用ツールとして切り出せないかという相談から、既存のローカルmicroVMツールの前例を調べた。AIエージェント向けのmicroVMサンドボックスは2025〜2026年に急増しており、masudaの要件——(1)クライアントが接続していなくても何時間でも動き続けること、(2)worktreeを読み書き両方向にライブで共有できること、(3)TLSを終端せずホスト名単位で外向き通信を制限できること、(4)WSL2で動くこと——に照らして5つをソースまで読んで比較した（実機では動かしていない）。4つを満たしうるのはmicrosandboxだけで、他はいずれかを明確に欠いていた。一方で、切り出して汎用ツールとして出しても、機能・成熟度で上回る既存ツールと同じ位置に並ぶだけで差別化は薄いことも分かった。

## Decision

VM層は自前で保守し続ける。そのうえで、masudaを知らない層を`internal/microvm`として分離し、`internal/sandbox`をmasuda固有の準備だけを持つ層にする。

- **`internal/microvm`**: `Host`（データディレクトリ・ブリッジ名・DHCPリースファイル・net-helperのバイナリ名・ブリッジが無いときの案内文・SSH鍵のコメント・ゲストユーザー）と`Spec`（ID・Dockerイメージ参照・rootfsへの注入物・カーネルモジュールの範囲・virtiofs共有のタグとホストディレクトリ・追加のカーネル引数・シリアルログの出力先）を受け取り、`Start`（常駐VM）・`Run`（使い捨てVM、電源断まで待つ）・`Shutdown`・`Remove`・`IsRunning`・`AttachArgs`・`Check`を提供する。停止は`Shutdown`（正常停止→強制終了→virtiofsd停止→TAP解放）と`Remove`（作業ディレクトリ削除）の2段に分け、呼び出し側が間に自分の後始末を挟めるようにした。依存してよいmasudaのパッケージは`internal/rootfs`だけとし、`TestNoMasudaImports`が`go list -deps`でこれを検査する
- **`internal/sandbox`**: `vmHost()`がmasudaのホスト設定を`microvm.Host`として組み立て、`vmStart`・`RunPrivilegedCommand`が共有・注入物・カーネル引数を`microvm.Spec`に詰めて呼ぶ。`Backend`インターフェースと`cmd/`配下の呼び出しは変えない

置き場所は`pkg/`ではなく`internal/`にした。今は外部に公開しないため、公開APIとしての互換性を約束する理由がない。

## Alternatives Considered

- **microsandbox（libkrun、Rust）を実行基盤として採用する**: 4要件をドキュメントとソースの上では満たし、ホスト名の検証もDNSで引いたIPとの突き合わせ付きでTLSを終端せずに行うなど、設計はmasudaより進んでいる部分もあった。しかしWSL2は公式サポート外で、masudaと同じカーネル（6.6.114.1-microsoft-standard-WSL2）で`/dev/kvm`へのアクセスが失われてSIGABRTする報告（superradcompany/microsandbox#920）に対し、メンテナは「WSLは優先しない」と回答している。WSL2はmasudaの主な動作環境であり、そこで死活的な不具合が出ても上流で直らない可能性が高い依存は持てない
- **matchlock（Firecracker）を採用する**: 無人稼働とクレデンシャル分離は合うが、443番を常にTLS終端してCAをゲストに注入する方式で、ホスト共有もFUSE over vsock（属性キャッシュ1秒）。実質1人の開発で、自ら実験段階と明記している
- **agent-vm（microsandboxのfork）を採用する**: VMの寿命がCLIプロセスに縛られ、デタッチ運用が未実装。外向き通信はIPの種別で絞るだけでドメインの許可リストが無い
- **Celesto（旧CelestoAI/SmolVM）を採用する**: 共有は9pかつQEMUバックエンドのみ。`allowed_domains`は作成時に一度IPへ解決するだけで、公式ドキュメント自身が厳密なドメイン制限ではないとしている。`~/.ssh`や`~/.git-credentials`をゲストへコピーする設計で、エージェントに秘密情報を読ませない方針と相容れない
- **arrakis（Cloud Hypervisor）を採用する**: 2025-06以降更新が無く、ホスト共有も外向き通信の制限も無い
- **切り出して別リポジトリの汎用ツールとして公開する**: 上の比較のとおり既存ツールとの差が薄く、公開ツールとしての保守を引き受ける理由が無い。分離を`internal/`に留めたのはこのため
- **分離せずに今の`internal/sandbox`のまま保守する**: 自前保守という点では同じだが、既存ツールから取り入れたい改善（ホスト側プロキシでの認証情報の差し替え、ホスト名とDNS解決結果の照合など）を入れるたびに、masuda固有の設定読み込みとVM起動が絡んだ関数に手を入れることになる。常駐VMと使い捨てVMで起動処理が重複している状態も残る
- **停止処理に呼び出し側のフックを差し込めるようにする（`Shutdown`/`Remove`に分けない）**: MCPリレーのpidfileがVMの作業ディレクトリにあるため、masudaは「VM停止→リレー停止→作業ディレクトリ削除」の順を必要とする。フックでも実現できるが、2つの関数を順に呼ぶほうが制御の流れが呼び出し側から読める

## Consequences

- WSL2やCloud Hypervisor・virtiofsdで起きる不具合は、上流に頼れず自分たちで直すことになる。既存ツールが先に解いた問題（ホスト名の偽装対策、認証情報の差し替えなど）は、アイデアとして取り込み続ける必要がある
- `internal/microvm`はmasudaの設定もワークスペースも知らないため、masuda固有の値はすべて`Host`か`Spec`で渡す。ホスト設定の値（`br-masuda0`など）は`internal/sandbox`の`vmHost()`に集まったが、設定ファイルからは変えられないままである
- 停止処理は起動時の`Spec`を持たないため、virtiofsdは作業ディレクトリの`virtiofs-*.sock.pid`を探して止める。共有のソケット名が`virtiofs-<tag>.sock`に統一された結果、`masuda-state`共有のソケットは`virtiofs-state.sock`から`virtiofs-masuda-state.sock`に変わったが、探して止める方式のため変更前に起動したVMも止められる
- `Run`は作業ディレクトリを消さず、削除は呼び出し側の`Remove`に任せる。使い捨てVMの成果物はVM停止後にワークスペースのスナップショットから回収するため、`Run`が消すと回収前にスナップショットが失われる
- 常駐VMではMCPリレーをrootfsのビルドより前に起動するようになった。リレーのアドレスを`Spec.KernelArgs`に載せる必要があるためで、rootfsのビルドが失敗した場合もリレーは`vmStart`が止める
- 将来`internal/microvm`を外に出す場合は、`internal/rootfs`と`cmd/masuda-net-helper`も一緒に移す必要がある。`rootfs`のラベル（`masuda-rootfs`）とnet-helperのマーカー（`masuda-managed-tap`）にはmasudaの名前が残っている
