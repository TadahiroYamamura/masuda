#!/usr/bin/env python3
"""docs/design/*.md が引用しているソースの行番号が、今も該当箇所を指すか照合する。

docs/design/README.md は「行番号を引用したら今も該当箇所を指すか確認すること」と
定めているが、確認は人手で、しかも「編集した文の引用だけ直す」運用のため、離れた
場所の大きな削除でまとめてずれても誰も気付かない。実際 2026-09-09 時点で27件中23件
がずれており、うち1件はファイル総行数を超える行を指していた。人間が読んで正しさを
判定できない種類の情報なので、機械で照合する。

引用の形は `` `シンボル`（`:123`）`` 系（記号や語が間に挟まっても拾う）。シンボルの
実体は Python の `def` / モジュール定数、Go の `func` / `type` / `const` / `var` から
探す。同名が複数あればどれか1つに当たれば一致とみなす（呼び出し側は「その名前の
定義」を指しており、どのファイルかまでは書いていないことが多いため）。

終了コード: 全一致なら0、ずれが1件でもあれば1。
"""

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DOC_GLOB = "docs/design/*.md"
SOURCE_GLOBS = ("orchestrator/*.py", "internal/**/*.go", "cmd/**/*.go", "runtime/*.py")

# `シンボル` と `:123` の間に「（」「, 」「の」等が挟まる書き方が実在するため、
# 間に置ける文字数だけ制限して間の中身は問わない。
CITATION = re.compile(r"`([A-Za-z_][A-Za-z0-9_]*)`[^`\n]{0,16}?`:(\d+)`")

# バッククォートで括られた語が全て識別子とは限らない（`develop`（ブランチ名）、
# `list`（サブコマンド名）等、地の文が同じ形で登場する）。素の小文字語は識別子
# として扱わず照合対象から外す——`_`か大文字を含むものだけを識別子とみなす。
# 素の小文字語まで照合すると、無関係な同名定義に当たって偽の不一致を作る
# （実例: `list` が internal/statedaemon/mcpserver/server.go の list に当たった）。
IDENTIFIER = re.compile(r"_|[A-Z]")

DEFINITION = (
    # Python: def / async def / モジュール定数
    r"^\s*(?:async\s+)?def\s+{sym}\b",
    r"^{sym}\s*(?::[^=]+)?=",
    # Go: func / メソッド / type / const / var
    r"^func\s+{sym}\b",
    r"^func\s+\([^)]*\)\s+{sym}\b",
    r"^(?:type|const|var)\s+{sym}\b",
)


def source_lines():
    out = {}
    for pattern in SOURCE_GLOBS:
        for path in ROOT.glob(pattern):
            out[path] = path.read_text(encoding="utf-8").splitlines()
    return out


def definitions(sources, sym):
    hits = []
    patterns = [re.compile(p.format(sym=re.escape(sym))) for p in DEFINITION]
    for path, lines in sources.items():
        for n, line in enumerate(lines, 1):
            if any(p.match(line) for p in patterns):
                hits.append((path.relative_to(ROOT), n))
    return hits


def main():
    sources = source_lines()
    stale, matched, unresolved = [], 0, []
    for doc in sorted(ROOT.glob(DOC_GLOB)):
        for n, line in enumerate(doc.read_text(encoding="utf-8").splitlines(), 1):
            for sym, cited in CITATION.findall(line):
                if not IDENTIFIER.search(sym):
                    continue
                where = f"{doc.relative_to(ROOT)}:{n}"
                hits = definitions(sources, sym)
                if not hits:
                    unresolved.append((where, sym, int(cited)))
                elif any(h[1] == int(cited) for h in hits):
                    matched += 1
                else:
                    stale.append((where, sym, int(cited), hits))

    for where, sym, cited, hits in stale:
        actual = ", ".join(f"{f}:{i}" for f, i in hits)
        print(f"NG {where}: `{sym}` は :{cited} と引用しているが実体は {actual}")
    for where, sym, cited in unresolved:
        print(f"?? {where}: `{sym}`（:{cited}）の定義が見つからない——引用先を確認すること")
    print(f"\n一致 {matched}件 / ずれ {len(stale)}件 / 未解決 {len(unresolved)}件")
    return 1 if stale else 0


if __name__ == "__main__":
    sys.exit(main())
