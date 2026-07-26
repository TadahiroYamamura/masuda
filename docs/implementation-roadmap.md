# 実装ロードマップ

`docs/design/sandbox-workflow.md`と`docs/adr/`で決めた設計を実装するための大まかな順序。詳細な実装方法（言語内の具体的な構成、エラーハンドリングの粒度等）はコーディングエージェントの判断に委ねる。ここでは「何を」「どの順番で」作るかのみを示す。

## 1. リポジトリ構造の移行

design doc「リポジトリ構造」・ADR-0007参照。

- `runtime/CLAUDE.md`: 旧ルートCLAUDE.md（作業ループ仕様）の原文を複製済み。ADR-0006の`GATE:<name>`終了条件の追加など、内容自体の実装上の変更はまだ未着手
- `runtime/`配下に`entrypoint.sh`、`start_claude.sh`、MCP経由のLSP設定
- `orchestrator/`配下に`langgraph_orchestrator.py`、`perspectives/config.py`
- `cmd/masuda/`: Go製CLIの雛形

## 2. Go CLI（`masuda`コマンド）の骨格

design doc「リポジトリ構造」「ゲート（G1/G2）のUX」、ADR-0005・0006参照。

- worktree管理（作成・ローカルマージ・削除。pushは含めない）
- サンドボックス起動（`docker run`、worktreeのbind mount、`~/.claude/CLAUDE.md`へのruntime/CLAUDE.md配置）
- ゲート操作: `masuda plan/review show|chat|approve|reject`

## 3. LangGraphオーケストレーターを6フェーズの親グラフに再設計

design doc「全体構成」参照。

- フェーズ0（worktree作成）は2番で作ったGo CLI（`masuda workspace create`）を呼び出す
- フェーズ1-2（調査・プラン、ADR-0008: エージェント分離+調査不足時のredo）
- フェーズ4（実装、ADR-0009: ビルド/テスト自己修正ループ、ADR-0010: プラン逸脱検知とG1再オープン）
- フェーズ5は既存の`feat/github-actions-langgraph-nodes`のレビューグラフ（review/checkの往復、13観点）を子グラフとして組み込む。フェーズ4と同一オーケストレーターにまとめ、G2却下時はフェーズ4に差し戻す（ADR-0013）

## 4. レビュー内部の再設計（機械的チェックのみ・完了）

design doc「フェーズ5（レビュー）の内部設計」、ADR-0004参照。

- 機械的チェック: checker/fixerの役割分離、unresolved_idsのフォールバック

当初は横断的チェック（ADR-0003・0011）・予算管理も本ステップに含める想定だったが、
実装時にADR-0003が前提としていた「LSPをMCP経由でセットアップ」がClaude Code自体の
ネイティブLSPプラグイン機構（例: `gopls-lsp@claude-plugins-official`）と置き換わる
可能性が判明し、Dockerサンドボックス内でのプラグイン導入方法に追加調査が必要な
ことが分かった。確度の高い機械的チェックのみを先に完了させ、横断的チェックは
ステップ8に切り出した。

## 5. ゲート機構

ADR-0006参照。

- ゲート到達時にコンテナ/tmuxセッションを終了させず待機させる仕組み（`GATE:<name>`終了条件をCLAUDE.mdのループルールに追加）
- `docker exec -it ... tmux attach`によるchatパス、対話内での承認マーカー書き込み

## 6. レビュー単体エントリーポイント

design doc「レビュー単体での再利用」参照。

- `masuda review start <branch-or-ref> [--base develop]`（design docは`masuda review <branch-or-ref>`と書いているが、既存の`masuda review show|chat|approve|reject`サブコマンド構成に合わせて`start`を追加する形にした）
- フェーズ0〜5の機構を、新規ブランチではなく既存refのworktreeに対して使い回す
- worktreeのパス・サンドボックスのコンテナ名は現状branch名だけをキーにしているため、同じbranchに対してフルパイプライン（`masuda plan/sandbox start`）が並行して動いていると衝突する。今回は「既存のPLAN.mdがある・既にセッションが動いている場合は拒否する」という安全策のみ実装し、根本解決（ワークスペースIDの導入）はステップ7に切り出した

## 7. ワークスペースIDによる並列実行対応（完了）

ステップ6（レビュー単体エントリーポイント）の実装中に見つかった課題。同じbranchに
対して複数の作業（フルパイプラインと`review start`、あるいは同じbranchへの複数の
`plan start`）を並行して走らせたいというニーズがあり、現状のbranch名だけをキーに
したworktree/コンテナのアドレッシングでは衝突する。

