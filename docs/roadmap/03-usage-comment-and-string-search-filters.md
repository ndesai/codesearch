# 3. usage:, comment: and string: search filters

**Seen in:** Google Code Search (`usage:`, `comment:`).

## Problem
Searching for an identifier also matches every mention in comments and string literals, which is noisy in large codebases.

## Proposal
Add filters that restrict which part of a line must match:
- `usage:foo` (or `code:`): matches outside comments and string literals,
- `comment:foo`: matches only inside comments,
- `string:foo`: matches only inside string literals.

The trigram index narrows candidate files as today; then, for each matching line, tokenize the file with the same Chroma lexer used for highlighting and keep only matches whose byte range falls in the right token class (comments, strings, or neither). Tokenization only runs on files that already match, and only up to the line limit.

## Acceptance criteria
- `usage:NewReader` in the Go tree excludes doc-comment lines like `// NewReader returns ...`.
- Filters compose with `OR`, `NOT` and grouping.
- Documented in the help dialog and README.

## Status: done

- Implemented with a small single-pass scanner over each candidate file
  (`internal/search/regions.go`) using per-language comment and quote syntax
  (`//`, `/* */`, `#`, `--`, `<!-- -->`, quotes, Go/JS backticks, Python and
  Kotlin triple quotes) rather than full syntax highlighting, which would be
  far slower. Files in languages without known syntax count as all code.
- A line counts for a filtered term only if one of the term's matches on it is
  in the right region; `-comment:x` and other negations work too. `code:` is
  an alias for `usage:`, `str:` for `string:`.
- On Chromium, `usage:WebContentsImpl lang:c++` takes ~210 ms vs ~80 ms
  unfiltered, and drops the 79 files that only mention it in comments or strings.
