# 11. Search commit messages and diffs

**Seen in:** Sourcegraph (`type:commit`, `type:diff`, with `author:`, `before:`, `after:`).

## Problem
There is no way to find when a line of code was introduced or removed, or which commit mentioned a bug number, without leaving the app.

## Proposal
- `type:commit <regexp>`: search commit messages (`git log --grep`), across the repositories selected by `repo:`.
- `type:diff <regexp>`: search added/removed lines (`git log -G`), showing matching hunks.
- Filters: `author:`, `before:`, `after:`, `file:`.
- Results link to the commit view already used by the History panel.

Runs `git` on demand with time and result limits, in parallel across repositories.

## Acceptance criteria
- `type:commit crbug` lists matching commits with author, date and subject.
- `type:diff writeVersion repo:codesearch` shows the hunks that added or removed `writeVersion`.
- Slow repositories time out individually without failing the whole search.
