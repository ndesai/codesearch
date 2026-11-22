# 2. Hover cards with the definition and doc comment of an identifier

**Seen in:** Sourcegraph, GitHub code navigation.

## Problem
To learn what an identifier is, you currently click it and read the cross-reference panel. A quick look should not require opening the panel.

## Proposal
Hovering an identifier in the file view (after a short delay) shows a small card with:
- the first definition's line (syntax highlighted) and file path,
- the doc comment immediately above the definition, if any,
- links: "Go to definition", "Find references".

Implementation notes: add an `/api/hover?repo&path&sym` endpoint that runs the cross-reference definition lookup (or the symbol index from [#1](01-symbol-index-built-at-index-time-ctags-style-for-ranking-sym.md) once it exists) and returns the definition plus the preceding comment block. Cache results per symbol for the page.

## Acceptance criteria
- Hover card appears within ~300 ms for a typical symbol on a warm index and never blocks scrolling or selection.
- Works with the keyboard as well (e.g. a key to show the card for the identifier under the caret) and is disabled on touch devices.

## Status: done

- `/api/hover` looks the symbol up in the symbol tables (falling back to
  search-based cross references for fields and constants), ranks definitions
  by proximity to the current file with types ahead of same-named
  constructors, and returns the signature (continued over lines while
  parentheses are open) and the doc comment above it, or a Python docstring.
- The card appears after a 350 ms pause; the API itself answers in about
  3–12 ms on Chromium. It hides when the pointer leaves, on scroll, click or
  Escape, and is disabled on devices without a fine pointer. `i` shows the
  card for a selected identifier.
