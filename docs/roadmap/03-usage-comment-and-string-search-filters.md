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
