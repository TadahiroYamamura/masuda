# ADR-0046: `masuda review start`はProvision〜Reviewの既存機構をそのまま再利用し、専用のレビュー実行パスは作らない

## Status

Accepted (2026-08-19)

## Context

本ADRは、`docs/design/sandbox-workflow.md`の廃止にあたり、同文書の「## レビュー単体での再利用」節に記録されていたがどのADRにも記録されていなかった設計判断を、ADRとして書き起こし直したものである。

masudaのフルパイプラインはProvision（worktree作成）→Discovery（調査）→Blueprint（プラン作成）→plan gate→Build（実装）→Review（レビュー）→review gateという6段階+2ゲートで構成される（`docs/glossary.md`）。一方、既にコミット済みの既存ブランチ（自分がフルパイプラインを経ずに書いたブランチ、あるいは他人のPR相当のブランチ）に対して、レビューロジック（14観点のreview/check往復＋横断的チェック）だけを単体で使いたいというニーズがある。フルパイプラインをこの用途にそのまま流用すると、Discovery/Blueprint/Buildを経由する前提でオーケストレーター（`orchestrator/implement_review_graph.py`）が組まれているため、「実装済みで差分もある」状態からどう合流させるかを決める必要があった。

## Decision

`masuda review start <branch-or-ref> [--base develop]`（`cmd/masuda/review.go:30`）は、Provision（worktree作成）〜Review（レビュー）に使われている機構をそのまま再利用する。フルパイプラインとの違いは「Provisionで新規ブランチを切る」代わりに「既に存在するref」からworktreeを作る点のみで、LSP・fixer・review/check/fix/recheckループはフルパイプラインと完全に同一のコードパスを通る。

- `worktree.BranchExists`で対象ブランチの存在を事前検証する（`masuda plan start`とは逆に、存在しないブランチはエラーにする）
- `seedReviewOnly(id)`（`cmd/masuda/review.go:126`）が状態ディレクトリに`implementation_result.json`を`{"status": "done"}`として直接書き込む。状態デーモン経由ではなく平ファイルへの直書きなのは、この時点ではまだワークスペースの状態デーモンが起動していない（Go側のCLIコマンドから直接呼ばれる）ため。これにより`detect_phase`はオーケストレーターの最初の呼び出しから`_detect_post_implementation_phase`に直行し、Build段階の実装・機械的バックストップ・途中レビューを一切経由しない
- `plan/steps.json`が存在しないため、`_iteration_budget()`は`BASE_BUDGET`のみにフォールバックし、[[0010-plan-deviation-reopens-plan-gate]]相当の機械的バックストップも`PLAN_STEPS_JSON.exists()`チェックにより自動的にスキップされる。このワークスペースはplan gateを一度も経由していないため、比較対象となる「承認された計画」自体が存在しない
- diffの基準refは`_read_base_ref()`が読む状態ディレクトリの`.masuda-base-ref`（`worktree.Create`が作成時に記録）である点は通常のフルパイプラインと共通だが、意味が異なる。通常のBuild段階では`_compute_diff()`の`git diff --cached <base_ref>`は「まだcommitされていない実装差分」を指すのに対し、レビュー単体では対象ブランチは既にcommit済みのため、この差分がそのままブランチの実装内容そのものになる。`git diff HEAD`では常に空になってしまうため、bare HEADではなくbase_ref基準であることがここで意味を持つ

新規のオーケストレーター状態機械・専用のレビュー実行パスは作らない。「フルパイプラインの一部分だけをスキップして合流させる」という設計を、既存の`detect_phase`の分岐に載せる形で実現する。

## Alternatives Considered

- **`--pr <PR番号>`によるGitHub PR指定を用意する**: ブランチ指定のみで十分と判断し、スコープ外とした。
- **直接API方式時代の`render_pr_context.py`（diff+メタ情報を1テキストにまとめてサブエージェントへ渡す方式）を新方式でも使い続ける**: 直接API方式（`langchain_anthropic.ChatAnthropic`でAnthropic APIを直接呼ぶ旧実装）では1回のAPI呼び出しで完結させる必要があったため、diffとメタ情報を事前に1つのテキストへまとめる`render_pr_context.py`が必要だった。新方式ではエージェントが実際のworktree内で作業するため、`git diff`やLSPをその場で叩ける。この前処理はほぼ不要になったため採用しなかった。

## Consequences

- レビュー単体実行は、フルパイプライン向けのオーケストレーター・review/check/fix/recheckループ・横断的チェックのコード資産をそのまま享受できる。専用の実行パスを別途保守する必要がない。
- 対象が自分のブランチか他人のPR相当のブランチかで挙動を変える必要がない。fixerによる自動修正はworktree内で完結しpushしない限り外部に影響しないため、[[0005-manual-push-automatic-worktree-ops]]の「pushは常に手動」がそのままレビュー単体実行でも安全装置として機能する。
- `masuda review start`はワークスペースIDの導入（[[0014-workspace-id-and-external-state-directory]]）後に実装されたため、実行のたびに一意なワークスペースIDを発行する。同じrefに対して複数回実行しても、[[0014-workspace-id-and-external-state-directory]]により別ワークスペースとして衝突なく並行できる。
- Build段階を経由しないため、`.masuda-base-ref`はBuild段階と異なる意味（「未コミット差分の基準点」ではなく「ブランチそのものの内容を映すための差分基準点」）を持つことになる。同じフィールドが文脈によって意味を変える点は、コードを読む側が意識する必要がある暗黙の前提として残る。
- worktreeの作成自体は`git clone --local`によるローカルクローン（[[0018-git-clone-local-over-linked-worktree]]）であり、レビュー単体実行でもこの制約（クローンされたブランチはメインリポジトリの参照グラフに含まれない等）がそのまま適用される。
