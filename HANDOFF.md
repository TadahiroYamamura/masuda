# HANDOFF
## 作業項目
`masuda gate show`のplanゲートで計画を読みやすく出す（engineの計画スキーマの構造化とCLIの描画）。
- masuda-engine `bd9515b`（main、push済み）: 同梱`plan.json`に`goal`・`steps[].title`・`steps[].tests`・`alternatives[]{option, reason}`・`risks[]`を足し（いずれも必須、配列は空可）、`planner.md`を「goal→機能単位のステップ（title）→description・tests→files」の分解と、テストファイルも同じステップの`files`に入れる指示に書き直した
- masuda-engine `1581849`（main、push済み）: `implementer.md`に、ステップの`tests`を同じステップで書いて通す指示を追記した
- masuda（develop、未push）: `cmd/masuda/client.go`の`formatGate`が`target: plan`のとき計画のJSONを`goal`・`summary`・`steps`（番号・title・description・tests・files）・`alternatives (considered, not taken)`・`risks`・`expected byproducts`の節に分けて出す（`formatPlan`）。無い項目は行ごと・節ごと省く（旧スキーマの計画も落ちない）。JSONとして解けない、または`steps`が無ければ従来どおり全文。契約テストの計画の固定値を新スキーマに、`docs/user/quickstart.md`の8節・`docs/api/flows.md`・`docs/user/cli.md`を新しい出力に合わせた
- masuda（develop、未push）: `go.mod`のengineを`v0.1.1-0.20261003003625-1581849e06e7`（mainの`1581849`）に追従させた
## 完了した契約テスト
C-M1〜C-M8（`go.mod`で固定したengine `1581849`を使って`GOWORK=off go test -count=1 ./...`緑）。`cmd/masuda`に新スキーマの描画・旧スキーマ・JSONでないときのテストを足した。旧スキーマの実物（`55a7dbe1b35f`の`records/gates/0000003-1.json`）も描画を目で確かめた
## 未完と理由
- 実機（live・quickstart）でplannerが新スキーマの計画を書けるかは未確認
## 次の一手
1. 実機1周で、plannerのtitleが機能単位になるか、テストファイルが同じステップの`files`に入ってdeviationが減るかを確かめる
## 注意点
- masudaの`go.mod`はengineの擬似バージョン`v0.1.1-0.20261003003625-1581849e06e7`（mainの`1581849`、タグなし）を指す。新スキーマは`go.work`なしで入り、`GOWORK=off go test ./contract/`のC-M4も緑。次のリリースでengineにタグを打ったら、そのタグへ`go get`し直す
- `docs/user/quickstart.md`の8節の出力例は新しい描画の形で書いたもので、公開物（v0.1.0）の実走ではない。v0.1.0のバイナリは計画を1行のJSONで出す。次のリリースまでdocsのサイト（`0.1`）とは食い違うので、リリース時に実走の出力へ差し替えるとよい
- 旧スキーマの計画はstepに`title`が無いので、ステップの見出しは`  1.`だけになり、descriptionがその下に5スペース字下げで続く
- サーバー・APIは変えていない（`subject`は計画のJSONのまま。見せ方はクライアントの責任）
- `serve`の`TestStallAfterFromLocalSettings`が`./...`一括実行で1度だけ落ちた（5秒以内にSTALLEDにならない）。単体と再実行では緑。負荷時の時間依存の揺れとみているが未調査
## 契約への提案
なし
