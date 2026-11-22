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

## Status: done

- Modification times come from each repository's manifest, loaded once per
  index snapshot in the background right after it is opened, and stored per
  shard aligned with the index's file ids, so queries do no extra disk reads.
- The boost is 2.5 for a file modified now and halves every 30 days, well below
  the boosts for definitions (20) and file name matches (12–30).
- `rank_recent: false` in the configuration turns it off.
- Cost: loading the times also loads every shard's name table up front; with
  Chromium indexed, `csweb` uses about 430 MB. Git commit times were not used.
