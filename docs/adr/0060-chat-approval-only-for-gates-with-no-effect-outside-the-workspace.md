# ADR-0060: chat内で自己解決してよいゲートを「承認がワークスペースの外に影響しないもの」に限り、reviewゲートを除外する

## Status

Accepted (2026-09-04)

## Context

[[0006-interactive-chat-plus-fast-path-gates]]は、ゲートに到達したセッションを終了させずに待機させ、人間が`masuda chat`で対話しながら承認できる形を用意した。対話の中で「進めていい」と言われたとき、セッション自身がゲートを解決してよい——この経路は[[0042-mcp-tool-call-replaces-inotifywait-gate-wait]]で`resolve_gate_from_chat` MCPツールになり、[[0029-immediate-stop-escalation-dedicated-gate]]の要請から`triage`だけがサーバー側で拒否されるようになった。

残る`plan`と`review`は同じ扱いだったが、この2つは承認したときに起きることが根本的に違う（GitHub Issue #24）。

- `plan`の承認はマーカーが`approved`になり、ループが次の段階へ進むだけである
- `review`（G2）の承認は、ホスト側CLIの`finalizeReviewApproval`（`cmd/masuda/gate.go`）まで含む一連の操作である——サンドボックスVMの停止、clone側の残差分commit、cloneからrepoRootへのブランチfast-forward反映（`worktree.Pull`、[[0023-review-approve-fast-forward-not-local-merge]]）、clone・状態ディレクトリの削除

セッションがマーカーだけを自己書き込みすると、待機は解除されるが後半が一切実行されない。実際に`oncall_pf_template`のワークスペースで発生し、gateは`approved`になったのに対象ブランチはホスト側リポジトリに存在せず、cloneも残ったままになった（ホストから改めて`masuda review approve`を実行して復旧）。

## Decision

`resolve_gate_from_chat`が受け付けるゲートを`plan`のみに絞る（`chatResolvableGateNames`）。判定基準は「**そのゲートの承認がワークスペースの外に影響しないか**」とし、3ゲートすべてをこの1つの規則で説明する。

| ゲート | chat自己解決 | 承認が引き起こすこと |
|---|---|---|
| `plan` | 可 | ループが次の段階へ進むだけ |
| `review` | 不可 | ブランチを人間の実リポジトリへ反映し、clone・状態ディレクトリを削除する |
| `triage` | 不可 | 懸念の対象が自分で閉じることになる（[[0029-immediate-stop-escalation-dedicated-gate]]） |

`review`をchatで承認しようとした場合、ツールは`masuda review approve <workspace-id>`をホストで実行する必要があることを述べたエラーを返す。`runtime/CLAUDE.md`のループ仕様も、その旨を人間へ伝えて待機を続けるよう指示する。

[[0057-build-review-orchestrator-runs-on-the-host-guest-pulls-next-task]]以降、ゲストがゲートマーカーを書く経路はこのツールしか無い。したがってこの制限は規約ではなく技術的な境界であり、GitHub Issue #13が指摘した「権限境界がプロンプト指示のみ」という状態が、3ゲートすべてについて解消したことになる。

## Alternatives Considered

- **マーカー書き込みと`finalizeReviewApproval`を不可分にする（Issue #24の提案3）**: [[0057-build-review-orchestrator-runs-on-the-host-guest-pulls-next-task]]がホスト側処理をcurated toolへ注入する仕組み（`PrivilegedRunner`・`OrchestratorRunner`）を用意したので、同型の`ReviewFinalizer`を足せば技術的には実装できるようになっていた。採らなかった理由は権限である——現状ゲストにできるのはマーカーを1つ書くことだけで、人間の実リポジトリを書き換え、ワークスペースを削除する部分はホストで人間が打つコマンドを必要とする。この案はそれをゲストから起動可能にするもので、ゲストから状態書き込み手段そのものを取り上げた[[0057-build-review-orchestrator-runs-on-the-host-guest-pulls-next-task]]の直後に逆方向へ広げることになる。実装上も、finalizeは自分を呼んでいるVMを停止しデーモンが使用中の状態ディレクトリを削除するため、ツール呼び出しが正常に返せない
- **冪等性を文書化し、承認直後に人間へ案内させる（Issue #24の提案2）**: `gate.Approve`にガードが無いので`masuda review approve`の再実行で復旧できるのは事実だが、案内するかどうかがエージェントの遵守に依存する。反映漏れが起きたこと自体には誰も気付かない
- **`review`のchat承認時にホスト側で警告だけ出す**: 警告の宛先が問題になる。人間はVM内のchatセッションを見ており、ホストの標準エラーは誰も見ていない

## Consequences

- G2で対話しながら承認する運用ができなくなり、人間はホストの端末で`masuda review approve <workspace-id>`を打つ必要がある。[[0006-interactive-chat-plus-fast-path-gates]]が用意した2パスのうち、chatパスはG1専用になった。ただしG2の対話そのもの（レポートについての質疑）は従来どおりchatで行える——最後の一押しだけがホストへ移る
- 承認の操作がホスト側に一本化されたことで、`finalizeReviewApproval`が実行されない経路が無くなった。Issue #24が記録した実インシデントの再発経路は塞がれている
- `resolve_gate_from_chat`が実質`plan`専用ツールになった。将来「承認がワークスペースの外に影響しない」新しいゲートが増えれば、そのときに`chatResolvableGateNames`へ足す判断材料は本ADRの基準になる
