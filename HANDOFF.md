# HANDOFF
## 作業項目
M1（旧コードの削除と骨組み）完了。コミット: `283bb6f`（削除と流用部分の写し）、`6b5729b`（buf generate・go.mod・`package serve`・`cmd/masuda`）、`5411913`（README・INSTALLATION・CONTRIBUTING）
- 残したもの: `docs/`、`scripts/gh.sh`、`proto/`、`buf.*`、`CLAUDE.md`、`HANDOFF.md`、`.gitignore`、`.claude/`、`contract/`
- 写したもの: `internal/staging`（旧`internal/worktree`の`clone --bare --local`と`FastForward`だけ）、`internal/perspectives`（14観点を`go:embed`、`Builtin()`）、`internal/config`（宣言/承認の形: `DeclHash`・`Load`・`LoadLocal`・`SaveLocal`。MCPサーバー宣言・images・Baseは削除）
- `gen/`: masuda.protoとsandbox.proto（`../masuda-sandbox/proto`から。`buf.gen.yaml`のMオプションで`gen/masuda/sandbox/v1;sandboxv1`へ）
- `serve`: `Start`/`Stop`/`Done`。全6サービスを登録し、`WorkspaceService.List`（空）・`Get`（NotFound）以外はUnimplemented。`Options.FakeSandbox`・`SandboxSocket`は受け取るだけでまだ使っていない
- `cmd/masuda`: `serve`（`--socket`・`--data-dir`・`--fake-sandbox`・`--sandbox-socket`）、`version`。標準`flag`で、cobraは入れていない
## 完了した契約テスト
C-M1（`go test ./contract/ -run TestCM1`が緑。`go build ./...`・`go vet ./...`も通る）。C-M2〜C-M7は想定どおり赤
## 未完と理由
- ローカルの追跡外ファイル`venv/`・`__pycache__/`・`masuda`（旧バイナリ）は残っている。`rm -rf`が自動承認されなかったため。ユーザーが消す。消したら`.gitignore`のPython関連の行も落とせる
- `CLAUDE.md`の「開発環境」がまだ旧設計（venv・pytest・rootfsビルド・`orchestrator/`/`runtime/`）を書いている。M1の範囲外なので触っていない
- `.claude/skills/`（adr-author・doc-placement）は削除済みの`docs/adr/`を前提にしている。残す指示どおり触っていない
## 次の一手
`docs/work-orders.md`のM2
## 注意点
- 契約ファイルは変えない
- `go.mod`の`masuda-engine`のrequireはまだimportが無いので`go mod tidy`で消える。tidyした後は手で戻す（M4でimportすれば不要になる）
- `golang.org/x/net` v0.59.0では`http2/h2c`がDeprecated（`http.Server.Protocols`で`SetUnencryptedHTTP2(true)`が推奨）。作業指示どおりh2cを使っている。置き換えるなら`serve.Start`の1か所
- 旧`config.PrivilegedCommandHash`（承認を宣言だけでなくイメージの中身のダイジェストにも結びつける）はimages.goと一緒に消した。M7で同じ考え方が要る
- 作業中、`docs/design/contracts.md`にこのセッション以外からの未コミットの変更（TypeScriptの生成プラグインの記述）が入っていた。触らず、コミットにも含めていない
## 契約への提案
なし
