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

## Status: done (validated with synthetic indexes only)

- `scip:` on a repository names a `.scip` file or a directory of them.
  `internal/scip` decodes the parts of the SCIP protobuf format needed for
  navigation (documents, occurrence ranges, symbols, roles) without generated
  code or new dependencies, maps `project_root` into the repository, and keeps
  document-local symbols separate. `csweb` reloads it when the files change.
- The cross-reference request now carries the clicked line; if the index has an
  occurrence of the identifier there, definitions and references come from the
  index and the panel shows "Precise".
- Staleness: a file modified after the index was written is not trusted, so
  its occurrences are left out (and counted), and clicks inside it use search.
- Tested with SCIP indexes encoded in the tests, including two same-named
  methods that search-based results can't tell apart. Not yet checked against a
  real indexer's output: installing and running `scip-go` was not permitted in
  this session. Position encoding is assumed to match byte offsets for the
  identifier itself (true for ASCII identifiers); the occurrence is also found by
  the symbol's name, so this only matters for highlighting.
- Not yet using SCIP: hover cards, the call hierarchy, and the API/MCP tools.
