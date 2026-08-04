# 競合ツールの機能・UX調査（2026-08-04時点）

これはmasudaの設計ドキュメントでもADRでもない。競合ツールがどのような機能・UXを提供しているかのWeb調査記録であり、今後masudaの機能を検討する際の参考資料として残す。実装するかどうかの判断・適合性の評価はここでは行っていない（事実の棚卸しが目的）。

調査対象は以下のカテゴリ:

- worktree/セッション管理系: claude-squad, Crystal(Nimbalyst), vibe-kanban, Conductor.build
- AIレビュー系: CodeRabbit, Qodo Merge(PR-Agent), Cursor Bugbot, Greptile, Korbit, Sourcery, Bito
- 自律コーディングエージェント全般: Devin, OpenHands, Factory.ai Droid等
- エディタ/プラットフォーム内蔵系: GitHub Copilot coding agent, Cursor Background Agent, Windsurf, Sourcegraph Amp等

比較基準としたmasudaの現状（2026-08-04時点）: CLIのみ（Web UI・モバイルアプリ・デスクトップGUIなし）、進捗確認は`masuda chat`でのtmux attachのみ、ゲート承認もCLIコマンドのみで外部通知なし、レビュー結果はテキストレポートまたはHunk連携のみ、複数ワークスペースの横断ビューは`masuda workspace list`のみでメトリクスは無い。

## 1. 可視化・進捗管理

- **vibe-kanban**: Kanbanボードで複数タスクをカラム表示、「ブロックされていない作業だけに絞る」フィルタ、全エージェント横断のリアルタイムステータス表示。カードが「In Progress→Done」に要した時間を自動集計しボトルネックを可視化する組み込みアナリティクス。
- **Nimbalyst（Crystal後継）**: Kanbanボード＋リサイズ可能なファイル変更パネル/プロンプトパネル。1つのworktreeで複数エージェントを同時稼働させて表示。Markdown WYSIWYG・Monacoコードエディタ・CSV表計算・UIモックアップ・Excalidraw図・ER図・Mermaid図という7種類以上のビジュアルエディタを内蔵し、コード以外の成果物もツール内で直接編集・確認できる。
- **OpenHands**: ブラウザベースの「Agent Canvas」で複数エージェントを1画面から同時監視。稼働中エージェントの役割・タスク配分をリアルタイム表示する計画（CPU/メモリ/トークン消費の概観、エージェント状態のリアルタイム診断）。
- **GitHub Copilot coding agent**: GitHub Mobileにセッション一覧ビュー（状態別フィルタ、リアルタイム更新）。
- **Devin**: 全コマンド・ファイルdiff・ブラウザ操作を含む完全なリプレイタイムラインをセッションごとに保持。
- **Sourcegraph Amp**: チーム内のスレッド活動・貢献度を追跡する「リーダーボード」でエンジニアリングリードに利用状況の可視性を提供。
- **claude-squad**: TUI内にGit diffの行数統計（追加/削除）を表示するDiff Viewがあり、Tabキーでpreview/diff表示を切替可能。

## 2. 通知・連携

- **Cursor Background Agent**: Slackで`@Cursor`メンションしてエージェントを起動、完了時にSlackへ通知＋作成されたPRへのリンク、同一スレッド内でフォローアップ指示を追加可能。Web/モバイルブラウザからもセッションに参加でき、PWAとしてインストールしてネイティブアプリ的に使える。
- **GitHub Copilot coding agent**: Slackアプリで`@GitHub`メンションからコーディングエージェントを起動しスレッド内で進捗を追える。Slackから自然言語でGitHub Issueを直接作成可能。GitHub MobileにLive Notifications機能があり、セッション進捗をプッシュ通知でリアルタイム受信。
- **Devin**: Slackモーダルからセッション作成時にPlaybook/Snapshotを添付可能。Web UIとSlackスレッドが双方向同期（どちらで送っても反映）。任意のSlackメッセージを右クリックして「Ask Devin about this」でコードベースへの質問として即座に投げられる。
- **Factory.ai Droid**: Slack・Microsoft Teams・Linear・Jiraと連携。セッション・実行環境・スキルがTUI/デスクトップ/Web/IDE/Slack間で同期し、「ターミナルで始めてスマホで終わらせる」という運用が可能。
- **Nimbalyst**: SwiftUI製のネイティブiOS/Androidコンパニオンアプリ。QRコードでデバイスペアリングし、セッション完了・エラー発生・承認待ちをプッシュ通知（ディープリンク付き）で受け取れる。スマホ上でファイル変更を赤緑diffでスワイプ確認し、個別に承認/却下できる。
- **vibe-kanban**: デスクトップ通知からカードへジャンプバックできる導線、GitHub連携でコミットとカードを紐付け。

