---
name: adr-author
description: Write a new ADR in masuda's docs/adr/, and keep the existing collection honest. Use when — the user asks for an ADR to be written or recorded; you have established via doc-placement that something is an ADR; a new decision changes or reverses an earlier ADR, so that ADR's Status and the index in docs/adr/README.md have to be updated; or you notice an ADR Status, the index in docs/adr/README.md, or an ADR pointer in docs/design/ that has gone stale or inconsistent. Step A-0 re-checks that the content really is ADR material even when the request came in as "write an ADR", and hands off to doc-placement when it is not.
---

# Writing and maintaining masuda's ADRs

Two independent jobs live here. **A** fires when writing a new ADR; **B** fires whenever the collection needs to be brought back in line, which includes — but is not limited to — the moment you write one.

---

# A. Writing a new ADR

## A-0: Confirm it is actually an ADR — even when you were asked for one directly

"ADRを書いて" is a reasonable request that is sometimes wrong about the content behind it, and this skill can be reached without `doc-placement` ever running. Check both before writing:

1. **Is there a real alternative that was seriously considered and rejected?** Not a hypothetical strawman.
2. **Is the "why" invisible from just reading the current code?**

If either is "no", **stop and run `doc-placement`** — the content probably belongs in a code comment, `docs/design/`, or the commit message instead. Say which, and why, rather than writing the ADR anyway.

This gate is worth the friction because the error is one-way: ADRs are never deleted, so one written for a gotcha or a bug story stays in the directory and in the index permanently, and every later reader has to work out that it should not have been there. Declining to write one costs nothing — the content still gets recorded, just somewhere it is actually read.

If the user pushes back after you have raised this, write the ADR. They may know of a rejected alternative you have not been told about.

## A-1: Check for overlap first

```bash
grep -rl "<relevant keyword>" docs/adr/
cat docs/adr/README.md
```

Read any hits in full. Three outcomes:

- **Already fully covered** — do not write a new ADR. Answer with the existing number, and if some `docs/design/` file was about to carry the reasoning inline, leave it a one-line pointer instead (B-3).
- **Adjacent but not the same decision** — e.g. an existing ADR covers *when* worktrees are created and this one covers *how*. Write the new ADR, and cross-reference the related one in Context via `[[NNNN-filename-without-extension]]` instead of re-explaining its Context.
- **Not covered at all** — write it from scratch.

Skipping this produces two ADRs that decide the same thing in different words, which is worse than no ADR: a later reader has no way to tell which one is in force.

## A-2: Pick the file

```bash
ls docs/adr/
git log --all --oneline --name-only --diff-filter=A -- 'docs/adr/0*.md' | grep '^docs/adr/' | sort -u | tail -5
```

Take the highest number either command reports, and use `+ 1`, zero-padded to 4 digits. Both commands are needed:

- **Read the numbers off the directory, not off `docs/adr/README.md`** — superseded ADRs are removed from the index (B-2) but keep their file and their number, so the index would hand you a number that is already taken.
- **Check every branch, not just yours.** masuda is worked on in several branches/worktrees at once, and an ADR added on another branch — especially one not yet pushed — is invisible to `ls` here. This has already happened once: two different ADR-0045s were written in parallel and the collision was caught only just before commit. Renumbering afterwards means touching the file, its title line, the index, and every `（ADR-NNNN）` pointer in `docs/design/`.

If a collision is unavoidable because the other branch is not visible from here at all, say so rather than assuming — the number is cheap to shift before commit and expensive after.

Filename: `NNNN-kebab-case-summary-of-the-decision.md`. Name it after the decision (`0018-git-clone-local-over-linked-worktree.md`), not the topic area.

## A-3: Write it using the established template

Match this shape precisely, headers included:

```markdown
# ADR-NNNN: <the decision itself, phrased as a sentence, not a topic label>

## Status

Accepted (YYYY-MM-DD)

## Context

What situation or tension made a decision necessary. Reference related ADRs
with [[NNNN-filename]] where relevant (this renders as a bidirectional link).
Concrete enough that someone with no memory of the discussion understands
why this needed deciding at all.

## Decision

What was actually decided, described as a mechanism (what happens, in what
file/function/flow) — not just the intent behind it.

## Alternatives Considered

- **<a real alternative>**: why it was rejected, in one or two sentences.
- <at least one more if there was one>

## Consequences

- Concrete downstream effects — including ones that get harder or more
  complex as a result, not just the upside. A list of pure benefits with no
  tradeoff is a sign the "why not do this obvious other thing" wasn't
  actually worked through.
```

Nothing else ever gets added to this file. The `## Status` section is the only part that changes later (B-1); everything from `## Context` down is written once.

Before writing, read 2-3 existing ADRs to calibrate tone and depth — they run fairly detailed (a paragraph or two of Context, concrete Alternatives), not one-liners. Use recent, currently-in-force ones such as `0043` or `0044`. **Do not model a superseded ADR** (check the Status line first): its Status carries an exceptional two-line form that must not be copied into a new file.

Then do B — all three parts.

---

# B. Keeping the collection honest

Run this whenever a decision changes, and whenever you simply notice drift. It does not require that you are writing an ADR right now.

## B-1: ADR bodies are frozen; Status must stay current

