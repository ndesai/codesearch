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
