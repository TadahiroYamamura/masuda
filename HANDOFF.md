# HANDOFF
## 作業項目
**M13（配布、v0.1.0に向けて）**。work-ordersのM13の箇条書きすべて（E11後の追従を含む。engineのE11は4b0191aでpush済み）。
- `ec36433` engineの固定: `go.mod`の`replace`を外し、`require github.com/TadahiroYamamura/masuda-engine v0.0.0-20261002130243-3f6f64eec8aa`（当時のpush済みmain）。隣の参照はgitignoreした`go.work`（`go work init . && go work use ../masuda-engine`）で、手順は`CLAUDE.md`と`design/README.md`。`aa1983e`で`v0.0.0-20261002133017-4b0191ad7e82`（E11を含むmain）へ上げた
- `ac30047` 互換性の確認・version・doctor: `internal/sandboxcontract/sha.go`（`go generate ./internal/sandboxcontract/`、`gen.go`が`../masuda-sandbox`の`sandbox.proto`のSHA-256を書く。`MASUDA_SANDBOX_DIR`で場所を変えられる）。`serve.SandboxInfo`・`CheckContract`。serveは起動時に確かめ、契約違い・GetServerInfo無し（Unimplemented）なら起動しない、届かないだけなら警告して起動。Run・Resumeはワークスペースを作る前に確かめ、違えば`FailedPrecondition`（両方のバージョンを理由に）、届かなければ`Unavailable`。フェイクsandboxはGetServerInfoでmasudaの値を返す。`masuda version`（masudaの版・Goの版・SHA-256、届けばsandboxのServerInfoと`contract: ok/MISMATCH`）、`masuda doctor`（config.json・git・docker・node・qemu・/dev/kvmまたはHVF・sandboxの到達と契約・Claudeトークン。NGがあれば終了コード1、warnだけなら0）。実物の`masuda-sandbox serve`（dist/cli.js、dev）に対してversion・doctorが`contract: ok`になることを手元で確かめた
- `a311081` `guest.BaseEnv`と`DefaultPath`を削除（PATHだけでなくXDG_*も。sandboxが同じ値を保証し、フェイクではホストの`/home/ubuntu`を指していた）。フェイクsandboxの`execEnv`はsandboxの`execBaseEnv`と同じ順（HOME・XDG・`$HOME/.local/bin:`ホストPATH→CreateSandboxのenv→先頭に`.local/bin`を足し直す→Execのenv）。雛形Dockerfileのコメントとトラブルシューティングを実態に
- `aa1983e` E11後の追従: step-diffのゲートの`staging_commit`は**作業ツリーのスナップショット**（`Runner.Diff(step-diff)`が取り込んだもの。親がブランチ先頭、`refs/masuda/gates/<occ>`に留める）。findingsはそのコミットへのコメントとして取り込む（interimの行番号は作業ツリー基準なので一致する）。`gate show`はtargetの行と差分の見出しで「publishされる内容（コミット済み）」と「このステップでこれからコミットされる内容（未コミット）」を区別。`concepts.md`のinterim、`api/flows.md`のゲートの節、`api/services.md`、`workflows.md`、`overview.md`を合わせた
- `c19e79f` `.github/workflows/ci.yml`・`release.yml`、`docs/design/release.md`（mkdocsのnavに追加）、`docs/user/install.md`をReleaseのtarball中心に書き直し、ソースからの手順は`design/README.md#source`へ
## 完了した契約テスト
C-M1〜C-M8すべて緑（`go test -count=1 -v ./contract/`）。`go build ./... && go vet ./...`指摘なし（`GOOS=darwin GOARCH=arm64 go vet ./...`も通る）、`go test -count=1 ./...`全部緑（liveはskip）。`scripts/docs-prepare.sh && mkdocs build --strict`は通る（INFOは既知の`#google-protobuf-Timestamp`1件だけ）。`actionlint`は3つのワークフローとも指摘なし（shellcheckは手元に無いので未実行）。契約ファイル（両proto、`docs/guest-protocol.md`）と契約テストは変更していない
## 未完と理由
- 実VMでの確認はしていない（liveは監督が実行）。今回の変更でliveで見るべきもの: (1) BaseEnvを外した後もチェックとメインセッションのPATHにclaude・イメージのツールが見えること、(2) interimゲートの`gate show`の見出しと、`StagingService.Diff(to: staging_commit)`がSubjectと一致すること、(3) serveの起動時とRunの前の契約確認が実物のsandboxで通ること
- ワークフロー（ci・release）は実際には走らせていない。最初のpushとタグで確かめる
- `go get ...@main`はモジュールプロキシのキャッシュで古いコミット（3f6f64e）を返した。コミットハッシュ指定（`@4b0191a`）で取った。リリース手順書にはタグでは`GOPROXY=direct`を試すよう書いた
## 次の一手
1. 監督: liveテスト（上の3点を含む）。サンドボックスイメージの再ビルドは不要（Go側のみ）
2. ユーザー: `redesign`をpushしてActionsの`ci`が緑になることを見る（このときmasuda-sandboxの`main`との契約の一致も確かめられる）
3. ユーザー: `docs/design/release.md`に沿ってv0.1.0（初回は手順2の`redesign`→`develop`と`v1-frozen-*`のpush、masudaに`main`が無いので作る）
4. 実機で`docs/user/quickstart.md`を頭から通し、出力例を実物に差し替える（M12から持ち越し）
## 注意点
- `go.work`があるとテストは隣の`../masuda-engine`の作業ツリー（未コミットを含む）で走る。固定した版で確かめるときは`GOWORK=off`
- `buf generate`でsandboxのクライアントを作り直したら、必ず`go generate ./internal/sandboxcontract/`も（CIが差分で落とす）
- Run・Resumeでsandboxに届かないとき、以前はワークスペースを作ってからBLOCKED（`sandbox boot failed`）になっていたが、今は`Unavailable`を返してワークスペースを作らない（contracts.mdのコードの約束どおり）
- `CheckScript`のログインシェル（`#!/bin/sh -el`）はそのまま。`/etc/profile`がPATHを置き換えるイメージ（Debian系）ではイメージのENVのPATHが消える（雛形とトラブルシューティングに書いた）。外すかはliveの結果を見て判断
- findingsは累積データなので、interim・reviewのどちらのゲートでも以前のステップの指摘が同じコミットに取り込まれる（行番号がずれうる）
- `docs.yml`はタグのときもmasuda-engineの既定ブランチから`workflow-schema.md`を取る。リリース時点でengineのmainの先頭がタグと同じなら問題ない（手順書に書いた）。タグに揃えるなら`docs.yml`のengineのcheckoutに`ref`を足す（今回は既存のワークフローなので触っていない）
- `doctor`はqemu-img・lz4の欠けをwarnにした（Gondolinに必須かをmasuda側で断定できないため）。必須ならfailに
- リポジトリには既にタグ`v0.0.1`（origin）がある
## 契約への提案
なし。step-diffのゲートの`staging_commit`を作業ツリーのスナップショットにしたのは、protoの「the staging commit / tree the diff was computed from」の範囲で表せると判断したため。protoのコメントは「For target=diff」だけなので、次に契約を開くときに「target=diff・step-diff」と書き足すのが望ましい
