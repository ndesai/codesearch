# 8. Multi-line search (pcre:yes)

**Seen in:** Google Code Search (`pcre:yes`), Sourcegraph (multi-line structural matching).

## Problem
Searches match one line at a time, so patterns spanning lines (a function signature followed by a specific first statement, a struct with two given fields) cannot be expressed.

## Proposal
- `multiline:yes` (alias `pcre:yes`) makes `.` and `\s` match newlines and lets a pattern span lines, using Go's `regexp` package on the candidate files (the trigram index still narrows candidates using the literal parts of the pattern).
- Show each match as a snippet covering all of its lines, with the match highlighted across lines.
- Cap matches per file and total work, as for normal searches.

## Acceptance criteria
- `multiline:yes "func \w+\(\)\s*\{\s*return nil"` finds functions whose body starts with `return nil`.
- Normal (single-line) searches are unaffected in speed.
