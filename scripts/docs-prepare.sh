#!/usr/bin/env bash
# mkdocs build・mike deployの前に走らせ、正が別の場所にある3つのページを作る。
#   docs/api/reference.md                 masuda.protoから生成（buf.gen.docs.yaml）
#   docs/user/reference/workflow-schema.md masuda-engineのdocs/workflow-schema.mdの写し
#   docs/design/release.md                .claude/skills/release/SKILL.mdの本文（frontmatterを除く）
# いずれも.gitignore対象。
#
# 最後に、docs/以下のMarkdownの__MASUDA_VERSION__をリリースの版に置き換える。
# これはdocs/の元ファイルをその場で書き換える。CIのcheckoutは使い捨てなので問題ないが、
# 手元で回したときは差分をコミットしないよう、終わったらgit checkout -- docs/で戻す。
# 版の決め方: MASUDA_DOCS_VERSION、無ければタグpush時のGITHUB_REF、無ければ直近のタグ。
#
# MASUDA_ENGINE_DIR: masuda-engineのチェックアウト（既定は隣の../masuda-engine）。
# GitHub Actionsはワークスペースの外にcheckoutできないので、ここで場所を渡す。
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
engine_dir=${MASUDA_ENGINE_DIR:-$root/../masuda-engine}

# bufが無い開発環境でも、Goだけあれば同じ版の生成ができるようにする。
if command -v buf >/dev/null 2>&1; then
  buf=(buf)
else
  buf=(go run github.com/bufbuild/buf/cmd/buf@latest)
fi

cd "$root"
mkdir -p docs/api
"${buf[@]}" generate --template buf.gen.docs.yaml

src=$engine_dir/docs/workflow-schema.md
if [ ! -f "$src" ]; then
  echo "docs-prepare: $src が無い。masuda-engineをチェックアウトし、MASUDA_ENGINE_DIRで場所を指定する" >&2
  exit 1
fi
dst=docs/user/reference/workflow-schema.md
mkdir -p "$(dirname "$dst")"
{
  printf '!!! note "取り込んだ文書"\n'
  printf '    この文書は[masuda-engine](https://github.com/TadahiroYamamura/masuda-engine)リポジトリの`docs/workflow-schema.md`から取り込んだもの。編集は向こうで行う。\n\n'
  cat "$src"
} >"$dst"

# リリース手順の正はSkill（エージェントも人間も同じ文面を読む）。サイトにはfrontmatterを落として載せる。
src=.claude/skills/release/SKILL.md
dst=docs/design/release.md
{
  printf '!!! note "取り込んだ文書"\n'
  printf '    この文書はリポジトリの`.claude/skills/release/SKILL.md`から取り込んだもの。編集はそちらで行う。\n\n'
  awk 'BEGIN { fm = 0 } NR == 1 && $0 == "---" { fm = 1; next } fm == 1 { if ($0 == "---") fm = 2; next } { print }' "$src"
} >"$dst"

version=${MASUDA_DOCS_VERSION:-}
if [ -z "$version" ]; then
  case "${GITHUB_REF:-}" in
    refs/tags/v[0-9]*) version=${GITHUB_REF#refs/tags/v} ;;
  esac
fi
if [ -z "$version" ]; then
  version=$(git describe --tags --abbrev=0 --match 'v[0-9]*' 2>/dev/null || true)
  version=${version#v}
fi
if [ -z "$version" ]; then
  echo "docs-prepare: 版を決められない。MASUDA_DOCS_VERSIONを指定するか、タグ(vX.Y.Z)を取得する" >&2
  exit 1
fi
# sed -iはGNUとBSD(macOS)で引数が違うので、perl -piを使う。
find docs -name '*.md' -exec env V="$version" perl -pi -e 's/__MASUDA_VERSION__/$ENV{V}/g' {} +
