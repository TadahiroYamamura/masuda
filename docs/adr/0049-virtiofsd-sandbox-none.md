# ADR-0049: virtiofsdを`--sandbox=none`で起動し、`newuidmap`/`newgidmap`をホスト前提条件に加えない

## Status

Accepted (2026-08-19)

## Context

本ADRは、Issue #31（VM移行）のM3〜M5作業中に下された判断を、`internal/sandbox/virtiofs.go`の`StartVirtiofs`docコメント（当時この判断を記録していた唯一の場所）から再構成したものである。私（本ADRの起票を依頼した人物）はこの判断の当事者ではない。

[[0044-remove-docker-execution-runtime-vmbackend-only]]の通り、masudaのサンドボックスはCloud Hypervisor microVM（`VMBackend`）に一本化されている。ホスト上のディレクトリをVMゲストへ共有する仕組みとしてvirtiofsdを採用しており、`StartVirtiofs`（`internal/sandbox/virtiofs.go:47`）が`vmStart`（`internal/sandbox/vmbackend.go`）から呼ばれ、以下を1プロセス1vhost-userソケットで共有する。

- `/workspace`（`worktreeDir`。対象リポジトリのgit worktree本体）
- `/masuda-state`（`stateDir`。masudaの状態ディレクトリ。git identityファイル`.masuda-git-identity`を含む）
- `/masuda-secrets`（`secretsDir`。`claude setup-token`で登録済みの場合のみ。Claude Code長期OAuthトークンファイル`token`、mode 0600、を含む）

virtiofsdはデフォルトで`--sandbox=namespace`を使う。これは`newuidmap`/`newgidmap`（uidmapパッケージ）を必要とするが、この2コマンドはmasudaのホスト前提条件（`docs/INSTALLATION.md`「VM実行基盤のセットアップ」節、`scripts/setup-vm-host.sh`の`require_cmd`）に含まれていない。`require_cmd`が確認するのは`sudo`・`ip`・`iptables`・`go`・`cloud-hypervisor`・`virtiofsd`のみである。

## Decision

`StartVirtiofs`が起動するvirtiofsdのコマンドラインに`--sandbox=none`を渡す（`internal/sandbox/virtiofs.go:64-68`）。

```go
cmd := exec.Command(virtiofsdBinary,
    "--socket-path="+socketPath,
    "--shared-dir="+dir,
    "--sandbox=none",
)
```

virtiofsdの起動自体は`startBackgroundProcess`（`internal/sandbox/bgprocess.go`）を経由するが、これはpidfileベースの起動/停止/孤児回収のみを行い、setuid/setcapのような権限変更は一切行わない。したがってvirtiofsdは、TAPデバイス管理（`internal/sandbox/vmnet.go`、`masuda-net-helper`経由でsetcapされた権限を要する）とは異なり、`vmStart`を実行しているホストユーザーのuid/gidのまま、追加の権限昇格・降格なしに動く（`virtiofs.go:10-13`のコメント参照）。

`--sandbox=none`により、virtiofsdは上記3つの共有ディレクトリへ、`--sandbox=namespace`が行うマウント名前空間ベースの追加隔離（およびそれに伴う`newuidmap`/`newgidmap`によるuid/gidマッピング）なしにアクセスする。結果として`newuidmap`/`newgidmap`はmasudaのホスト前提条件に加えていない。

## Alternatives Considered

- **`--sandbox=namespace`（virtiofsdのデフォルト）を使い、`newuidmap`/`newgidmap`を新たなホスト前提条件として`docs/INSTALLATION.md`に追加する**: コメントに記録されている唯一の代替案。Issue #31 M3〜M5のスパイク時点ではこの追加のホスト依存を課さない判断がされ、`--sandbox=none`が採用された。コメントには、この代替案が具体的にどのような理由で退けられたか（インストール手順の複雑化、対象環境での`newuidmap`/`newgidmap`可用性の懸念、等）までは記録されておらず、それ以上の推測は行わない。

## Consequences

- virtiofsdは、`/workspace`（対象リポジトリのworktree全体）・`/masuda-state`（git identityを含む状態ディレクトリ）・`/masuda-secrets`（登録時はClaude OAuthトークンを含む）の3ディレクトリに対し、`--sandbox=namespace`が提供するマウント名前空間ベースの追加隔離なしに、ホストユーザーの通常の権限のままアクセスする。virtiofsd自体に脆弱性があった場合、この追加隔離層が無いことで、ホストユーザーの権限範囲内での影響がより広くなりうる。
- **これはコメント自身が明言する通り、最終的なセキュリティ姿勢ではなく、既知の・意図的な暫定的妥協である**。Issue #11（サンドボックスのネットワーク/隔離強化、Issue #31のM1〜M6完了直後に着手予定）で見直される前提であり、本ADRの決定はその見直しまでの間だけ有効なものとして扱う。Issue #11の中でこの判断が変更・上書きされる場合は、本ADRを編集せず新しいADRを起票し、本ADRを`[[0048-virtiofsd-sandbox-none]]`として参照させること。
- `newuidmap`/`newgidmap`をホスト前提条件に加えていないため、`docs/INSTALLATION.md`「VM実行基盤のセットアップ」節・`scripts/setup-vm-host.sh`の`require_cmd`はこの2コマンドの存在確認を持たない。これは意図的な現状であり、Issue #11での見直し時に変更されうる。