## 3. コスト・メトリクス可視化

- **OpenHands**: トークン消費量・API費用・レスポンス遅延を横断的にモニタリングする専用のメトリクス/コスト追跡システム。Enterpriseプランでは予算上限の強制と利用状況レポート機能。
- **GitHub Copilot coding agent**: セッションのオーバービュー画面でトークン使用量・セッション数・セッション所要時間を確認可能。
- **Nimbalyst**: トークン使用量の解析・表示機能。
- **Sourcegraph Amp**: チーム単位のリーダーボードで利用状況・貢献度を可視化。
- **vibe-kanban**: カードごとの所要時間集計によるボトルネック分析（コスト自体ではなく時間軸のメトリクス）。

## 4. レビュー結果の見せ方

- **CodeRabbit**: PRの先頭に「walkthrough」コメント（変更内容の平易な要約、関連する変更をまとめたファイル一覧テーブル）を自動投稿。API呼び出し・イベントフロー等アーキテクチャに影響する変更にはMermaidのシーケンス図・状態遷移図・ER図を自動生成（"Change Stack"）。インラインの行コメントに加え、Critical/Major/Minor/Trivialの重要度ラベルでフィルタ可能。IDE拡張機能ではワンクリックで提案を適用できる。`@coderabbitai`への返信で30秒以内にインラインでチャット応答するAgentモード。過去のレビューフィードバックを記憶し以後の指摘に反映する「Learnings」機能。
- **Qodo Merge（PR-Agent）**: PRコメント上のスラッシュコマンド群——`/review`（全体レビュー）、`/describe`（PR説明文の自動生成/更新）、`/improve`（改善提案）、`/ask`（PRに関する質問応答）、`/update_changelog`、`/implement`（人間のレビューコメントでの議論をそのままコミット可能なコードに変換）。
- **Cursor Bugbot**: 1PRに対し8並列パスをランダム化した差分順序で実行しバグを検出。自律的なエージェント調査（Claude Agent SDKベース）で検出したバグをクラウドVM上で自動修正し、そのままPRブランチへコミットとしてpush（35%がそのままマージされる）。
- **Greptile**: コードベース全体のコードグラフを構築した上での対話型チャットレビューアシスタント。
- **Korbit**: 「教える」ことに重心を置いたPRボット。指摘の説明・ガイドに加え、人間が読みやすいPR説明文を自動生成。
- **Sourcery**: IDE内でリアルタイムに改善点を下線表示し、ワンクリックで修正を適用できるペアプログラマー的UX。過去のレビュー傾向から提案を学習。30以上の言語に対応。

## 5. タスクの起点の多様性

- **GitHub Copilot coding agent**: GitHub Issueから起動、Slackから`@GitHub`メンション＋自然言語でIssue作成→起動、GitHub Mobileから起動、CLIから起動。
- **Cursor Background Agent**: Slackスレッド内メンションから起動、Web/モバイルPWAから起動。
- **Devin**: Slackモーダルからチャンネル選択・Playbook添付・プロンプト編集の上で起動。任意のSlackメッセージへの右クリックからも起動可能。「Shortcuts」でSnapshot＋Playbookの組み合わせをホーム画面に保存し、リポジトリごとにワンクリックで定型タスクを起動できる。
- **Factory.ai Droid**: ターミナル・IDE・ブラウザ・Slack・Teams・Linear・Jiraのいずれからでもタスクを起点にできる。

## 6. エディタ統合

- **CodeRabbit**: 無料のVS Code拡張機能でPRに上げる前にローカルで同じAIレビューを実行可能（無料版はトークン制限あり）。
- **Sourcery**: IDE内でのリアルタイム改善提案（30以上の言語対応）。
- **Bito**: VS Code/JetBrains上のIDE拡張＋CLI、組織のコードベースに合わせて学習させられる。
- **Factory.ai**: VS Code/JetBrains/Vim内にネイティブに常駐し、既存のショートカット・ツールチェーン・デバッグ手順を変えずにAI駆動の変更を実行。
- **Windsurf（Cascade）**: エディタ横に常駐するエージェントパネルで複数ファイルを一度に編集、ツール呼び出し（MCP/シェル/Web）ごとにレビュー可能な差分としてステージング。実行前に触るファイル一覧・大まかな手順を提示するステップ単位の承認フロー。IDE内にアプリのライブプレビューを表示し、プレビュー上の要素をクリックしてその要素/スタイルの修正をCascadeに依頼できる。「continue my work」で中断した作業を編集履歴＋ターミナル履歴から再開。

