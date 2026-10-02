# HANDOFF
## 作業項目
**v0.1.0のリリース**（#59、手順は`.claude/skills/release/SKILL.md`）。2026-10-03 01:10 JSTに公開した。3リポジトリのタグとコミット: masuda `v0.1.0`=5528b91、masuda-engine `v0.1.0`=4b0191a、masuda-sandbox `v0.1.0`=a9dce82。`develop`・`main`・`redesign`はいずれも5528b91。Release: https://github.com/TadahiroYamamura/masuda/releases/tag/v0.1.0 、docs: https://tadahiroyamamura.github.io/masuda/ （`0.1`=`latest`）。
- `98ff556` fix(doctor): qemu-imgの欠けをNGに、lz4の検査をやめる（GondolinはVM起動のたびにqemu-imgを呼ぶ。lz4は自前でinitramfsを組むときだけ）
- `41d07eb` chore(ci): ci.yml・release.ymlに`sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0`のステップ。GitHubのUbuntu 24.04ランナーはAppArmorが非特権ユーザー名前空間を禁じ、フェイクsandboxの`unshare -Urm`（C-M7・serveの特権コマンドのテスト）が`uid_map`への書き込みで落ちた
- `5528b91` chore(deps): go.modのengineを`v0.1.0`に（内容は4b0191aと同じ）
- 再起動（電源断）後の確認: `cab597aec8e2`をresumeして同じplan gateに戻り、承認して完走（実機5周目、約14分）。旧#52は構造的に解決
## 完了した契約テスト
C-M1〜C-M8（`GOWORK=off go test -count=1 ./...`、手元とCI両方で緑）。sandboxの単体64件・契約C-S*8件（実VM）緑。live（`MASUDA_LIVE_TEST=1 go test -timeout 60m ./live/`）PASS 997秒（2026-10-03 00:27〜00:44 JST）。release.ymlの全ステップ緑（Check versions・契約一致・ビルド・pack・checksums）。公開物同士で`masuda version`が`0.1.0`/`contract: ok`、`doctor`全ok。
## 未完と理由
- なし。公開物でのquickstartの1周も通り（`55a7dbe1b35f`、17分31秒、d6c2b1bで出力例を差し替え）、#59は閉じた
- `~/.local/bin/masuda`は9月7日の旧実装のバイナリのまま（触っていない）。公開物で置き換えるのはユーザーの判断
## 次の一手
1. リリース手順は`.claude/skills/release/SKILL.md`（Skill `release`、bb5b03c）に移した。次のリリースはこれに従う。`scripts/precheck.sh vX.Y.Z`が速い前確認
2. quickstart実走で見つけた課題: masuda-engine#3と#66（`expected_byproducts`のglobがengineの完全一致・ホストの`path.Match`のどちらでも効かず、Pythonでは毎ステップdeviationが開く。両方を同じ版で直す）、masuda-engine#4（テスト不足の指摘の`file`が実装側を指し、fixerが構造的にcannot_fix）
3. #65: ゲスト→ホストのフックcurl（TcpMap経由）が1接続だけ約135秒待たされ、メインセッションが止まる。5周目とliveの2周連続で1周に2回。緩和は`internal/guest/guest.go`のフックcurlに`--connect-timeout`・`--max-time`を付けること。原因の切り分けはIssueの手順
4. #60〜#64、masuda-sandbox #1〜#5、masuda-engine #1〜#2
## 注意点
- `redesign`ブランチは役目を終えた（`develop`=`main`）。以後の開発は`develop`。CLAUDE.mdの正の記述も直した
- 自動モードの安全判定で、**強制push・タグpush・CIでのsysctl編集**はエージェントから実行できない（ユーザーが`!`で打つ）。通常のpush（develop・redesign・main新設）とコミットはできた
- `go get masuda-engine@<tag>`はタグ直後なら`GOPROXY=direct`。今回はプロキシも数分で返した
- liveの記録: `/tmp/masuda-live-data-451579620/workspaces/ffd7dd0f371a/`（#65の証拠。再起動で消える）
- ゲストのClaude Code 2.1.287は`Agent`を既定でバックグラウンド起動し、メインセッションはターンを終えて通知を待つ。ループ規約（`internal/guest/loop-claude.md`）はこれを前提にしていない。#65の停止は通知の遅延として現れる
- このセッションのdev `masuda serve`と`masuda-sandbox serve`（`node dist/cli.js`、ソケットは既定）は起動したまま。止めるならpkill
## 契約への提案
なし。
