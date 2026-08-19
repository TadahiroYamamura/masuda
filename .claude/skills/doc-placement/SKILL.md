---
name: doc-placement
description: Decide where a piece of masuda project knowledge belongs — docs/design/, docs/adr/, a code comment, CLAUDE.md, or the commit message. Use BEFORE writing, whenever you are about to add content to CLAUDE.md or to a file under docs/design/, whenever the sentence you are about to write explains why one approach was chosen over another that was genuinely considered ("〜という理由で", "〜ではなく〜を採用した", "検討した結果", "当初は〜だったが", "instead of", "we chose X because", "we considered Y but"), or whenever you are unsure which of those five places something belongs in. masuda's CLAUDE.md has repeatedly grown into an implementation log because this kind of content kept landing there instead, so treat this as a required checkpoint, not an optional one. When the answer is "this is an ADR", hand off to the adr-author skill.
---

# Where does this belong?

## masuda's five kinds of project memory

Each has one job. Content in the wrong one is the failure this skill prevents.

| | job | when it is read |
|---|---|---|
| `docs/adr/` | **decisions** — a choice made among real alternatives, with the rejected ones and why | when someone asks "why is it like this?", or is about to change it |
| `docs/design/` | **the current design** — how each part works today, per topic | on demand, before touching that part |
| `CLAUDE.md` | **orientation and routing** — what the project is, and where to look for anything deeper | in full, at the start of every session |
| `git log` | **history** — what happened in what order, and the reasoning for each specific change (`意図`/`設計上の考慮点`/`懸念事項`) | when tracing when and why something changed |
| code comments | **local context** — why *this specific line or function* looks the way it does | whenever that file is open |

The load-bearing consequence: **`CLAUDE.md` is paid for in every session, so nothing goes there that is only needed at some specific later moment.** Not reasoning, not ADR numbers, not traps that only matter when touching one particular area. It routes; the artifact it routes to carries the content.

## The two questions

1. **Is there a real alternative that was seriously considered and rejected?** Not a hypothetical strawman — something genuinely on the table, with a concrete reason it lost.
2. **Is the "why" invisible from just reading the current code?** If someone opening the relevant file today would immediately see the answer, let the code speak for itself.

**Both yes → this is an ADR.** Invoke the `adr-author` skill and stop writing wherever you were writing.

## If not both yes

Work down this list; the first match wins.

- **A workaround for something broken or quirky in an external tool, with no alternative weighed** — e.g. "Claude Code's permission rule needs a doubled leading slash for a true absolute-path anchor". That is a **gotcha**, not a decision. It goes:
  1. **as a comment on the artifact itself**, if it is about one file, function, or build step — `go:embed` cannot reach `..` belongs in the file with the `//go:embed` directive; a Dockerfile naming trap belongs in that Dockerfile. Read at exactly the moment it bites, and it moves with the thing it describes, so it cannot go stale.
  2. **in `docs/design/`**, only if it spans several files or is a workflow step ("after editing `orchestrator/`, rebuild both the base and the variant image").
  3. **never in `CLAUDE.md`.** A gotcha is a trap you do not know to look for, which argues for putting it somewhere always-loaded — but the person about to trip it is reading the file or the topic doc, not `CLAUDE.md`, and `CLAUDE.md` cannot hold every trap as the project grows.

- **The story of a bug that was found and already fixed**, with nothing left to decide going forward — the **commit message** (`意図`/`設計上の考慮点`/`懸念事項`). The fix is the record; a prose retelling next to it is redundant.

- **How something works today, with no decision behind it** — `docs/design/`, in the topic file.

- **Where to find something** — `CLAUDE.md`, as routing. One line, pointing at the file that has the content.

- **A decision that an existing ADR already covers** — do not write a second one. Leave a one-line pointer (`（ADR-NNNN）`) in the `docs/design/` file that describes the mechanism, and put nothing in `CLAUDE.md`. Search first: `grep -rl "<keyword>" docs/adr/` and the index at `docs/adr/README.md`.

## The specific failure this guards against

Before this checkpoint existed, decision-with-alternatives content repeatedly landed in `CLAUDE.md` as prose — the rationale for `git clone --local` over `git worktree add`, and for `inotifywait` over a `while` loop, both sat there for a long time with no ADR at all. Once `CLAUDE.md` absorbs enough of these it stops being a quick orientation read and becomes an implementation log nobody reads end to end, which is the same thing as having no orientation document.
