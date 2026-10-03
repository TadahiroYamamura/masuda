#!/bin/bash
# live.TestGuestSubagentContinuationがゲストのexecノードで動かす検査。メインセッション（claude-work）とは
# 別のtmuxセッションでclaudeを起こし、サブエージェントに文字列を考えさせて覚えさせ、Bashで別の作業を
# 挟んだあとSendMessageで同じサブエージェントに思い出させる。token.txtとrecall.txtが一致すればexit 0。
# exit 1は継続が効かなかった（またはclaudeが筋書きどおりに動かなかった）、exit 2は環境の不備（トークンが無い）。
# 結果の要約は$MASUDA_OUT/continuation-reportに書き、同じものをstderrにも出す（失敗時はexecのLogTailで読める）。
set -u

D=/tmp/continuation
SESSION=continuation
REPORT="${MASUDA_OUT:-$D}/continuation-report"

say() { echo "continuation: $*" >&2; }

say "claude --version: $(claude --version 2>&1)"

# execの環境にClaudeのトークン（プレースホルダ）が入るかはmasudaの版次第（serveのguestEnvは
# トークンを除くと書いているが、2026-10-03の実機では入っていた）。無ければLaunchの環境を継いだ
# メインセッションのtmuxサーバーから取る。
token_source=exec-env
if [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]; then
  token_source=
  for scope in "-t claude-work" "-g"; do
    line=$(tmux show-environment $scope CLAUDE_CODE_OAUTH_TOKEN 2>/dev/null) || continue
    case "$line" in
      CLAUDE_CODE_OAUTH_TOKEN=?*)
        export CLAUDE_CODE_OAUTH_TOKEN="${line#*=}"
        token_source="tmux show-environment $scope"
        break
        ;;
    esac
  done
fi
if [ -z "$token_source" ]; then
  say "トークンが無い: execの環境にもtmux（claude-work・グローバル）にもCLAUDE_CODE_OAUTH_TOKENが無い"
  exit 2
fi
say "token source: $token_source"

rm -rf "$D"
mkdir -p "$D"
for f in token.txt recall.txt done; do
  if [ -e "$D/$f" ]; then
    say "$D/$f が始める前からある"
    exit 1
  fi
done

# tools: Writeだけにするのは、token.txtを読んで当てる抜け道を塞ぐため。
mkdir -p "$HOME/.claude/agents"
cat > "$HOME/.claude/agents/rememberer.md" <<'EOF'
---
name: rememberer
description: 文字列を考えて覚えておく
tools: Write
---
指示されたファイルにだけ書く。ファイルを読まない。頼まれた文字列は後で聞かれるので覚えておく。
EOF

cat > "$D/prompt.txt" <<'EOF'
このセッションはmasudaのループではない。~/.claude/CLAUDE.md のループ規約には従わず、next_task や MCP サーバー masuda のツールは一切呼ばない。以下の手順だけを順に行い、終わったら終了する。/tmp/continuation/token.txt と /tmp/continuation/recall.txt は自分では読まない。

1. Agent ツールで subagent_type "rememberer" のサブエージェントを1つ起動し、次のとおり頼む。「12文字の無作為な英数字の文字列を1つ考え、/tmp/continuation/token.txt にその文字列だけを書け。報告には文字列を含めず『done』とだけ答えよ」。完了を待つ。
2. Bash ツールで `sleep 10` を実行する。
3. 手順1と**同じ**サブエージェントに SendMessage で次のとおり送る（新しいサブエージェントは起動しない。SendMessage が遅延読み込みのツールなら ToolSearch で読み込んでから使う）。「さっき考えた文字列を /tmp/continuation/recall.txt にその文字列だけ書け。ファイルは読むな。覚えていなければ unknown と書け」。完了を待つ。
4. Write ツールで /tmp/continuation/done を作る（中身は何でもよい）。これで終わり。
EOF

