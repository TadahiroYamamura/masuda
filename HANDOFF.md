# HANDOFF
## 作業項目
人間がstagingのコミットに付けた行コメントを、差分のゲートを却下したときに手直しのエージェントへ届ける（masuda、develop、未push）。
- serve: `runCtl.decide`（`serve/run.go`）で、判断が`rejected`かつゲートに`StagingCommit`があるとき、そのコミットへの人間のコメント（`author == "human"`）を時刻順に集め、engineへ渡す`engine.Decision.Comment`だけを「本文＋`## 差分への行コメント`＋`- <path>:<line>: <body>`」に合成する（`rejectFeedback`、`serve/gates.go`）。記録とAPIの`Decision.comment`は人間が送った本文のまま。`approved`では合成しない。`path`の無いコメントは`（コミット全体）`、`line`が0なら`<path>`だけ。複数行の本文は2スペース字下げで箇条書きに収める
- CLI: `masuda gate comment <id> <occurrence> <path>:<line> <text>`を足した（`GateService.Get`→`staging_commit`が無ければ「このゲートは差分を対象にしていない」→`StagingService.AddComment`）。本文は残りの引数をつなげる。`gate show`は`staging_commit`のあるゲートで`ListComments`を1回呼び、人間のコメントを差分の後に`comments (sent to the agent on reject):`で並べ、未判断なら`comment: masuda gate comment ...`の行を添える
- docs: `docs/api/flows.md`（判断する・差分ビュー）、`docs/user/cli.md`（gateの表）、`docs/user/quickstart.md`（9節の却下の例）
- 前の作業（planゲートの計画の描画、engine `1581849`への追従）はコミット済み・未push
## 完了した契約テスト
C-M1〜C-M8と、新しい`TestCM4_RejectedReviewCarriesLineComments`（smokeでreview gateに行コメントを付けて却下→次のimplementerのタスクファイルに合成された差し戻しが入り、ゲートの記録の本文は人間のまま）。`GOWORK=off go test -count=1 ./...`緑。serveに`TestRejectSendsHumanLineComments`・`TestRejectFeedback`、`cmd/masuda`に`TestFormatGateHumanComments`・`TestParseLocation`を足した
## 未完と理由
- 実機（live・quickstart）で、plannerが新スキーマの計画を書けるか、手直しのエージェントが行コメントを踏まえて直すかは未確認
## 次の一手
1. 実機1周で、plannerのtitleが機能単位になるか、テストファイルが同じステップの`files`に入ってdeviationが減るかを確かめる
2. 同じ周回でreview gateに`gate comment`を付けて却下し、rework/implementのタスクに行コメントが届いて反映されるかを見る
## 注意点
- `../masuda-engine`は別のエージェントが編集中のことがある。gitignore済みの`go.work`があると編集中のengineが混ざるので、ビルド・テストは`GOWORK=off`で行う
- masudaの`go.mod`はengineの擬似バージョン`v0.1.1-0.20261003003625-1581849e06e7`（mainの`1581849`、タグなし）を指す。次のリリースでengineにタグを打ったら、そのタグへ`go get`し直す
- `docs/user/quickstart.md`の8節の出力例は新しい描画の形で書いたもので、公開物（v0.1.0）の実走ではない。9節の`gate comment`もv0.1.0には無い。リリース時に実走の出力へ差し替えるとよい
- 行コメントの合成はserve側（engineへ渡す直前）で行い、engineの`Decision`や契約は変えていない。差し戻しの全文はengineの記録（feedback）とタスクファイルの「前回からの差し戻し」に残る
- `gate show`の行コメント表示はクライアントが`ListComments`の順（追記順）で出す。serveの合成は時刻で安定ソートする（追記順と同じになるはず）
- 旧スキーマの計画はstepに`title`が無いので、ステップの見出しは`  1.`だけになる
- `serve`の`TestStallAfterFromLocalSettings`は`./...`一括実行で稀に落ちる（5秒以内にSTALLEDにならない）。単体と再実行では緑。今回は一括でも緑
## 契約への提案
なし