The body (Context / Decision / Alternatives Considered / Consequences) is frozen once written. Do not edit it, not even to fix a typo in a way that changes meaning, and never to reflect a later change of direction. It records what was true and decided *then*. If a decision changes, that is a **new** ADR, explaining the change in its own Context via `[[NNNN-filename]]`.

**`## Status` is the one exception, and keeping it current is mandatory.** It is the only signal a future reader has that a decision was later narrowed or reversed; a bare `Accepted` on an ADR that a later decision already changed is what makes the whole collection untrustworthy.

- **The earlier decision no longer holds at all** — replace its Status section:

  ```markdown
  Superseded by [[NNNN-the-new-adr]] (YYYY-MM-DD)

  Accepted (original date) — <one sentence on what replaced it and why>
  ```

- **The earlier decision still holds but a specific part was replaced** — keep its `Accepted (date)` line and append below it:

  ```markdown
  - 一部改訂: [[NNNN-the-new-adr]] — <what specifically changed>
  ```

  Be concrete about *which part*. "0027により変更された" is useless to someone deciding whether to open the file; "ステップ位置の追跡は`git rev-list --count`ではなく`masuda-step-<workspace-id>-<N>`というgit tagの数" is what they need.

- **The body states something that was already factually wrong when it was written** — no later decision changed it, it was simply incorrect. Keep the `Accepted (date)` line and append:

  ```markdown
  - 訂正: <what the body claims> は誤り。<what is actually true, and where to see it>
  ```

  This is not `一部改訂:` — nothing was decided differently, so no ADR is cited as the cause. The decision itself still holds, so the index line stays exactly as it is (unlike `Superseded by`, which removes it). Reach for this whenever you find an error in an ADR you are reading: fixing the body is not an option, and a correction recorded anywhere else will not reach the person who opens that ADR.

Touch only the Status section. Everything from `## Context` down stays byte-for-byte as it was.

## B-2: Update the index (`docs/adr/README.md`)

`CLAUDE.md` routes readers to the index rather than to the directory, so an ADR missing from the index is invisible and one with stale annotations actively misleads. The index is **not append-only** — all three operations come up:

- **A new ADR** — add one line to the matching `###` theme group, in number order. If no group fits, add a new group rather than forcing it into a bad one:

  ```markdown
  - **[NNNN](NNNN-filename.md)** <the decision in one line, same phrasing as the ADR title>
  ```

- **An ADR you just marked `一部改訂:`** — edit its *existing* index line, appending or extending the annotation. **Numbers only**; the explanation belongs in that ADR's Status section and must not be duplicated here:

  ```markdown
  - **[0027](0027-....md)** Build段階をステップ単位の... — *一部改訂: 0029, 0037*
  ```

- **An ADR you just marked `Superseded by`** — **delete its index line entirely.** The file stays on disk; it just stops being something the index offers to open. Anyone who needs it reaches it from the `[[...]]` reference in the ADR that superseded it.

Verify afterwards:

```bash
# index line count == ADR files minus superseded ones
grep -cE '^- \*\*\[[0-9]{4}\]' docs/adr/README.md
ls docs/adr/[0-9]*.md | wc -l
grep -lE '^Superseded by ' docs/adr/[0-9]*.md | wc -l   # 行頭一致。本文で語彙として言及しているADRを拾わないため

# every index link resolves
grep -ohE '\]\([0-9]{4}[^)]*\.md\)' docs/adr/README.md | sed 's/](//; s/)//' | while read f; do test -f "docs/adr/$f" || echo "MISSING: $f"; done
```

## B-3: Where the pointer goes — `docs/design/`, never `CLAUDE.md`

The `docs/design/` file covering this mechanism gets **at most one line**: what it is, plus the ADR number. Not the alternatives, not the reasoning, not "we initially tried X but". If you want to write a second sentence of "why" outside `docs/adr/`, that sentence belongs in the ADR (or in a Consequences bullet you missed) — go add it there.

**Put no ADR number in `CLAUDE.md`.** It is loaded in full every session, but nobody needs an ADR number at that moment — the number is wanted later, when someone is questioning or changing the mechanism, and what they are reading *then* is the topic's design doc or the code. A number in `CLAUDE.md` is therefore paid for in every session, delivered at the wrong moment, and becomes a third place that has to stay in sync with `docs/adr/`. `CLAUDE.md` already routes "why" questions to `docs/adr/README.md`; that one rule replaces every inline number.

A pointer in a **code comment** is fine, and is read at exactly the right moment.

**Example** — from masuda's own history:

*Before (prose carrying the "why" inline, wrong):*
> `create <branch> [--base]`: 新規ワークスペースID発行＋`git clone --local`によるローカルクローン方式（`git worktree add`ではない）。理由: linked worktreeは対象worktree自身の絶対パスとメインリポジトリの`.git/worktrees/<name>`が双方向に絶対パス参照し合う構造で、`/workspace`のような別パスへのbind mountに耐えられないことが実機で判明したため

*After (one line in the design doc covering workspaces):*
> `create <branch> [--base]`: 新規ワークスペースID発行＋`git clone --local`によるローカルクローン方式（ADR-0018、`git worktree add`ではない）

...with the full reasoning in `docs/adr/0018-git-clone-local-over-linked-worktree.md`.
