#!/usr/bin/env bash
# リリース前の速い確認をまとめて走らせる。使い方: precheck.sh vX.Y.Z
# masudaのチェックアウトの中から打つ。隣の../masuda-engine・../masuda-sandboxは
# MASUDA_ENGINE_DIR・MASUDA_SANDBOX_DIRで差し替えられる。
# VMを使う確認（sandboxの契約テスト、live）は長いので含めない。最後にコマンドを出す。
# `precheck.sh --claude-code`はゲストのClaude Codeの版の段だけを走らせる（SKILL.mdの1-0で版を上げた直後に使う）。
set -euo pipefail

root=$(git rev-parse --show-toplevel)
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
fail=0

say() { printf '\n== %s\n' "$*"; }
ng() { printf 'NG: %s\n' "$*"; fail=1; }
ok() { printf 'ok: %s\n' "$*"; }

# check_claude_code は、internal/guestのClaudeCodeVersionが`masuda version`・init の雛形・liveのDockerfileに
# 入っていること（go testで見る）と、その版が配布元に実在すること（manifest.jsonが200）を確かめる。
check_claude_code() {
  say "ゲストのClaude Code（internal/guest.ClaudeCodeVersion）"
  local ver base code
  ver=$(cd "$root" && GOWORK=off go run ./cmd/masuda version --sandbox-socket /nonexistent/none.sock 2>/dev/null \
    | sed -n 's/^  claude code: \([^ ]*\) (guest, verified)$/\1/p')
  if [ -z "$ver" ]; then ng "masuda versionにclaude codeの行が無い"; return; fi
  ok "masuda versionの版: $ver"
  if (cd "$root" && GOWORK=off go test -count=1 -run 'TestInitRepoWritesTemplatesAndKeepsExistingFiles|TestPrintVersionShowsVerifiedClaudeCode|TestLiveDockerfilePinsClaudeCode' ./cmd/masuda/ ./live/ >/dev/null); then
    ok "雛形のDockerfile・liveのDockerfileに$verが入る"
  else
    ng "雛形かliveのDockerfileに版が入っていない（GOWORK=off go test ./cmd/masuda/ ./live/）"
  fi
  base=$("$here/claude-code-latest.sh" --base-url)
  code=$(curl -s -o /dev/null -w '%{http_code}' "$base/$ver/manifest.json" || true)
  if [ "$code" = 200 ]; then ok "$base/$ver/manifest.json は200"; else ng "$base/$ver/manifest.json が$code（その版は配布されていない）"; fi
  local latest
  if latest=$("$here/claude-code-latest.sh"); then
    if [ "$latest" = "$ver" ]; then ok "最新版と同じ"; else printf 'info: 最新版は%s（定数は%s）。上げないなら理由を追跡Issueに書く\n' "$latest" "$ver"; fi
  else
    ng "最新版を引けない（claude-code-latest.sh）"
  fi
}

if [ "${1:-}" = "--claude-code" ]; then
  check_claude_code
  exit "$fail"
fi

