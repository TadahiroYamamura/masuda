#!/usr/bin/env python3
# Usage: ./fetch_pr_data.py OWNER/REPO PR_NUMBER [--with-content] [output.json]
#
# 依存: gh (GitHub CLI, 認証済み)

import argparse
import base64
import json
import subprocess
import sys
import urllib.parse
from pathlib import Path


def _gh(endpoint, paginate=False):
    cmd = ["gh", "api"]
    if paginate:
        cmd += ["--paginate", "--jq", ".[]"]
    cmd.append(endpoint)
    result = subprocess.run(cmd, capture_output=True, text=True)
    if result.returncode != 0:
        return None
    if paginate:
        items = []
        for line in result.stdout.splitlines():
            line = line.strip()
            if line:
                items.append(json.loads(line))
        return items
    return json.loads(result.stdout)


def fetch_pr(repo, pr_number, with_content=False):
    detail = _gh(f"repos/{repo}/pulls/{pr_number}")
    if detail is None:
        raise RuntimeError(f"PR #{pr_number} の取得に失敗しました")

    review_comments = _gh(f"repos/{repo}/pulls/{pr_number}/comments?per_page=100", paginate=True) or []
    reviews         = _gh(f"repos/{repo}/pulls/{pr_number}/reviews?per_page=100",  paginate=True) or []
    files_raw       = _gh(f"repos/{repo}/pulls/{pr_number}/files?per_page=100",    paginate=True) or []

    files = files_raw
    if with_content:
        head_sha = detail["head"]["sha"]
        files = []
        for f in files_raw:
            if f["status"] == "removed":
                files.append({**f, "content": None})
                continue
            encoded = urllib.parse.quote(f["filename"], safe="/")
            data = _gh(f"repos/{repo}/contents/{encoded}?ref={head_sha}")
            content = None
            if data and data.get("encoding") == "base64":
                try:
                    content = base64.b64decode(
                        data["content"].replace("\n", "")
                    ).decode("utf-8", errors="replace")
                except Exception:
                    pass
            files.append({**f, "content": content})

    return {
        "number":          detail["number"],
        "title":           detail["title"],
        "state":           detail["state"],
        "merged":          detail.get("merged", False),
        "created_at":      detail["created_at"],
        "merged_at":       detail.get("merged_at"),
        "closed_at":       detail.get("closed_at"),
        "author":          detail["user"]["login"],
        "body":            detail.get("body"),
        "base_branch":     detail["base"]["ref"],
        "head_branch":     detail["head"]["ref"],
        "head_sha":        detail["head"]["sha"],
        "review_comments": review_comments,
        "reviews":         reviews,
        "files":           files,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("repo", metavar="OWNER/REPO")
    parser.add_argument("pr_number", type=int)
    parser.add_argument("--with-content", action="store_true")
    parser.add_argument("output", nargs="?")
    args = parser.parse_args()

    output = Path(args.output or f"pr_{args.pr_number}.json")
    print(f"[fetch] PR #{args.pr_number} ({args.repo}) → {output}")
    pr = fetch_pr(args.repo, args.pr_number, with_content=args.with_content)
    output.write_text(json.dumps(pr, ensure_ascii=False, indent=2))
    print(f"[fetch] 完了: {output}")


if __name__ == "__main__":
    main()
