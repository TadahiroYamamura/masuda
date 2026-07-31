# ADR-0023: review承認時のブランチ反映を、develop等へのローカルmergeからfast-forward限定のPullに変える

## Status

Accepted (2026-07-31)

## Context

[[0005-manual-push-automatic-worktree-ops]]により、`review approve`はworktreeブランチのローカルマージ・worktree削除まで自動化してよいと決めた。その実装（`cmd/masuda/gate.go`の`finalizeReviewApproval`）は`worktree.Merge(root, id, info.Branch, defaultBase)`を呼んでおり、`defaultBase`はリテラル文字列`"develop"`固定だった。

これが実際の運用で破綻した。あるワークスペース（`masuda plan start <既存ブランチ名> ... --base <SHA>`で作成）で`review approve`を実行すると、`refusing to merge: <repo> has "<branch>" checked out, not "develop"`というエラーになった。

調査すると、問題は「developにチェックアウトし直せば直る」という運用の話ではなく、コード側の設計に2つの見落としがあった。

1. **base解決の不整合**: `plan.go`・`workspace.go`のmergeサブコマンド・`review.go`は全て`resolveBase()`（`--base`/`--into`フラグ＞`.masuda.json`の`base`フィールド＞フォールバック`"develop"`の優先順位）を経由して解決するが、`gate.go`の`finalizeReviewApproval`だけがこれを迂回し、常にリテラル`"develop"`をマージ先にしていた。
2. **「新規ブランチ作成」と「既存ブランチ再利用」の区別の欠如**: `internal/worktree/worktree.go`の`Create`は、指定した`branch`が既に存在する場合、`base`パラメータを完全に無視してその既存ブランチをそのままcloneする（新規ブランチ作成時のみ`base`から分岐する）。にもかかわらず`workspace.Create`は、git操作で実際に`base`が使われたかどうかに関係なく、常に解決済みのbase値を`Info.Base`としてmetadataに保存してしまう。今回のワークスペースの`--base <SHA>`は、マージ先の指定ではなく「レビュー時にmasudaが追加したコミットだけを差分として見せる」ためのdiff基準点のピン留めだった。つまり`Info.Base`は「diffの基準点」と「マージ先ブランチ」という意味の異なる2つの用途を1つのフィールドに混在させていた。

この2点を踏まえて修正方針を検討する過程で、そもそも「developにローカルでmergeする」という前提自体が実態に合っていないことが分かった。GitHubのPRベースの開発フロー（本ケースの`oncall_pf_template`はまさにこれで、`origin`へのpush・PRを前提にしている）では、develop等の統合ブランチへのマージはPRを通じて行われ、ローカルで直接mergeすることはない。ワークスペースが新規ブランチをbaseから切ったパターンでも、既存ブランチをそのまま使ったパターンでも、masudaがすべきことは「作業結果をrepoRootの対象ブランチへ反映する」ことだけであり、develop等への統合はユーザー自身がPR経由で行う操作である。

## Decision

`review approve`によるブランチ反映を、`into`（develop等の統合先ブランチ）への`--no-ff` mergeから、**repoRoot上の同名ブランチへのfast-forward限定の反映**に変える。「新規ブランチ作成」と「既存ブランチ再利用」を区別する必要はなくなり、常に同じ1つの操作で済む。

`internal/worktree/worktree.go`に新しい関数`Pull(repoRoot, id, branch string) error`を追加する。

```go
func Pull(repoRoot, id, branch string) error {
    cloneDir := Dir(repoRoot, id)
    current, _ := runGit(repoRoot, "branch", "--show-current")
    if strings.TrimSpace(current) == branch {
        // 対象ブランチをrepoRoot自身がチェックアウト中は直接ref更新をgitが拒否するため、
        // FETCH_HEAD経由でff-onlyマージする
        if _, err := runGit(repoRoot, "fetch", cloneDir, branch); err != nil {
            return err
        }
        _, err := runGit(repoRoot, "merge", "--ff-only", "FETCH_HEAD")
        return err
    }
    // 未チェックアウト（ブランチが存在しない/他ブランチをチェックアウト中どちらでもOK）:
    // 非forceのrefspecなのでgit自身がfast-forwardのみ許可し、非FFなら自動的に拒否する
    _, err := runGit(repoRoot, "fetch", cloneDir, branch+":"+branch)
    return err
}
```

`cmd/masuda/gate.go`の`finalizeReviewApproval`は`worktree.Merge(root, info.ID, info.Branch, defaultBase)`の代わりにこの`worktree.Pull(root, info.ID, info.Branch)`を呼ぶ。`info.Base`・`defaultBase`・`resolveBase()`はこのフローで一切使わなくなる（`Info.Base`はdiffの基準点という元々の用途のままでよく、フィールド自体は変更しない）。