tag=${1:-}
case "$tag" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "usage: $0 vX.Y.Z | --claude-code" >&2; exit 2 ;;
esac
version=${tag#v}

engine=${MASUDA_ENGINE_DIR:-$root/../masuda-engine}
sandbox=${MASUDA_SANDBOX_DIR:-$root/../masuda-sandbox}

check_repo() {
  local name=$1 dir=$2 branch=$3
  say "$name ($dir)"
  git -C "$dir" fetch -q origin
  if [ -n "$(git -C "$dir" status --porcelain)" ]; then ng "作業ツリーに未コミットの変更がある"; else ok "作業ツリーはclean"; fi
  local head upstream
  head=$(git -C "$dir" rev-parse "$branch")
  upstream=$(git -C "$dir" rev-parse "origin/$branch")
  if [ "$head" != "$upstream" ]; then ng "$branch($head)がorigin/$branch($upstream)と違う"; else ok "$branch はoriginと一致 ($(git -C "$dir" log --oneline -1 "$branch"))"; fi
  if git -C "$dir" rev-parse -q --verify "refs/tags/$tag" >/dev/null; then ng "タグ$tagが既にある"; fi
  if git -C "$dir" ls-remote --tags origin "refs/tags/$tag" | grep -q .; then ng "originにタグ$tagが既にある"; else ok "タグ$tagは未使用"; fi
}

check_repo masuda "$root" develop
check_repo masuda-engine "$engine" main
check_repo masuda-sandbox "$sandbox" main

say "masuda: mainはdevelopのfast-forwardか"
if git -C "$root" merge-base --is-ancestor main develop; then ok "main は develop の祖先（手順4で合わせられる）"; else ng "main に develop に無いコミットがある"; fi

say "masuda: build / vet / test（GOWORK=off）"
(cd "$root" && GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test -count=1 ./... 2>&1 | grep -v 'no test files') || ng "masudaのテストが落ちた"

check_claude_code

say "masuda-engine: test"
(cd "$engine" && go test -count=1 ./... 2>&1 | grep -v 'no test files') || ng "engineのテストが落ちた"

say "masuda-sandbox: 単体テスト"
(cd "$sandbox" && pnpm test 2>&1 | tail -4) || ng "sandboxの単体テストが落ちた"
if [ -n "$(git -C "$sandbox" status --porcelain)" ]; then ng "sandboxのテストが作業ツリーを汚した"; fi

say "契約SHA（internal/sandboxcontract/sha.go）"
(cd "$root" && MASUDA_SANDBOX_DIR="$sandbox" go generate ./internal/sandboxcontract/ && git diff --exit-code --quiet -- internal/sandboxcontract/) && ok "再生成しても差分なし" || ng "sha.goに差分が出た（sandboxの契約が変わっている）"

say "release.ymlと同じクロスビルド"
tmp=$(mktemp -d)
for target in linux/amd64 darwin/arm64; do
  os=${target%/*}; arch=${target#*/}
  if (cd "$root" && CGO_ENABLED=0 GOOS=$os GOARCH=$arch GOWORK=off go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$tmp/masuda_${os}_${arch}" ./cmd/masuda); then ok "built $target"; else ng "build $target"; fi
done
if [ "$(uname -s)/$(uname -m)" = "Linux/x86_64" ]; then
  out=$("$tmp/masuda_linux_amd64" version --sandbox-socket "$tmp/none.sock" 2>/dev/null | head -1 || true)
  case "$out" in "masuda $version "*) ok "masuda version → $out" ;; *) ng "masuda versionの出力が違う: $out" ;; esac
fi
rm -rf "$tmp"

say "clients/ts: npm pack --dry-run"
(cd "$root/clients/ts" && npm pack --dry-run 2>&1 | grep -E 'filename|total files') || ng "npm packが落ちた"

say "まとめ"
if [ "$fail" -ne 0 ]; then echo "NGがある。直してからもう一度。"; exit 1; fi
cat <<MSG
速い確認はすべて通った。次はVMを使う確認（同時に走らせない）:
  cd $sandbox && pnpm build && node dist/cli.js serve --socket "\$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock" &
  MASUDA_SANDBOX_SOCKET="\$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock" pnpm test:contract
  cd $root && MASUDA_SANDBOX_SOCKET="\$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock" MASUDA_LIVE_TEST=1 GOWORK=off go test -count=1 -timeout 20m -v -run TestGuestSubagentContinuation ./live/
  cd $root && MASUDA_SANDBOX_SOCKET="\$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock" MASUDA_LIVE_TEST=1 GOWORK=off go test -count=1 -timeout 60m -v -run TestDevelopLapOnPythonRepo ./live/
そのあと SKILL.md の手順2（engineのタグ）へ。
MSG
