# ADR-0040: masuda自身が読み書きする状態は、ワークスペース単位の常駐デーモン（汎用KVストア）で管理する

## Status

Accepted (2026-08-17)

## Context

[[0017-inotifywait-for-gate-wait-polling]]のGATE待機機構は、`/masuda-state`がbind mountでホストとコンテナ間で同一inodeを共有していることに構造的に依存している——ホスト側の書き込みがコンテナ内から`inotifywait`のイベントとして即座に見えるのは、両者が同じファイルシステムエントリを指しているからに過ぎない。

GitHub Issue #31のMicroVM移行検討（Firecracker・Cloud Hypervisorの実機spike）で、この前提がDockerに固有のものだと判明した。Cloud Hypervisor + virtio-fsは`/workspace`相当のライブなディレクトリ共有こそ実現できるが、host→guestのinotify伝播（remote inotify）は実機でも動作しないことを確認済みである。つまりMicroVMへ移行する場合、採用するhypervisorに関わらず、`/masuda-state`のゲート待機は「ファイル共有の模倣」ではなく「明示的なRPC」に作り替える必要があるというのがIssue #31での結論であり、この課題はIssue #35として切り出された。

設計を検討する過程で、対象を「ゲート待機だけを解決する薄いRPC」に留めるか、より広い範囲（`orchestrator/*.py`が読み書きするTASK.md・カウンタ・redo系マーカー等）まで踏み込むかという分岐があった。後者を選んだ動機は、既存の`.masuda-gate/<name>.json`のようなマーカーファイル方式が、`detect_phase`（`orchestrator/investigate_plan_graph.py`・`orchestrator/implement_review_graph.py`）の「ディスク上のファイルの有無・中身だけを見て今の状態を再導出する」という設計を、ステップを追加するたびに新しいマーカーファイルを生やす形で肥大化させ続けてきたことにある。[[0039-redo-pending-marker-bridges-gate-consume-and-subagent-rewrite]]のように「redo未完了」という同一概念に対して3種類のマーカーファイルがそれぞれ異なる消費契約を持つに至った実例や、TDDモード（[[0037-tdd-mode-red-green-refactor-subloop-in-phase4]]）が既存の状態機械を拡張せずほぼ丸ごとシャドー複製した実例が、この技術的負債の裏付けになっている。

## Decision

`internal/statedaemon`として、ワークスペースごとに1プロセス常駐する状態デーモンを新設する。中身は名前空間付きキー（`<namespace>:<rest>`、例: `gate:plan`・`internal:iteration-count`）に対する汎用KVストア（`Get`/`Put`/`Delete`/`List`/`WaitForChange`）で、`WaitForChange`が[[0017-inotifywait-for-gate-wait-polling]]の`inotifywait`単発ブロッキング呼び出しに相当するプリミティブになる。値はディスクへ永続化され、デーモン再起動時に読み直される。

**移行するのは「書き手・読み手が両方ともmasuda自身のコード（Go CLI／Pythonオーケストレーター）である」状態だけに限定する。** Claudeのメインセッションやサブエージェントが自身のRead/Edit/Bashツールで直接触る成果物（`plan/summary.md`・`plan/steps.json`・`INVESTIGATION.md`・`triage_concern.json`・`review_results/*`・`implementation_result.json`等）は対象外とし、ファイルのまま残す。この境界は最初から正しく引けたわけではない——`internal/gate`の初回移行でこの区別を誤り、plannerサブエージェントがEditツールで書く`plan/summary.md`等までデーモンへ移してしまい、書き手（サブエージェント→ファイル）と読み手（`internal/gate`→デーモン）が食い違って`masuda plan show`が機能しなくなる実際のバグを作った（2回にわたり訂正コミットで対処）。さらに`INSTRUCTIONS.md`のような「内容がTASK.mdへ展開されるのではなく、ファイルパスそのものがサブエージェントへの指示文に埋め込まれ、サブエージェント自身のReadツールで開かれる」ケースも同じ理由で対象外と判明した——判断基準は「サブエージェント向けの指示文に生のファイルパスが登場するか、それとも内容が既に文字列として展開されているか」である。

デーモンのライフサイクルはワークスペース単位: `masuda internal statedaemon --state-dir <dir>`という隠しサブコマンドを`masuda workspace create`時にデタッチしたバックグラウンドプロセスとして起動し、`masuda workspace remove`で停止する。`masuda plan start <workspace-id>`のような再開経路でも、PIDファイル＋signal 0（実際にはシグナルを送らない生死確認）で冪等に起動できるようにした——デーモンプロセスがホスト再起動や手動killで落ちていても自己修復する。

## Alternatives Considered

- **ファイルを唯一の真実のソースとして残し、デーモンは変更通知だけに徹する（file-relay案）**: デーモンがホスト側でファイルを`fsnotify`監視し、変更をゲスト側へRPCで中継するだけの薄い実装。既存のファイルベース実装への変更が最小で済む利点はあったが、「今どのステップかをdetect_phaseが再導出しやすくなる」というマーカーファイル肥大化への対処にならず、Issue #29（状態ディレクトリのファイル体系整理）の解決にもつながらないため不採用。
- **状態ファイルごとに専用の型付きRPCメソッドを用意する（1ファイル1メソッド）**: `review_results/{result,check,fix,recheck}_{pid}_attempt{N}.json`のようなステップ×観点×試行回数で組み合わせ的に増える動的パスが大半を占めており、専用メソッドを都度追加する設計は非現実的と判断した。
- **masuda自身が読み書きする状態だけでなく、サブエージェント成果物も含めて一律デーモンへ移す**: 実際に試みて実バグを作った（Context参照）。サブエージェントはデーモンへ到達する手段（MCPツール等）を持たないため、書き手側がファイルのままである限り、デーモン側だけ移行しても読み手・書き手が食い違う。サブエージェントにMCP書き込みtoolを与える設計は本ADRのスコープ外とした。
- **ホスト全体で単一の常駐デーモンが全ワークスペースを多重化する**: masudaは現状ワークスペースのコンテナ・worktree・状態ディレクトリいずれもワークスペースID単位でアドレッシングしており、「masudaは常駐サービスを持たない」という既存の運用モデルからも大きく外れる。1ワークスペースのデーモンがクラッシュしても他に影響しない、という分離の利点も失われるため不採用。

## Consequences

- ワークスペースのライフサイクル管理（`internal/workspace`・`cmd/masuda`）が、コンテナ・worktreeに加えてバックグラウンドのデーモンプロセスも面倒を見る必要が生じた。デーモンには現状スーパーバイザがなく、クラッシュ時は次回のstart系コマンド実行まで気付かれない
- 「masuda自身の状態」と「サブエージェント成果物」という2層のモデルを常に意識する必要がある。新しい状態を追加するたびに「これは誰が書き、誰が読むか」を先に確認しないと、Contextに記した実バグを再現するリスクがある
- `.masuda-base-ref`（Go側`worktree.Create`が書く、読み取り専用）は書き手・読み手の条件だけ見れば安全に移行できるはずだが、`workspace.Create`がデーモン起動（`startDaemon`）より前に実行されるため、意図的にファイルのまま残した。デーモン起動タイミングとの依存関係を今後同様の判断で都度確認する必要がある
- `TASK.md`（Claudeのメインセッションが自身のReadツールで読む、ADR-0006のループ制御チャネル）は本ADRの対象外のまま残っている。ループ終了条件の伝え方自体をMCP経由に作り替えるかどうかは別途検討が必要