- `masuda worktree create`・`masuda plan start`・`masuda review start`は、呼び出す
  たびに一意なワークスペースID（`<branch>-<ランダム短縮文字列>`形式を想定）を発行し、
  標準出力に表示する
- `masuda plan|review show|chat|approve|reject`・`masuda sandbox start|stop`・
  `masuda worktree merge|remove`は、branch名ではなくワークスペースIDを引数に取る
  よう変更する
- 再開（resume）の意味が変わる: 現状`masuda plan start <branch>`（taskを省略）で
  「そのbranchの既存セッションを再開」としていたが、同じbranchに複数のワーク
  スペースがありうる以上、再開は`masuda plan start <workspace-id>`とID明示に変える
  必要がある
- 現在動いているワークスペースの一覧を確認する`masuda worktree list`のような
  コマンドが新たに必要になる
- マージ時に実際にmergeする対象のgitブランチ名はワークスペースIDと独立して変わらない
  （ワークスペースIDはディレクトリ・コンテナ名の一意化のためのものであり、
  git上のbranch名そのものではない）

**追加スコープ（ステップ6実装中に判明）**: masuda自身の制御ファイル（TASK.md・
PLAN.md・INVESTIGATION.md・`.masuda-gate/`・`.masuda-base-ref`・
`.masuda-review-state.json`・`review_results/`等）を、worktree（＝対象リポジトリの
git管理下）の中に置くのをやめ、`~/.local/share/masuda/workspaces/<workspace-id>/`
のような独立したディレクトリに移す。理由:

- worktree内に置いていたことで、`_compute_diff()`がレビュー対象のdiffにmasuda自身の
  ファイルを混入させてしまうバグが実際に発生した（除外リストで対症療法したが、
  そもそも置かなければ発生しない）
- TASK.md等が対象リポジトリの`git status`に常に出現し、対象リポジトリ側の
  `git add -A`等で誤ってコミットされるリスクがある
- ワークスペースIDを導入するなら、そのディレクトリ名自体をこの制御ファイル置き場
  としてそのまま使える（worktreeのパスとは完全に分離する）

この変更に伴う影響:

- Dockerサンドボックスは`/workspace`（worktree）とは別に、この状態ディレクトリ用の
  bind mountがもう一つ必要になる
- `investigate_plan_graph.py`・`implement_review_graph.py`は、相対パス
  （例: `Path("TASK.md")`）でcwd＝worktree前提の実装になっているため、状態
  ディレクトリを指す絶対パスを受け取って使うよう作り直しが必要
- git操作（`git status`・`git diff`・`git add`等）は引き続きworktreeに対して行う
  （cwdの使い分け、または`git -C <worktree>`への統一が必要）

**実装結果**: 上記の通り`internal/workspace`パッケージを新設し、`worktree`/`sandbox`/
`hostloop`・両オーケストレーター・`cmd/masuda`各コマンドをワークスペースID対応に
書き換えた。副次効果として、masuda自身の制御ファイルがworktree外に出たことで
`implement_review_graph.py`の内部ファイル除外ロジック（`_MASUDA_INTERNAL_FILES`等）
と`masuda review start`の衝突拒否安全策が丸ごと不要になり削除できた。

