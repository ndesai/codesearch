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

## Status: done

- `class:` (types of every kind), `function:`/`func:` (functions, methods,
  macros, RPCs) and `symbol:` (any kind) take a regular expression matched
  against whole symbol names, case-insensitive by default. Results come from
  the symbol index: each distinct name is tested once per shard, and only the
  result files are read, to show their lines.
- Qualified names (`Reader.ReadSlice`, `Rect::intersect`) match when the
  enclosing symbol (by outline nesting) or a word before the name on the
  definition line (a Go receiver, `Rect::`) is the scope.
- `symbol:` now means this regular-expression filter; `sym:` (and `def:`)
  still look up the definitions of an exact identifier.
- Several filters combine with `OR`; mixing them with text terms is an error.
  Results are capped at 2,000 definitions.
- On Chromium, `class:WebContents.*Impl lang:c++` returns the 5 class
  definitions in ~140 ms; qualified lookups take up to ~0.7 s.