## 7. その他のUX

- **Devin**: 「**Machine Snapshots**」（リポジトリのクローン・環境構築済みの状態を「セーブ」しておき、以降のセッションをその状態から即座に開始できる。毎回のセットアップ時間を省く）。「**Rollback**」（セッション全体をやり直さず、失敗したステップだけを巻き戻して修正を続けられる）。「**Playbooks**」（頻出タスク向けの再利用可能なプロンプトテンプレート）。「**Managed Devins**」（親Devinが複数の子Devinに作業を分担させ、各子は独立したVM上でターミナル・ブラウザ・開発環境を持つ。親は子の実行トレースを読んで「何が上手くいき何が失敗したか」を次のタスク分解に反映できる）。
- **vibe-kanban**: Chrome DevTools・要素インスペクタ・モバイルデバイスエミュレーションを備えた統合ブラウザ「App Preview Engine」をUI内に内蔵し、エージェントが作業中のアプリをリアルタイムでプレビューできる。UIから直接devサーバーを起動できる。
- **Nimbalyst**: 1つのworktree内で複数エージェントを同時に走らせられる。7種類以上のビジュアルエディタにより、コード以外（モックアップ・図・仕様書）の成果物もツール内でレビュー・編集完結できる。
- **Crystal（レガシー版）**: 「Test, compare approaches」を標榜し、並列に走らせた複数セッションの結果を比較検討する体験を製品コンセプトの中心に据えている。
- **Sourcegraph Amp**: スレッド共有機能で、推論過程・ツール呼び出し・ファイル編集を含むセッション全体をチーム/公開範囲で共有し、他の開発者が「エージェントがなぜその解法に至ったか」を丸ごと追跡できる。

## 補足: Machine Snapshotsについての検討結果

上記7節のDevinの「Machine Snapshots」はmasudaでも当初検討したが、[Issue #9](https://github.com/TadahiroYamamura/masuda/issues/9)での議論の結果、masudaには不要という結論に至った。重い依存関係インストールはユーザー自身のDockerイメージビルド（`.masuda/settings.json`の`image`フィールド）に委ねられ、Dockerのレイヤーキャッシュが同等の高速化とキャッシュ無効化判定を既に提供するため。詳細はIssue #9を参照。

## 参照した主要URL

- https://www.blog.brightcoding.dev/2026/07/17/vibe-kanban-the-revolutionary-ai-agent-manager-every-dev-needs
- https://www.vibekanban.com/docs/workspaces/chat-interface
- https://nimbalyst.com/features/
- https://github.com/Nimbalyst/nimbalyst
- https://nimbalyst.com/mobile-agent-management/
- https://runpane.com/compare/claude-squad
- https://vibecodinghub.org/blog/claude-squad-review
- https://docs.coderabbit.ai/pr-reviews/walkthroughs
- https://www.coderabbit.ai/blog/introducing-atlas-the-first-ai-native-code-review-interface
- https://www.coderabbit.ai/blog/code-with-ai-review-with-coderabbits-ide-extension-apply-fixes-in-one-click
- https://deepwiki.com/qodo-ai/pr-agent
- https://docs.devin.ai/integrations/slack
- https://medium.com/@nitinmatani22/devin-ai-macros-and-version-history-trigger-playbooks-instantly-and-iterate-without-fear-d92bb6410e8a
- https://cognition.ai/blog/devin-can-now-manage-devins
- https://factory.ai/product/web
- https://www.productcool.com/product/factory-4
- https://github.blog/changelog/2026-02-26-github-mobile-track-coding-agent-progress-in-real-time-with-live-notifications/
- https://github.blog/changelog/2025-10-28-work-with-copilot-coding-agent-in-slack/
- https://docs.github.com/en/copilot/how-tos/use-copilot-agents/cloud-agent/track-copilot-sessions
- https://cursor.com/blog/agent-web
- https://cursor.com/docs/integrations/slack
- https://swizec.com/blog/cursor-background-agents-in-slack-changed-my-workflow
- https://www.openhands.dev/
- https://deepwiki.com/OpenHands/OpenHands/8.4-agent-implementations
- https://bito.ai/compare/bito-vs-greptile/
- https://www.seaflux.tech/blogs/cascade-windsurf-ai-keeps-developers-in-flow/
