# 9. Call hierarchy (who calls this, what does this call)

**Seen in:** Google Code Search (call hierarchy for C++/Java via Kythe).

## Problem
The cross-reference panel lists references, but not which *functions* those references are in, so tracing call paths takes many clicks.

## Proposal
A "Callers" view for a function symbol: for each reference that looks like a call (`name(`), find the enclosing function using the outline extractor and group references by caller. Each caller can be expanded to show its own callers, giving a lazily loaded tree. A "Callees" view lists calls made inside the function body.

This is heuristic, like the existing cross references; label it as such.

## Acceptance criteria
- Callers of `bufio.(*Reader).ReadSlice` in the Go tree show `ReadLine`, `ReadBytes` and so on, each expandable.
- Depth and fan-out are capped so the view stays fast on Chromium-sized trees.

## Status: done

- `/api/callers`: files from the trigram index for `name\s*\(`, limited to the
  viewed file's language family and nearest first (at most 2,000 files, 50 calls
  per file, 200 callers); calls in comments and strings, and definitions or
  declarations, are skipped. Each call is grouped under the function whose
  definition precedes it in the file's outline.
- `/api/callees`: the calls in the function's body (up to the next symbol at
  the same depth, at most 500 lines), excluding keywords, each resolved to the
  nearest function definition in the symbol index.
- UI: **Callers** and **Callees** buttons in the cross-reference panel open a
  tree; expanding a caller shows its call sites and its own callers, expanding a
  callee shows what it calls.
- Heuristic, as stated in the proposal: calls are matched by name, so callers of
  other functions with the same name in the same language appear too (ranked
  after nearer ones).
