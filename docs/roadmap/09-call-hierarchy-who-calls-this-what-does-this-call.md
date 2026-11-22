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
