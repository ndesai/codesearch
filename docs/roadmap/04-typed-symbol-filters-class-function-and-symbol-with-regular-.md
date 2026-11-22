# 4. Typed symbol filters: class:, function: and symbol: with regular expressions

**Seen in:** Google Code Search (`class:`, `function:`), GitHub (`symbol:` with regexes and qualifiers).

## Problem
`sym:` finds definitions of an exact identifier only. There is no way to ask for "classes whose name ends in Impl" or "functions named handle*".

## Proposal
- `class:` (also matches struct, interface, enum, trait, ...), `function:`/`func:` (functions and methods), and `symbol:` accept a regular expression over symbol names, e.g. `class:.*Impl$`, `function:^handle`.
- Support qualified names like `Reader.ReadSlice` or `Rect::intersect`.
- Results list each matching definition (kind, name, file, line), ranked by name match quality.

Depends on the symbol index ([#1](01-symbol-index-built-at-index-time-ctags-style-for-ranking-sym.md)); until then it can be implemented with the outline extractor over candidate files.

## Acceptance criteria
- `class:WebContents.*Impl lang:c++` returns class definitions only, not uses.
- Works together with `file:`, `lang:` and `repo:`.
