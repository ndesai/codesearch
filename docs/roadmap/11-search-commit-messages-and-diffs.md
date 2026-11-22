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

## Status: done

- `type:commit` and `type:diff`, with `author:`, `before:`/`until:`,
  `after:`/`since:`, `file:` and `repo:`, in every git work tree inside the
  configured folders (from `.repo/project.list` when present, otherwise found by
  scanning a few levels), cached for 10 minutes.
- git's pattern matching is POSIX, so git only gets the longest literal of the
  first term as a prefilter (`--grep` or `-G`, with `-i` when case-insensitive);
  every message and changed line is then checked with the query's RE2
  expressions, so OR, NOT and file filters work as in code search.
- Anchored `file:^dir/` filters skip work trees outside `dir` and are passed to
  git as pathspecs: on this machine `type:diff writeVersion file:^codesearch/`
  went from 20 s (a shallow Chromium clone's single huge commit) to 0.5 s.
- Up to 30 commits per work tree and 200 in total, 16 git processes at once,
  20 s overall; work trees that time out make the result "partial" without
  failing it. Results link to the files at each commit (via #10).