claude_bin=$(command -v claude)
# フックはmasudaの/hooksへ届き、このセッションの終了（SessionEnd）がメインセッションの死（DEAD）と
# 区別できない。--settingsでこのセッションだけフックを止める。
tmux new-session -d -s "$SESSION" -x 200 -y 50 -c /workspace \
  -e "CLAUDE_CODE_OAUTH_TOKEN=$CLAUDE_CODE_OAUTH_TOKEN" \
  "$claude_bin --dangerously-skip-permissions --settings '{\"disableAllHooks\":true}' -- \"\$(cat $D/prompt.txt)\"" \; \
  set-option -t "$SESSION" remain-on-exit on
if [ $? -ne 0 ]; then
  say "tmuxのセッション$SESSIONを起こせなかった"
  exit 1
fi

for _ in $(seq 180); do
  [ -e "$D/done" ] && break
  [ "$(tmux display-message -p -t "$SESSION" '#{pane_dead}' 2>/dev/null)" = 1 ] && break
  sleep 2
done
done_seen=no
[ -e "$D/done" ] && done_seen=yes
# doneの直前のWriteが書き終わるのを待ち、画面を残してから止める。
sleep 2
tmux capture-pane -p -t "$SESSION" -S -200 > "$D/pane.txt" 2>&1
tmux kill-session -t "$SESSION" 2>/dev/null

# メインのセッションの記録（~/.claude/projects/-workspace/*.jsonl）のうちこの筋書きのものから、
# ツールの呼び出し回数と、token.txt・recall.txtを書いたサブエージェントの記録を数える（診断用）。
tool_counts=unknown
subagents=unknown
main_log=$(grep -l 'continuation/recall.txt' "$HOME"/.claude/projects/*/*.jsonl 2>/dev/null | head -1)
if [ -n "$main_log" ]; then
  tool_counts=$(python3 - "$main_log" <<'PY' 2>&1
import collections, json, sys
c = collections.Counter()
for line in open(sys.argv[1]):
    try:
        o = json.loads(line)
    except ValueError:
        continue
    content = (o.get("message") or {}).get("content")
    if isinstance(content, list):
        for b in content:
            if isinstance(b, dict) and b.get("type") == "tool_use":
                c[b.get("name")] += 1
print(", ".join(f"{k}={v}" for k, v in sorted(c.items())))
PY
)
  sub_token=$(grep -l 'continuation/token.txt' "$HOME"/.claude/projects/*/*/subagents/*.jsonl 2>/dev/null | xargs -r -n1 basename | tr '\n' ' ')
  sub_recall=$(grep -l 'continuation/recall.txt' "$HOME"/.claude/projects/*/*/subagents/*.jsonl 2>/dev/null | xargs -r -n1 basename | tr '\n' ' ')
  subagents="token.txtを書いた記録: [${sub_token}] recall.txtを書いた記録: [${sub_recall}]"
fi

token=$(tr -d '[:space:]' < "$D/token.txt" 2>/dev/null)
recall=$(tr -d '[:space:]' < "$D/recall.txt" 2>/dev/null)
verdict=fail
if [ -n "$token" ] && [ -n "$recall" ] && [ "$recall" != unknown ] && [ "$token" = "$recall" ]; then
  verdict=pass
fi

{
  echo "claude --version: $(claude --version 2>&1)"
  echo "token source: $token_source"
  echo "done file: $done_seen"
  echo "token.txt: ${#token}文字"
  if [ "$verdict" = pass ]; then
    echo "recall.txt: token.txtと一致"
  else
    echo "recall.txt: '${recall}'（${#recall}文字、token.txtと不一致）"
  fi
  echo "main session tools: $tool_counts"
  echo "subagent transcripts: $subagents"
  echo "verdict: $verdict"
} > "$D/report.txt"

if [ "$verdict" = pass ]; then
  mkdir -p "$(dirname "$REPORT")"
  cp "$D/report.txt" "$REPORT"
  cat "$D/report.txt" >&2
  exit 0
fi
cat "$D/report.txt" >&2
echo "--- capture-pane（末尾） ---" >&2
tail -n 60 "$D/pane.txt" >&2
exit 1
