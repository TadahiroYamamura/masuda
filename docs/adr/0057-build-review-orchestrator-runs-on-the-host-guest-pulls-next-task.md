# ADR-0057: Build/Reviewのオーケストレーターをホストで動かし、ゲストはcurated MCPの`next_task`で次のタスクを受け取る

## Status

Accepted (2026-09-04)

## Context

Issue #43で、VMパスの`orchestrator/implement_review_graph.py`が状態デーモンのtrustedソケットへ到達できないことが実機で確認された。`detect_phase`の最初のデーモン読み取りが`connection refused`で落ち、フェーズ4-5が1ステップも進まない。原因は、trustedソケットがホスト上のUDSであり、[[0044-remove-docker-execution-runtime-vmbackend-only]]でDocker実行基盤を削除した際にcuratedソケット側だけがホスト側relayへ移行され、trusted側には相当するものが無かったことにある（Docker時代はbind mountで届いていた）。v0.1の合格判定はパイプラインを実機で1周通すことであり、これが解消するまでBuildより先へ進めない状態だった。

「ゲストからtrustedへ到達させる配管を足す」ことは可能だが、そもそもなぜゲストがtrusted setを必要とするのかを遡ると、[[0041-mcp-protocol-with-trusted-and-curated-surfaces]]が`orchestrator/*.py`を「サンドボックス内で動くtrustedな呼び手」と位置づけていることに行き着く。しかしオーケストレーターはmasuda自身の制御コード（状態機械・予算・ステップcommit）であって、サンドボックスに入れる理由は無い。フェーズ1-2は既にこの形になっており（`internal/hostloop`がホスト上で`investigate_plan_graph.py`を回す、[[0012-worktree-created-before-investigation]]）、フェーズ4-5だけがメインセッション・サブエージェント・オーケストレーターを同じVMに同居させていた。`runtime/CLAUDE.md`自身が「オーケストレーターとClaudeセッションが同一ユーザー・同一コンテナで実行されている」という権限境界の欠如を但し書きとして抱えていた（Issue #13・#20）。

移設が成立するかは2点にかかっており、どちらも実機で確認した。

1. **ホスト側からworktreeのgitが正しく見えるか。** `/workspace`はホストのclone（`git clone --local`の自己完結クローン、[[0018-git-clone-local-over-linked-worktree]]）のvirtiofs共有である。ゲストが書いた直後にホストで`git status`/`git diff`/ファイル読みを行う試行を50回、`sync`を挟まずに全て成功（取りこぼし0、64MiBファイルのmd5一致、削除・リネーム・mode変更も即座に反映）
2. **逆方向の遅延。** ホストが`git commit`した直後にゲスト内で`git status`がcleanになるまで約0.5〜0.6秒かかる（8回計測、61/472/510/530/592/583/598/597ms）。`virtiofsd`を`--cache=`指定なし＝既定の`auto`（属性キャッシュのタイムアウト1秒）で起動しているためで、この非対称性を知らずに「ホストがcommit→ゲストが即座に新しい状態を見る」前提を置くと間欠的に失敗する

## Decision

`orchestrator/implement_review_graph.py`をホスト側で実行し、サンドボックス内のセッションはcurated MCPの新tool`next_task`で次のタスクを受け取る。

- **ホスト側ランタイム**: `internal/hostloop`の`ensureRuntime`が`implement_review_graph.py`も展開し、フェーズ1-2用と同じvenv・同じ`state_client.py`を共有する。`EnsureImplementReviewOrchestrator()`がそのパスを返す
- **実行**: `cmd/masuda/statedaemon.go`の`orchestratorRunner`が、cwdをworktree、`MASUDA_STATE_DIR`をワークスペースの状態ディレクトリとしてスクリプトを1回実行し、書かれた`TASK.md`を読んで返す。注入の形は`run_privileged_command`の`PrivilegedRunner`と同じ（`internal/statedaemon/mcpserver`から`internal/sandbox`をimportできないため）
- **tool**: `next_task`は引数を取らず、タスク本文を戻り値で返す。ランナーがnilなら登録しない。**ファイルではなく戻り値で返すのはContextの計測2が理由**で、`TASK.md`も従来どおり書かれるが、それは人間と再開セッションのための記録になる
- **ループ**: `runtime/CLAUDE.md`のループルールを「TASK.mdが無ければLangGraphを起動する」から「`next_task`を呼ぶ」へ書き換える。VMからオーケストレーターを起動する手段は無くなる
- **プロンプト内のパス**: `MASUDA_STATE_DIR`はホストのパスになるが、指示を受け取るセッションが開けるのは`/masuda-state`である。`GUEST_STATE_DIR`（`MASUDA_GUEST_STATE_DIR`環境変数、`internal/sandbox.GuestStateDir`が渡す）と`_agent_path()`が、プロンプトへ描画するパスに限って先頭のプレフィックスを差し替える。スクリプト自身が開くパスは常にホスト側のまま
- **イメージ**: rootfsから`masuda`バイナリ・`orchestrator/`・Python venv（およびそれらを作っていたGoのマルチステージビルド）を外す。ゲストがtrusted setへ到達できないことを、規約ではなくイメージの中身として持たせる

