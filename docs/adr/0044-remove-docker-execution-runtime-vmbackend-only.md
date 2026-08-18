# ADR-0044: Dockerの実行基盤（コンテナ起動）を完全に削除し、サンドボックスはVMBackendのみにする

## Status

Accepted (2026-08-18)

## Context

Issue #31フェーズBで、サンドボックスの実行基盤をDockerからCloud Hypervisor microVM（`internal/sandbox.VMBackend`）へ段階的に移行してきた（M1〜M5-6、それぞれ実機検証込みでコミット済み）。`internal/sandbox.Backend`インターフェース導入（M1）以降、`DockerBackend`と`VMBackend`は並存しており、`cmd/masuda`の各サブコマンドは引き続き旧来のパッケージレベル関数（`sandbox.Start`/`Stop`/`IsRunning`/`AttachArgs`、実体は`DockerBackend`相当）を直接呼んでいた。

Dockerを恒久的な選択可能バックエンドとして残すか、VMBackend安定後に削除するかは当初から論点があり（`internal/sandbox/backend.go`のdocコメント、および開発メモリ`project_masuda_phaseb_vm_migration`）、Issue #35の議論時点でユーザーから「今回の対応を1とし、その後サンドボックスをMicroVMに置き換える、という流れにすればDocker対応は不要」という完全置き換えの意向が示され、2026-08-18のセッションで改めて確認し確定していた。VMBackendがStart/Stop/IsRunning/AttachArgsの全メソッドで実機検証済み（M5-6完了）になったことを受け、ユーザーから明示的に削除の指示があった。

## Decision

Dockerを「コンテナとして実際に起動・アタッチ・停止する」実行基盤としては完全に削除し、`VMBackend`のみを唯一の`Backend`実装とする。

- `internal/sandbox/sandbox.go`からDocker専用実装（`Start`/`Stop`/`IsRunning`/`AttachArgs`/`hostCredentialMounts`/`gitIdentityEnvArgs`/`writeEmbeddedClaudeMd`/`runningHostPort`/`runDocker`/`ContainerName`）を削除。`Handle`型・`DefaultImage`定数・`tmuxSession`定数・`nameSanitizer`・`freePort`はVMBackend側も使う共有基盤として残す
- `internal/sandbox/backend.go`から`DockerBackend`型を削除。`Backend`インターフェース自体は維持
- `internal/sandbox/backend_test.go`（Docker統合テスト）を削除
- `cmd/masuda/{sandbox,chat,gate,review,workspace,update}.go`の全呼び出し元を、`cmd/masuda/sandbox.go`に新設した`var sandboxBackend sandbox.Backend = sandbox.VMBackend{}`経由に統一

**`docker build`/`docker export`（`internal/rootfs.Build`）は削除しない**。これはサンドボックスVMのrootfsを作るための変換元イメージビルドであり、「コンテナとして実行する」こととは別の関心事——VMのrootfsは`docker build`で作ったイメージの中身を`docker export`で取り出し、fakeroot経由でext4イメージに変換したものであって、そのDockerイメージ自体が動くわけではない。`Dockerfile`・`docker/{go,python,typescript,full}/Dockerfile`もrootfsの元イメージ定義として維持する。

### 削除の過程で見つかった2つの未実装ギャップ

Docker版の`Start()`がコンテナ起動時に暗黙に行っていた処理のうち、`VMBackend`に移植されていなかったものが2つ見つかり、削除前に埋めた。

