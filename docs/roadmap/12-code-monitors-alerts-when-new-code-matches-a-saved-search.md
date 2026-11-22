# 12. Code monitors: alerts when new code matches a saved search

**Seen in:** Sourcegraph code monitors.

## Problem
Saved searches show current results, but nothing tells you when new matches appear (a deprecated API being used again, a TODO with your name, a risky pattern).

## Proposal
- Mark a saved search as "monitored".
- After each background index update, re-run monitored searches restricted to files that changed in that update (the indexer already knows them), and record new matches.
- Show new matches on the home page and as a badge on the saved search; optionally POST them to a configurable webhook (Slack, ntfy, a script).

## Acceptance criteria
- Adding a file that matches a monitored search produces exactly one new-match entry after the next update.
- Monitors only scan changed files, so they add negligible cost to an incremental update.

## Status: done

- The manifest now records the files each incremental update added or changed
  (up to 10,000; older manifests still load). When `csweb` picks up a new index
  snapshot (from its own background updates or from `csindex`), it re-runs the
  monitored saved searches on just those files.
- New matches become alerts on the saved search (newest first, up to 100) with
  an unseen count, shown on the home page with links to the matching lines;
  "Mark all seen" clears the count.
- `monitor_webhook` in the configuration receives a JSON POST per search with
  new matches.
- Only code searches can be monitored (not `type:commit` or `class:` searches),
  and full rebuilds don't raise alerts, since everything would be "new".
