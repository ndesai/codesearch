# 7. Recency-aware ranking

**Seen in:** Zoekt (file update time as a ranking signal), Sourcegraph (personalized ranking).

## Problem
Results are ranked by match quality, definitions and path heuristics only. Recently edited code is usually what you are looking for, and abandoned code rarely is.

## Proposal
Use the modification times already stored in each repository's manifest as a mild ranking boost (decaying over weeks to months), and optionally the last git commit time of the file. Keep the boost small enough that a definition or file-name match still wins.

## Acceptance criteria
- Between two otherwise equal matches, the more recently modified file ranks first.
- No extra disk reads at query time (use the manifest data loaded with the shard).
- A config option to turn it off.
