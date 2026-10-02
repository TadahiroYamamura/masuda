#!/usr/bin/env bash
# リリース前の速い確認をまとめて走らせる。使い方: precheck.sh vX.Y.Z
# masudaのチェックアウトの中から打つ。隣の../masuda-engine・../masuda-sandboxは
# MASUDA_ENGINE_DIR・MASUDA_SANDBOX_DIRで差し替えられる。
# VMを使う確認（sandboxの契約テスト、live）は長いので含めない。最後にコマンドを出す。
set -euo pipefail

tag=${1:-}
case "$tag" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "usage: $0 vX.Y.Z" >&2; exit 2 ;;
esac
version=${tag#v}

root=$(git rev-parse --show-toplevel)
engine=${MASUDA_ENGINE_DIR:-$root/../masuda-engine}
sandbox=${MASUDA_SANDBOX_DIR:-$root/../masuda-sandbox}
fail=0

say() { printf '\n== %s\n' "$*"; }
ng() { printf 'NG: %s\n' "$*"; fail=1; }
ok() { printf 'ok: %s\n' "$*"; }

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
  cd $sandbox && pnpm build && node dist/cli.js serve --socket "\$XDG_RUNTIME_DIR/masuda-sandbox.sock" &
  MASUDA_SANDBOX_SOCKET="\$XDG_RUNTIME_DIR/masuda-sandbox.sock" pnpm test:contract
  cd $root && MASUDA_LIVE_TEST=1 GOWORK=off go test -count=1 -timeout 60m -v ./live/
そのあと SKILL.md の手順2（engineのタグ）へ。
MSG
