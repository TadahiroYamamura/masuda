#!/usr/bin/env bash
# mkdocs build・mike deployの前に走らせ、正が別の場所にある2つのページを作る。
#   docs/api/reference.md                 masuda.protoから生成（buf.gen.docs.yaml）
#   docs/user/reference/workflow-schema.md masuda-engineのdocs/workflow-schema.mdの写し
# どちらも.gitignore対象。
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
