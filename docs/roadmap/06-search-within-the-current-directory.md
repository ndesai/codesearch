# 6. Search within the current directory

**Seen in:** Google Code Search (`.` in the search box searches the current directory).

## Problem
When browsing a deep directory such as `frameworks/base/core/java/android/app`, restricting a search to it means typing the whole path into a `file:` filter.

## Proposal
- In a file or directory view, typing a query that starts with `.` (or using a "Search in this folder" button next to the breadcrumbs) adds `repo:^<repo>$ file:^<dir>/` to the query.
- The suggestion dropdown offers "Search in <dir>" as its first entry while browsing.

## Acceptance criteria
- From `/f/go/net/http/server.go`, searching `.ServeMux` returns matches under `net/http/` only.
- The added filters are visible and editable in the search box.
