# 5. is:test, is:generated and is:vendored filters

**Seen in:** GitHub code search (`is:generated`, `is:vendored`).

## Problem
Tests, generated code and third-party code are currently only ranked lower. Users often want to remove them entirely, or search only them.

## Proposal
Add `is:test`, `is:generated`, `is:vendored` (and `-is:...` / `NOT is:...`), classified by path and content heuristics:
- test: `_test.go`, `*Test.java`, `test/`, `tests/`, `testdata/`, `__tests__/`, `*.spec.ts`, ...
- vendored: `third_party/`, `external/`, `vendor/`, `node_modules/`, `prebuilts/`
- generated: `*.pb.go`, `*_pb2.py`, `*.g.dart`, files whose first lines say "Code generated ... DO NOT EDIT" or "@generated".

Share the classification with the existing ranking penalties so the two never disagree, and add the categories to the filter sidebar.

## Acceptance criteria
- `NewReader -is:test` excludes `*_test.go` files.
- The content-based "generated" check reads only the first few lines of candidate files.
