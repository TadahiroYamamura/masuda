# 設計ドキュメント

masudaが**現在**どう動いているかを記述する。「なぜそうなったか」は書かない——判断の経緯は `docs/adr/`（索引は [`../adr/README.md`](../adr/README.md)）にあり、ここからは `（ADR-NNNN）` のポインターだけを張る。

全体像から入るなら [`pipeline.md`](pipeline.md)。

## 触る対象から引く

| 触る対象 | 読むファイル |
|---|---|
| `orchestrator/investigate_plan_graph.py`、`internal/hostloop/` | [discovery-blueprint.md](discovery-blueprint.md) |
| `orchestrator/implement_review_graph.py` のステップ実行・TDD・バックストップ | [build.md](build.md) |
| 同ファイルの観点レビュー・横断的チェック・synthesize、`internal/perspectives/`、`internal/hunkcontext/` | [review.md](review.md) |
| `internal/gate/`、`cmd/masuda/gate.go`、`cmd/masuda/triage.go`、`runtime/CLAUDE.md` | [gates.md](gates.md) |
| `internal/workspace/`、`internal/worktree/`、`cmd/masuda/workspace.go` | [workspace.md](workspace.md) |
| `internal/config/`、`runtime/merge_claude_settings.py` | [config.md](config.md) |
| `internal/sandbox/` のVM起動・virtiofs・SSH鍵・認証、`runtime/entrypoint.sh` | [sandbox-vm.md](sandbox-vm.md) |
| `internal/sandbox/vmnet.go`、`cmd/masuda-net-helper/`、`scripts/setup-vm-host.sh` | [networking.md](networking.md) |
| `internal/egressproxy/`、`cmd/masuda-egress-proxy/`、`internal/sandbox/egressproxy.go`、`cmd/masuda/egress.go` | [egress-filter.md](egress-filter.md) |
| `Dockerfile`、`docker/*/Dockerfile`、`internal/rootfs/` | [images-and-rootfs.md](images-and-rootfs.md) |
| `internal/statedaemon/`（KVストア・MCP 2面） | [state-daemon-mcp.md](state-daemon-mcp.md) |
| `internal/statedaemon/mcpaggregator/`、`cmd/masuda/mcp.go` | [mcp-child-servers.md](mcp-child-servers.md) |
| `internal/selfupdate/`、`internal/verify/`、`cmd/masuda/update.go`・`init.go`、`.github/workflows/` | [distribution-and-update.md](distribution-and-update.md) |
| `cmd/masuda/main.go`、サブコマンドの一覧と呼び出し順 | [cli.md](cli.md) |
| 段階をまたぐデータフロー、予算管理 | [pipeline.md](pipeline.md) |

## このディレクトリの外にあるもの

- **用語の定義**（段階名・ゲート名・エスカレーション区分と旧称の対応）: [`../glossary.md`](../glossary.md)
- **セットアップ手順**（何を実行するか）: [`../INSTALLATION.md`](../INSTALLATION.md)。ここが書くのは仕組みだけで、手順は繰り返さない
- **masuda自身の開発手順**: [`../CONTRIBUTING.md`](../CONTRIBUTING.md)
- **判断の経緯・却下した代替案**: [`../adr/README.md`](../adr/README.md)

## 既知の問題

未修正のまま把握している問題は、該当する機構のドキュメント末尾に `## 既知の問題` として置いてある。一覧するには次を実行する。

```bash
grep -rn -A4 '^## 既知の問題' docs/design/
```

## 書くときの決まり

- **現在形だけで書く。** 「当初は」「〜という理由で」「〜ではなく〜を採用した」が出てきたら、それはADRの内容。`doc-placement` skill を使う
- **1ファイル1関心事。** 他ファイルの担当範囲は書かず、参照1行で済ませる。同じ説明が2箇所にあると必ず片方が古くなる
- **コードが正。** 既存の記述と実装が食い違っていたら実装を正とし、ドキュメントを直す

## 書き終えたら

```bash
# 相互参照がすべて実在ファイルを指すか（左の出力が右にすべて含まれること）
grep -ohE 'docs/design/[a-z-]+\.md' docs/design/*.md CLAUDE.md | sort -u
ls docs/design/

# 経緯が混入していないか（ヒットしたら doc-placement skill へ）
# このREADME自身は禁止表現を説明のために書いているので除外する
grep -n '当初は\|という理由で\|ではなく.*を採用した\|検討した結果\|実機で判明' docs/design/*.md | grep -v '^docs/design/README.md:'
```

行番号（`ファイル名:123`）を引用した場合は、その行が今も該当箇所を指すか確認すること。隣接する変更で簡単にずれる——`Dockerfile`に3行足されただけで3箇所がずれた実績がある。
