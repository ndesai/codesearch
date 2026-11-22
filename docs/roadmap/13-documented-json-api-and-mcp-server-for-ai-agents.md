# 13. Documented JSON API and MCP server for AI agents

**Seen in:** Sourcebot (MCP server), GitLab (proposed MCP access to exact code search).

## Problem
The web app already uses a JSON API (`/api/search`, `/api/file`, `/api/xref`, ...), but it is undocumented and shaped for the UI. AI coding agents and scripts could make good use of a fast index over all of your repositories.

## Proposal
- Document a stable, versioned JSON API (`/api/v1/...`) for search, file contents, directory listings, cross references, outline and blame, returning plain text rather than highlighted HTML.
- Add an MCP server (`csmcp`, stdio and HTTP transports) exposing tools such as `search_code`, `read_file`, `find_definitions`, `find_references` and `list_repos`, reading the same index as `csweb`.
- Optional API token for access beyond localhost.

## Acceptance criteria
- An MCP client can search and read code from the configured repositories.
- API responses are covered by tests so the UI and the API cannot drift apart silently.
