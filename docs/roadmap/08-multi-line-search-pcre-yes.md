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

## Status: done

- `multiline:yes` (or `pcre:yes`) compiles terms with Go's `regexp` in `(?ms)`
  mode and runs them over whole candidate files; the trigram index still
  narrows the candidates. Matches are capped at 100 per file and 20 lines each.
- Each line of a match is shown as a result line with its part of the match
  highlighted, so multi-line matches read as consecutive lines.
- Patterns cannot contain spaces (they separate terms), so use `\s`; quoted
  terms are literal. The example in the proposal is therefore written
  `multiline:yes func\s\w+\(\)\s*\{\s*return\snil`.
- Normal (single-line) searches use the same code as before.
