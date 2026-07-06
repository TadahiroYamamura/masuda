#!/usr/bin/env python3
# Usage: ./fetch_all_prs.py [--with-content] [--no-cache] [--cache-dir DIR] [output.zip]
#
# 依存: gh (GitHub CLI, 認証済み), fetch_pr_data.py (同ディレクトリ)

import argparse
import json
import subprocess
import sys
import zipfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
from fetch_pr_data import fetch_pr

REPO      = "ONCALLJP/oncall_pf_template"
REPO_SLUG = "ONCALLJP_oncall_pf_template"


def get_pr_numbers(repo):
    result = subprocess.run(
        ["gh", "api", "--paginate", "--jq", ".[].number",
         f"repos/{repo}/pulls?state=all&per_page=100"],
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        raise RuntimeError("PR 一覧の取得に失敗しました")
    return sorted(int(n) for n in result.stdout.splitlines() if n.strip())


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--with-content", action="store_true")
    parser.add_argument("--no-cache",     action="store_true")
    parser.add_argument("--cache-dir")
    parser.add_argument("output", nargs="?")
    args = parser.parse_args()

    cache_dir = Path(args.cache_dir or f"pr_cache_{REPO_SLUG}")
    output    = Path(args.output    or f"pr_data_{REPO_SLUG}.zip")
    cache_dir.mkdir(exist_ok=True)

    print(f"[all] リポジトリ  : {REPO}")
    print(f"[all] 出力先      : {output}")
    print(f"[all] キャッシュ  : {cache_dir}")
    print(f"[all] ファイル内容: {args.with_content}")

    print("[all] PR 一覧を取得中...")
    pr_numbers = get_pr_numbers(REPO)
    print(f"[all] 対象 PR 数: {len(pr_numbers)}")

    done, fail = 0, 0
    for pr_number in pr_numbers:
        out = cache_dir / f"pr_{pr_number}.json"
        if not args.no_cache and out.exists():
            print(f"[skip] PR #{pr_number}")
            done += 1
            continue
        try:
            pr = fetch_pr(REPO, pr_number, with_content=args.with_content)
            out.write_text(json.dumps(pr, ensure_ascii=False, indent=2))
            print(f"[done] PR #{pr_number}")
            done += 1
        except Exception as e:
            print(f"[fail] PR #{pr_number}: {e}", file=sys.stderr)
            fail += 1

    print(f"[all] 取得完了: {done}件 / 失敗: {fail}件")

    if done == 0:
        print("Error: 取得できた PR がありません", file=sys.stderr)
        sys.exit(1)

    print("[all] ZIP 圧縮中...")
    json_files = sorted(
        cache_dir.glob("pr_[0-9]*.json"),
        key=lambda p: int(p.stem.split("_")[1]),
    )
    with zipfile.ZipFile(output, "w", zipfile.ZIP_DEFLATED) as zf:
        for f in json_files:
            zf.write(f, f.name)

    print(f"[all] 完了: {output} ({done} PR 収録、失敗 {fail} 件)")


if __name__ == "__main__":
    main()