## Alternatives Considered

- **trustedソケットにもホスト側relayを立て、ゲストから到達させる**: Issue #43の一次案。`mcpclient`がUDS固定なのでTCP経路の追加が要る点はどの案でも同じだが、これはDocker時代と同じ「ゲストが全キーを読み書きできる」状態を復元する。[[0041-mcp-protocol-with-trusted-and-curated-surfaces]]がcurated setを分けた目的（`gate:triage`への任意書き込みを技術的に塞ぐ、[[0029-immediate-stop-escalation-dedicated-gate]]）を、VMパスに限って手放すことになる
- **orchestrator専用の第3のtool set（`gate:`への`put`だけを拒否する5tool）をゲストへ公開する**: 上より安全で、`gate:*`へのputをorchestratorが一度も行わないことは確認済みだった。ただしこれも「オーケストレーターをVMに置き続ける」ための配管であり、本ADRの移設を行えば丸ごと不要になる。加えて、relayをブリッジのゲートウェイIPに立てる以上、共有ブリッジ上の他ワークスペースのVMからも到達しうる（Issue #47）
- **フェーズ判定そのものをGoの状態デーモンへ移す**: [[0055-gate-marker-is-an-unconsumed-decision-waited-on-by-presence]]のAlternatives Consideredで却下済み。判定の入力（プラン成果物の有無・gitのタグ/diff/変更ファイル集合）がKVストアの領域外であり、Python/Goの言語境界も跨ぐ。本ADRは状態機械をPythonのまま、gitの隣に置いたまま実行場所だけを変えるので、この却下理由には当たらない
- **ゲストの`/masuda-state`をホストと同じ絶対パスにマウントし、パス変換を不要にする**: プロンプトの読み替えが丸ごと消えるが、ホストのホームディレクトリ構成がゲストへ漏れ、`/workspace`・`/masuda-state`という規約（`runtime/fstab.vm`・`runtime/CLAUDE.md`・複数のADR）を壊す
- **プロンプトにシェル変数の形（`$MASUDA_STATE_DIR/...`）で書き、ゲスト側の環境変数で解決させる**: 読み手がシェルではなくLLMであり、Write/Editツールはリテラルのパスしか取らない。決定的なのは、レビュー観点のサブエージェントのようにBashを持たないよう意図的に絞った相手（`implement_review_graph.py`の「Bash/Read/Grep等は不要」）が展開できないこと——結果をどこに書くかという最も落としたくない指示に推論が1段挟まる

## Consequences

- Issue #43は「直す」ではなく消滅する。ゲストにtrustedソケットへの経路が無いことが正常な状態になり、`docs/design/state-daemon-mcp.md`の記述もその前提で書き直された
- [[0029-immediate-stop-escalation-dedicated-gate]]の「triage対象のエージェントが自分でtriageゲートを閉じてはならない」は、curated setの`resolve_gate_from_chat`が`"triage"`を拒否することに加えて、ゲストが状態を書く手段そのものを持たないことで支えられるようになった。Issue #13・#20が指摘する権限境界の欠如は、フェーズ4-5については構造的に解消する
- ステップcommitがホスト側のgitで行われるようになる。ゲストへgit identityを渡す既存の仕組み（`.masuda-git-identity`、[[0044-remove-docker-execution-runtime-vmbackend-only]]）は、サブエージェントが自分でcommitする場合のためだけに残る
- rootfsイメージからLangGraphを含むvenvとGoのビルドステージが消え、イメージが小さくなる。ただし**baseイメージの再ビルドと、それを`FROM`する各リポジトリのイメージエントリの再ビルドが必要**で、この移行を挟まない古いイメージのVMは`next_task`を知らないループプロトコルで動く
- `runtime/entrypoint.sh`・`start_claude.sh`の「`masuda.mcp_relay=`が無ければ自前でrelayを起動する」フォールバックは、`masuda`バイナリが無くなったため成立しない。relay不在は起動時エラーにした——relayが無いとゲート待機も`next_task`も使えず、ループが1歩も進まないため
- ホストとゲストで`stat`の`dev`が異なる（実測2096 vs 33、`ino`は一致）。`.git/index`のstat情報は書いた側とだけ整合するため、gitを叩く主体をホストに寄せたことは都合が良い方向だが、ゲスト側で`git status`を叩くと毎回内容比較にフォールバックしうる
- オーケストレーターは「自分が開くパス」と「プロンプトに書くパス」の2種類を持つことになった。`_agent_path()`を通し忘れたパスはゲストから開けないが、ホスト上のテストでは書き手と読み手が同じマシンなので気付けない。`MASUDA_GUEST_STATE_DIR`を設定した状態のレンダリングをテストで固定してある