既存の`worktree.Merge`（`into`への`--no-ff` merge）は削除しない。`masuda workspace merge <id> [--into <branch>]`というユーザーが明示的に叩く手動コマンドはそのまま残す。これは自動承認フローとは別の、ユーザーが意図してローカル統合したい場合の逃げ道として維持する。

git-fetchのref更新は次の3パターンいずれでも安全に動作することを、使い捨てリポジトリでの実験で確認済み。

- ブランチがrepoRootに存在しない（新規ブランチ作成パターン）: `git fetch <clonedir> branch:branch`でそのまま新規ブランチとして作られる（exit 0）
- ブランチは存在するが未チェックアウト（既存ブランチ再利用パターン、他ブランチをチェックアウト中）: 同じコマンドでfast-forwardされる（exit 0）。履歴が分岐（非fast-forward）していれば`[rejected] (non-fast-forward)`で自動的に拒否される（exit 1、部分的な状態変更なし）
- ブランチをrepoRoot自身がチェックアウト中: 直接`fetch branch:branch`は`fatal: refusing to fetch into branch ... checked out`で拒否されるため、`fetch branch`（FETCH_HEAD経由）→`merge --ff-only FETCH_HEAD`に切り替える。非fast-forwardなら同様に安全に拒否される

### リカバリ設計

fast-forward限定にしたことで、3-way mergeによるコンフリクト（コンフリクトマーカーが残る中途半端な状態）は構造的に発生しなくなる。起こり得る失敗は「repoRoot側の`branch`とclone側の`branch`が分岐しており、fast-forwardできない」ケースのみで、これは上記の通りgit自身が安全に（部分的な状態変更なしで）拒否する。

`cmd/masuda/gate.go`の`newGateApproveCommand`は`gate.Approve()`（承認マーカーの書き込み）→`finalizeReviewApproval()`（`Pull`の実行）の順で呼ぶ。`Pull`が失敗しても`worktree.Remove`/`workspace.Remove`には到達しないため、ワークスペースの状態は失敗前のまま残る。マーカーの書き込みは冪等（JSON上書きのみ）なので、`masuda review approve <id>`の再実行は常に安全である。したがって失敗時のリカバリは「エラーを返して停止し、人間が分岐の原因を確認して手動で解消してから、`review approve`を再実行する」という運用でよい。

## Alternatives Considered

- **`finalizeReviewApproval`が`defaultBase`の代わりに`resolveBase()`（`.masuda.json`の`base`設定）を読むようにする**: `.masuda.json`の`base`はあくまで「新規ブランチを切るときのデフォルト値」であり、そのワークスペースが実際に何から分岐したか（あるいは分岐すらしていないか）とは無関係。既存ブランチ再利用パターンでは的外れなマージ先になる可能性があるため不採用。
- **`Info`に`BranchExisted bool`を追加し、新規ブランチ作成パターンなら`info.Base`へmerge、既存ブランチ再利用パターンならfast-forwardのみ、と2つの操作に分岐する**: 実装可能だが、GitHubのPRベースの開発フローを前提にすると、そもそもどちらのパターンでもmasudaがdevelop等へローカルでmergeする必要がない。2つの操作に分岐させるよりも、常に同じfast-forward反映に統一するほうが単純で、かつ実態（実際の統合はPR経由）に合っている。
- **非fast-forwardな分岐が検知された場合、エージェント（サブエージェント）に自動でrebase/force-updateさせて解消する**: fast-forward限定にした時点で、解消すべき3-way conflictは構造的に存在せず、起こり得るのは「どちらの履歴を正とするか」という価値判断が要る分岐のみ。これを自動判断させることは、[[0005-manual-push-automatic-worktree-ops]]が定めたpush禁止や、`worktree.Merge`が元々持っていた「ユーザーのチェックアウトを勝手に切り替えない」という他の安全原則と矛盾する破壊的操作になり得るため不採用。非fast-forward時は明確なエラーで停止し、人間の判断を挟む。

## Consequences

- `review approve`はdevelop等の統合ブランチを一切意識しなくなる。GitHubのPRベースの開発フローと自然に整合し、「masudaでの作業結果をPRに出す」という後続手順をユーザーが阻害されずに行える
- 新規ブランチ作成パターンと既存ブランチ再利用パターンを区別するための`Info`拡張（`BranchExisted`等の新フィールド）が不要になり、`workspace.json`のスキーマ変更を避けられた
- 一方で、`masuda workspace merge`（手動コマンド）と`review approve`（自動フロー）でブランチ反映の意味が乖離した。前者は明示的に`into`ブランチへ`--no-ff` mergeする「ローカル統合」、後者はfast-forward限定の「ブランチの持ち帰り」であり、コマンド名だけでは挙動の違いが分からない。ドキュメント（CLAUDE.md）側での明記が必要
- 既にfast-forward不可能な状態（repoRoot側で対象ブランチに直接コミットが積まれた等）になっているワークスペースは、`review approve`が常に失敗するようになる。これは仕様であり、解消には人間の判断（手動でのreconcile）を必須とする