1. **git identity受け渡し**: Docker版は`docker create -e GIT_AUTHOR_NAME=... -e GIT_AUTHOR_EMAIL=...`でコンテナに環境変数として渡していた（Build段階の各ステップコミットに必要）。VM版は`internal/sandbox/gitidentity.go`の`WriteGitIdentity(stateDir, repoRoot)`が、既存の`/masuda-state`virtiofs共有に相乗りする形で`stateDir/.masuda-git-identity`（1行目name、2行目email、シェルクォーティングの複雑さを避けるためプレーンな2行フォーマット）を書き込み、`runtime/entrypoint.sh`/`start_claude.sh`が`sed`で読んで`export`する。新規のvirtiofs共有は追加していない
2. **`~/.claude/CLAUDE.md`（作業ループ仕様）の注入**: Docker版は`docker create`→`docker cp`→`docker start`の順序で、コンテナ起動直後・entrypoint実行前にCLAUDE.mdを注入していた（ADR-0007、イメージに焼き込まずコンテナ起動時に動的注入する設計）。VM版はrootfsが`vmStart`のたびに毎回ゼロから再構築される設計（Dockerイメージのような「一度ビルドしたら使い回す」性質を持たない）ため、ADR-0007が回避しようとした「イメージ再ビルドの手間」がそもそも発生しない。既存の`internal/rootfs.Build`の`ExtraFile`機構（SSH公開鍵・カーネルモジュールと同じパターン）でrootfsビルド時に焼き込む形にした

両方とも実機で動作確認済み（スペースを含む名前の受け渡し、`/home/ubuntu/.claude/CLAUDE.md`のゲスト到達を確認）。

## Alternatives Considered

- **Dockerを恒久的な選択可能バックエンドとして残す（`.masuda/settings.json`にbackend選択フィールドを追加）**: Issue #35時点でユーザーが明示的に却下した方針。2つの実行基盤を並行して保守するコスト（本ADRで見つかった2つのギャップのような差分が今後も発生し続ける）に見合う利用シーンが無いと判断された
- **Dockerに完全に非依存化する（`docker build`/`docker export`によるrootfs抽出も廃止し、debootstrap等で直接rootfsを作る）**: 検討したが、実装コストが非常に大きい（masuda自身のDockerfile群がLSPプラグイン・言語ツールチェーンの構成管理を担っており、これを別の仕組みで代替する意味が薄い）。「コンテナとして実行する」ことと「イメージとしてファイルシステムを構成管理する」ことは別の関心事であり、後者にDockerを使い続けることが実行基盤の選択（VM）を制約しないため不採用
- **git identityをカーネルコマンドライン（`--cmdline`）経由で渡す**: `masuda.mcp_relay=`と同じパターンだが、値にスペースが含まれる場合のクォーティングが複雑になる（Linuxカーネルのcmdlineパーサーの引用符処理に依存させたくない）ため、既存のvirtiofs共有（`/masuda-state`）に相乗りする方式を選んだ
- **CLAUDE.mdをvirtiofs経由（`/masuda-state`等）で渡し、entrypoint.sh側でコピーする**: rootfsの再ビルドなしにmasuda CLIの更新だけで内容を反映できる利点はあるが、VMのrootfsはワークスペースのStartごとに毎回作り直される設計のため、この利点の価値が小さい。既存のExtraFile機構をそのまま使う方が実装コストが低いため不採用

## Consequences

- `.masuda/settings.json`の`image`フィールドの意味が「Dockerで実行するイメージ」から「サンドボックスVMのrootfs変換元イメージ」に一本化された。`--image`フラグのヘルプ文言も合わせて更新した
- `masuda-net-helper`（TAP管理用のsetcap済みヘルパーバイナリ）を含むVM実行基盤の前提条件（Cloud Hypervisor・virtiofsd・`scripts/setup-vm-host.sh`等）は、もはや「Issue #31の移行作業に参加する開発者だけが必要とするもの」ではなく「masudaを使う全利用者が必要とするもの」になった。`docs/CONTRIBUTING.md`にあった該当節は`docs/INSTALLATION.md`へ統合し、`CONTRIBUTING.md`側にはmasuda自身のGoコードを変更する開発者だけが意識すべき注意点のみを残した
- ホストのClaude Code認証情報（`~/.claude/.credentials.json`・`~/.claude.json`）のbind mountは廃止され、`claude setup-token`の長期OAuthトークン（`masuda internal claude-token set`で登録）に一本化された。これは元々M5-6でVM対応のために導入したものだが、本ADRによりDocker版の代替手段としての位置づけが確定した
- `internal/sandbox`パッケージのテストスイート実行時間が短縮された（Docker統合テスト`TestDockerBackendLifecycle`の削除により、約12秒短縮）
