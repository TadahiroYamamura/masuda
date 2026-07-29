---
name: adr-author
description: Author and maintain masuda's ADRs (docs/adr/, numbered Architecture Decision Records). Use this whenever the user asks to write, draft, or record an ADR, or says something was "decided"/"検討した結果〜にした" about masuda's design. Also use it PROACTIVELY, before writing into CLAUDE.md, docs/design/, or a code comment, whenever the content you're about to write explains why one implementation approach was chosen over a real rejected alternative — masuda's CLAUDE.md has repeatedly drifted into an implementation log because this kind of content kept landing there instead of in an ADR, so treat this skill as a required checkpoint, not an optional one, whenever you notice yourself narrating a trade-off outside docs/adr/.
---

# Authoring ADRs for masuda

## Why this skill exists

masuda keeps four kinds of project memory, each with a distinct job:

- `docs/adr/`: **decisions** — a choice made among real alternatives, with the rejected alternatives and why. Immutable once written.
- `CLAUDE.md`: **orientation** — what an agent needs to find its way around the project right now. Not a place for "why," only "what" plus a pointer to the ADR that has the "why."
- `git log`: **history** — what happened, in what order, and the reasoning behind each specific change (via the `意図`/`設計上の考慮点`/`懸念事項` commit format).
- code comments: **local context** — why *this specific line or function* looks the way it does, scoped to the file it's in.

The recurring failure this skill guards against: writing decision-with-alternatives content (the kind that belongs in an ADR) as prose into CLAUDE.md instead. It happened repeatedly before this skill existed — e.g. the rationale for `git clone --local` over `git worktree add`, and for `inotifywait` over a `while` loop or the Monitor tool, both sat in CLAUDE.md prose for a long time with no ADR at all. Once CLAUDE.md absorbs enough of these, it stops being a quick orientation read and turns into an implementation log nobody wants to read end-to-end — which is exactly the failure mode being prevented here.

## Step 1: Check whether this is actually ADR material

Ask two questions about the content in front of you:

1. **Is there a real alternative that was seriously considered and rejected?** Not a hypothetical strawman — something that was genuinely on the table, with a concrete reason it lost out.
2. **Is the "why" invisible from just reading the current code?** If someone reading the relevant file(s) today would immediately see the answer, it isn't ADR (or CLAUDE.md) material at all — let the code speak for itself.

Both "yes" → this is an ADR. Write one (Step 2 onward).

If it's a **workaround for something broken or quirky in an external tool**, with no real alternative being weighed — e.g. "Claude Code's permission rule needs a doubled leading slash for a true absolute-path anchor, because of how it parses single slashes" — that's not a decision, it's a gotcha. It belongs in CLAUDE.md (terse, as a fact) or a code comment, not an ADR. Don't force it into ADR shape.

If it's **the story of a bug that was found and already fixed**, with nothing left to decide going forward, that belongs in the commit message (`意図`/`設計上の考慮点`/`懸念事項`), not in an ADR or in CLAUDE.md. The fix itself is the record; a prose retelling next to it is redundant.

If you're mid-way through writing CLAUDE.md, `docs/design/`, or a code comment and you notice the sentence you're about to write contains "〜という理由で", "〜ではなく〜を採用した", "検討した結果", "当初は〜だったが" (or the English equivalents — "instead of", "we chose X because", "we considered Y but"), stop and run this check before finishing the sentence.

## Step 2: Check for overlap with existing ADRs

Before drafting anything, search `docs/adr/` for related ground:

```bash
grep -rl "<relevant keyword>" docs/adr/
ls docs/adr/
```

Read any hits in full. Three outcomes:

- **Already fully covered** — don't write a new ADR. Point the calling context (CLAUDE.md, the user's question, wherever this came up) at the existing ADR number instead.
- **Adjacent but not the same decision** (e.g. an existing ADR covers *when* worktrees are created, this one covers *how*) — write a new ADR, and cross-reference the related one in Context via `[[NNNN-filename-without-extension]]` rather than re-explaining its Context.
- **Not covered at all** — write a new ADR from scratch.

This step matters: two ADRs is a real repo, once discovered it fully replaces the CLAUDE.md paragraph that used to carry this instead.

## Step 3: Pick the file

`ls docs/adr/` and take the highest existing `NNNN`, then use `NNNN + 1`, zero-padded to 4 digits. Filename: `NNNN-kebab-case-summary-of-the-decision.md` — name it after the decision (e.g. `0018-git-clone-local-over-linked-worktree.md`), not the topic area.

## Step 4: Write it using the established template

Every existing ADR in `docs/adr/` follows this exact shape — match it precisely, headers included:

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

Look at 2-3 existing ADRs in `docs/adr/` before writing (e.g. the ones Step 2 turned up, or any recent one like `0017`/`0018`) to calibrate tone and level of detail — they run fairly detailed (a paragraph or two of Context, concrete Alternatives), not one-liners.

## Step 5: Never edit an existing ADR

Every ADR in this repo has exactly one commit against it, ever — confirmed via `git log --oneline -- docs/adr/*.md`. Treat this as a hard rule: once an ADR file exists, don't edit it, not even to fix a typo in a way that changes meaning, and never to update its Status when a later decision reverses it.

If a new decision changes or reverses an earlier one, write a **new** ADR. Explain the change in the new ADR's Context (referencing the old one via `[[NNNN-filename]]`), and let CLAUDE.md's existing instruction ("ADRは番号順に読むと議論の経緯が追える") carry the reader from the old decision to the new one. The old ADR stays exactly as it was — it's a record of what was true and decided *then*, not a living document.

## Step 6: Leave CLAUDE.md (or wherever this came up) with a pointer, not a summary

Once the ADR exists, anything else that needs to mention this decision gets **at most one line**: what it is plus the ADR number. Not the alternatives, not the reasoning, not "we initially tried X but". If you find yourself wanting to write a second sentence of "why" outside `docs/adr/`, that's the content that belongs in the ADR you just wrote (or a Consequences bullet you missed) — go add it there instead.

**Example** — before/after from masuda's own history:

*Before (what used to sit in CLAUDE.md, wrong):*
> `create <branch> [--base]`: 新規ワークスペースID発行＋`git clone --local`によるローカルクローン方式（`git worktree add`ではない）。理由: linked worktreeは対象worktree自身の絶対パスとメインリポジトリの`.git/worktrees/<name>`が双方向に絶対パス参照し合う構造で、`/workspace`のような別パスへのbind mountに耐えられないことが実機で判明したため

*After (CLAUDE.md today):*
> `create <branch> [--base]`: 新規ワークスペースID発行＋`git clone --local`によるローカルクローン方式（ADR-0018、`git worktree add`ではない）

...with the full reasoning moved into `docs/adr/0018-git-clone-local-over-linked-worktree.md`.
