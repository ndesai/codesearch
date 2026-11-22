# 14. Optional compiler-accurate cross references from SCIP indexes

**Seen in:** Sourcegraph (SCIP/LSIF precise code navigation), Google Code Search (Kythe).

## Problem
Search-based cross references are fast and language-agnostic, but they cannot distinguish overloads, methods with the same name on different types, or symbols reached through imports and macros.

## Proposal
Allow importing [SCIP](https://github.com/sourcegraph/scip) indexes produced by existing indexers (scip-go, scip-java, scip-typescript, scip-clang, scip-python, ...):
- a per-repository `scip:` path in the configuration (or a drop-in directory),
- precise definitions and references used when a SCIP index covers the file, falling back to the current heuristics otherwise,
- the UI marks which results are precise.

## Acceptance criteria
- With a scip-go index of this repository, clicking `Merge` in `index/merge.go` lists exactly its real callers.
- Files without SCIP data behave as today.
- Stale SCIP data (file changed since the index was built) is detected and not trusted for that file.