実機テストで、同一branchに対する2つの並行ワークスペースが衝突なくG1ゲートまで
進むこと、成果物が状態ディレクトリ側にのみ生成されることを確認済み。途中で
Claude Codeの許可ルール`Edit(/abs/path)`（先頭スラッシュ1つ）がworktree外の絶対パス
に対して常に確認プロンプトを出し自動承認されない実装上の罠を発見し、
`Edit(//abs/path)`（先頭スラッシュ2つ）に修正して解消した
（[anthropics/claude-code#25137](https://github.com/anthropics/claude-code/issues/25137)、
[#18200](https://github.com/anthropics/claude-code/issues/18200)）。

**コマンド名の見直し（コミット後、レビューで指摘）**: 上記の実装当初は本節の記述通り
`masuda worktree create|merge|remove|list`という名前だったが、「worktree」というgit
用語のコマンドグループの下に、branch名だけでなく状態ディレクトリ・メタデータまで
含む広い概念（workspace）の操作が混在しているのは違和感がある、との指摘を受けて
`masuda workspace create|merge|remove|list`に改名した。`internal/worktree`パッケージ
自体はgitチェックアウトの実装詳細として維持し、CLIコマンド名としては表に出さない
形に整理した。

## 8. 横断的チェック（LSP経由の整合性検証）

design doc「フェーズ5（レビュー）の内部設計」、ADR-0003・0011参照。ステップ4から
切り出し。着手前に以下を調査する必要があった。

- Claude CodeのネイティブLSPプラグイン機構（MCP経由の自前実装ではなく）を使う場合、
  Dockerサンドボックス内でLSPプラグイン（gopls-lsp等）をどう導入するか
  （ビルド時にマーケットプレイス経由でインストールするか、gopls本体のみ入れて
  別の方法でLSP登録するか）→ **解決済み（下記）**
- explorer→verifierの1パス構成（redoなし、ADR-0011）→ **実装・実機確認済み（下記）**
- 予算管理: `ITERATION_BUDGET`の単位を「サブエージェント起動1回」に統一、
  サブエージェントごとの内部ターン数上限を追加 → 未着手（下記「実装結果」の懸念事項を参照）

**Dockerイメージ側の準備（完了）**: 公式マーケットプレイス
（`anthropics/claude-plugins-official`）を調査した結果、TypeScript・Python含む
主要13言語すべてに公式LSPプラグインが存在し、Goの`gopls-lsp`と同じ
`lspServers`スキーマ（`command`/`args`/`extensionToLanguage`）に従うことを確認した。
「masudaが対象repoの言語をどう判定してプラグインを選ぶか」については、
言語検出のヒューリスティックを持たず、ユーザー（またはrepoの保守者）が
`.masuda.json`にDockerイメージ名を明示する方式に決定（`internal/config`、
既にコミット済み）。

これを受けて、Go・Python・TypeScriptそれぞれの言語ツール＋対応LSPプラグインを
同梱したDockerイメージバリアントを実装した:

- `Dockerfile`（base）: `claude plugin marketplace add anthropics/claude-plugins-official`
  を追加。認証不要（公開GitHubリポジトリへの`git clone`のみ）でビルド時に問題なく
  動作することを実機で確認済み。プラグイン状態は`~/.claude/settings.json`・
  `~/.claude/plugins/`に保存され、コンテナ起動時にホストからbind mountされる
  `~/.claude.json`・`~/.claude/.credentials.json`とは別ファイルなので、
  ビルド時に焼き込んだ内容が起動時に上書きされる心配はない
- `docker/go/Dockerfile`: Goツールチェーン（バージョン・sha256固定）＋`gopls`＋
  `gopls-lsp`プラグイン。**注意**: リポジトリ直下に`Dockerfile.go`という名前で
  置くと、Goの`go build ./...`・`go vet ./...`がそれを`.go`ソースファイルとして
  誤認しビルドが壊れることを実機で発見したため、`docker/go/Dockerfile`という
  配置にした
- `docker/python/Dockerfile`・`docker/typescript/Dockerfile`: pyright／
  typescript-language-serverはnpm配布のため新規システムトゥールチェーン不要
  （base imageに既にNode.jsがあるため）。npmのグローバルインストール先が
  root所有のため、その1ステップだけ`USER root`に戻す必要があった
- `docker/full/Dockerfile`: 上記3つを1つのDockerfileにまとめたkitchen sink
  バリアント。複数言語混在repo向け

実機ビルドで確認したイメージサイズ: base 1.64GB → go +410MB（LSPだけでなく
phase4実装エージェントの`go build`/`go test`にも必要） → python +50MB →
typescript +80MB → full（3つ合計）+550MB。事前の見積もり通り、npm配布の
Python・TypeScriptは軽量、Goツールチェーンが支配的という結果になった。

各バリアントとも、対応するLSPバイナリの実行可能性（`gopls version`・
`pyright --version`・`typescript-language-server --version`）と
`claude plugin list`でのプラグイン有効化を実機で確認済み。

**explorer→verifierの実装（完了）**: `orchestrator/implement_review_graph.py`の
フェーズ5に、13観点の機械的チェックが全て収束した後に走るexplorer→verifierの
1パス構成（ADR-0003・ADR-0011、redoなし）を追加した。

- `CROSS_CUTTING_FINDINGS_JSON`（explorerの出力）・`CROSS_CUTTING_VERIFIED_JSON`
  （verifierの出力）を`review_results/`配下に新設。`_detect_review_phase()`は
  13観点収束後、findings未生成なら`cross_cutting_explore`、findingsが空でなければ
  `cross_cutting_verify`、それ以外は`synthesize`に分岐する（findingsが空なら
  verifierを起動する意味がないためスキップ）
- explorerタスク（`_cross_cutting_explore_task()`）はBash・Read・Grep・Glob・
  ネイティブLSPツールへのフルアクセスを持つサブエージェントに、diffを起点とした
  多ターンの探索を委譲する。verifierタスク（`_cross_cutting_verify_task()`）は
  探索した本人とは別コンテキストで指摘の妥当性のみを検証し、確認できたものだけを
  残す。両者ともfixerによる自動修正・redoは持たない（ADR-0011: 複雑な指摘は
  常にG2で人間が判断する）
- 確認済みの指摘は`_cross_cutting_section()`で最終レポートに
  「## 横断的チェックの指摘（人間の判断が必要）」として確定的に追記される
- G2却下時の`_clear_review_state()`（レビューをperspective 0からやり直す、
  ADR-0013）は、これら2ファイルも`review_results/`配下ごと削除するため
  追加のコード変更は不要だった
- **プロンプト文面の修正（レビュー指摘）**: 当初「探索の観点の例」に3つの
  箇条書きを並列で書いていたが、実際には「実装パターンの一貫性」（ファイルAB間の
  流儀の食い違い）と「変更の伝播漏れ」（シグネチャ変更が呼び出し元に反映されて
  いない）は性質の異なる2カテゴリだと指摘を受け、見出しを分けて整理した。さらに
  「伝播漏れ」の例は、Go等の静的型付け言語では単純な引数の過不足がコンパイル
  エラーになりフェーズ4のビルド自己検証（ADR-0009）で既に弾かれるはずだという
  指摘を受け、この観点は動的型付け言語や文字列ベースディスパッチ・リフレクション
  経由の呼び出しなど、ビルドでは検知できないケースに限定する形に修正した
- **実機テストで判明した罠**: `orchestrator/`はDockerイメージのビルド時に
  `COPY`で焼き込まれる（コンテナ起動後に変わらない）ため、既存のコンテナ・
  イメージに対してオーケストレーターのコード変更をテストする際は、必ず
  `masuda-loop:latest`・言語バリアントイメージの両方を再ビルドしてから
  コンテナを起動し直す必要がある。再ビルドを忘れて古いイメージのままテストした
  結果、新しいcross_cutting_explore/verifyフェーズが一切実行されず`synthesize`に
  直行するという見かけ上の不具合に遭遇し、原因究明に時間を要した

**実機テスト（成功）**: `masuda-loop:go`イメージで、意図的に仕込んだコード上の
矛盾（新規関数`pruneStaleLocks`が、リポジトリ内の`runGit`/`runDocker`/tmux起動
ヘルパー等と異なりstderrを握りつぶす、標準ライブラリで済む処理を外部コマンド
`find`に投げている、等）に対し、explorerサブエージェントが実際にネイティブLSP
ツール（`findReferences`）を使い、以下5件を検出した:

1. （高）`pruneStaleLocks`が完全なデッドコード（LSPのfind references・repo全体
   grepで呼び出し元0件、かつ削除対象の`.lock`ファイルを生成するコード自体が
   存在しないことも確認）
2. （中）標準ライブラリで済む処理を外部コマンド`find`に投げている（意図的に
   仕込んだ本命の指摘）
3. （中）import順がgofmt違反（`gofmt -l`で実機確認。これは仕込んだものではなく
   偶発的に混入した実際のバグをexplorerが発見した）
4. （中）stderrを握りつぶしエラー文脈を失っている（`runGit`/`runDocker`/
   `hostloop`の一貫したエラーラップ流儀との不一致。仕込んだ本命の指摘そのもの）
5. （低）workspaceルートディレクトリ未作成時に失敗する潜在バグ（同一ファイル内の
   `List()`の挙動と食い違うことを、実際に`find <存在しないパス>`を実行して
   終了コードを確認した上で指摘）

独立したverifierサブエージェントは5件すべてを追加の引用・根拠を加えた上で確認し
（1件は熟考の上で重大度を中→低に格下げ）、最終レポートの
「横断的チェックの指摘」セクションに正しく反映された。レポートはさらに、
13観点中の「デッドコード・未使用変数」観点がdiff単体では検出できなかったこの
デッドコードを、横断的チェックが検出したことを明示的に言及しており、
ADR-0003が想定した価値（diffだけでは見えない問題の検出）が実際に機能することを
確認できた。G2ゲート到達・`GATE:review`到達・Monitorツールでの待機まで
一連の流れを実機で確認済み。
