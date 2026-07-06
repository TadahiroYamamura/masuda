#!/usr/bin/env python3
"""
`gh pr view --json title,body,comments,baseRefOid,headRefOid` の出力（--meta）と
`git diff <base> <head>` の出力（--diff）を、レビュー対象の1つのテキストファイルにまとめる。

Usage: python render_pr_context.py --meta pr_meta.json --diff pr.diff -o pr_context.md
"""

import argparse
import json
from pathlib import Path


def render(meta: dict, diff_text: str) -> str:
    lines = [
        "---",
        f"title: {meta.get('title', '')}",
        "---",
        "",
        meta.get("body") or "(説明なし)",
    ]

    comments = meta.get("comments") or []
    if comments:
        lines += ["", "---", "### コメント", ""]
        for c in comments:
            author = (c.get("author") or {}).get("login", "unknown")
            lines.append(f"- **{author}**: {c.get('body', '')}")

    lines += ["", "---", "### 変更内容 (git diff)", "", "```diff", diff_text.rstrip("\n"), "```"]

    return "\n".join(lines) + "\n"


def main() -> None:
    parser = argparse.ArgumentParser(description="PRメタ情報とgit diffを1つのテキストにまとめる")
    parser.add_argument("--meta", required=True)
    parser.add_argument("--diff", required=True)
    parser.add_argument("-o", "--output", required=True)
    args = parser.parse_args()

    meta = json.loads(Path(args.meta).read_text(encoding="utf-8"))
    diff_text = Path(args.diff).read_text(encoding="utf-8")

    Path(args.output).write_text(render(meta, diff_text), encoding="utf-8")
    print(f"[render_pr_context] {args.output}")


if __name__ == "__main__":
    main()
