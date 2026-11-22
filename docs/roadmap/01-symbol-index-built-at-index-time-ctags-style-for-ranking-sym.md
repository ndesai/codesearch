# 1. Symbol index built at index time (ctags-style) for ranking, sym: and cross references

**Seen in:** Zoekt and Sourcebot (ctags-based symbol ranking), GitHub and Sourcegraph symbol search.

## Problem
Definitions are currently recognized by matching regular expressions against each candidate line at query time. That works across every language, but it has false positives (for example `class Foo;` forward declarations in C++ are reported as definitions of `Foo`), and ranking can only boost definitions it happens to classify while scanning.

## Proposal
Extract symbols (name, kind, line, enclosing scope) for every file while indexing and store them per shard, next to the trigram index, updated incrementally with the same delta logic as the shard itself.

- Reuse `internal/outline` as the extractor (it already understands ~25 languages), optionally using Universal Ctags when it is installed for higher accuracy.
- Use the symbol table for `sym:` queries, for the Definitions section of the cross-reference panel, and as a ranking signal (boost files that *declare* a matched identifier).
- Exclude forward declarations and other non-definitions from the definition list.

## Acceptance criteria
- `sym:RenderFrameHostImpl` on Chromium no longer lists `class RenderFrameHostImpl;` forward declarations.
- Incremental updates keep the symbol table in sync (extend the existing incremental-vs-full equivalence test).
- Index size grows by no more than ~15%.

## Status: done

- Symbols are extracted with `internal/outline` in the indexer's parallel file
  readers and stored per shard in a `.sym` table (`internal/symbols`): 12-byte
  records sorted by name, memory-mapped for lookups. Incremental updates merge
  the old table with the changed files' symbols; the incremental-vs-full test
  compares symbol tables too.
- `sym:` queries and cross references use the table; files containing table
  definitions are searched first, so definitions are no longer lost to the
  candidate limit. Heuristics still find fields, constants and the like.
- Forward declarations (`class Foo;`, `friend class Foo;`) and constructor
  initializer lines are no longer definitions. On Chromium,
  `sym:RenderFrameHostImpl` went from 120 files to 5.
- Cost on Chromium (802k files): full index 21 s → 27–29 s; symbol tables add
  112 MB to a 489 MB index (+23%, above the 15% target: Chromium has ~6M
  symbols). Universal Ctags support was not added.
