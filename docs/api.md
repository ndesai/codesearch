# API and MCP server

ND Code Search offers two programmatic interfaces over the same index:

- a JSON API served by `csweb` under `/api/v1/`, and
- an [MCP](https://modelcontextprotocol.io) server, `csmcp`, for AI coding
  agents.

Both return plain text (not highlighted HTML) and use the same query syntax as
the web UI.

## Authentication

By default the API is available to anything that can reach `csweb`, which by
default listens on `localhost` only. To require a token, set `api_token` in the
configuration file; requests must then send

    Authorization: Bearer <api_token>

The token applies to `/api/v1/` and to `csmcp -http`. The web UI's own
endpoints are not affected.

## JSON API (`csweb`)

All endpoints use `GET` and return JSON. Errors return a 4xx status with
`{"error": "message"}`.

### `GET /api/v1/repos`

The configured repositories.

```json
{"repos": [{"name": "aosp", "root": "/home/me/aosp", "indexed": true, "files": 802034,
            "bytes": 6227812345, "shards": 33, "updated": "2026-11-22T08:42:51Z"}]}
```

### `GET /api/v1/search?q=QUERY&limit=20&offset=0`

Code search. `limit` is the number of files (default 20, at most 200);
`offset` skips files, for paging. Files are in rank order; for each, `matches`
lists matching lines (definitions first). `ranges` are byte offsets of the
matches within `text`. `class:`, `function:` and `symbol:` queries return
definitions. `type:commit` and `type:diff` are not supported here.

```json
{"query": "NewReader", "numFiles": 397, "numMatches": 1320, "truncated": false,
 "files": [{"repo": "go", "path": "bufio/bufio.go", "language": "Go", "numMatches": 6,
            "matches": [{"line": 59, "text": "func NewReader(rd io.Reader) *Reader {",
                         "ranges": [[5, 14]], "definition": true}]}]}
```

### `GET /api/v1/file?repo=REPO&path=PATH&start=1&end=0`

A file's lines `start`..`end` (1-based, inclusive; `end=0` means the end of the
file), at most 2,000 lines per request, or a directory listing.

```json
{"repo": "go", "path": "bufio/bufio.go", "kind": "file", "language": "Go", "size": 22145,
 "totalLines": 839, "startLine": 59, "lines": ["func NewReader(rd io.Reader) *Reader {", "..."]}
{"repo": "go", "path": "bufio", "kind": "dir",
 "entries": [{"name": "bufio.go", "dir": false, "size": 22145}]}
```

Binary files return `"binary": true` and no lines.

### `GET /api/v1/definitions?symbol=NAME&repo=REPO&path=PATH`

Definitions of an identifier. `repo` and `path` are optional: the file the
symbol was seen in, so that nearby definitions come first.

```json
{"symbol": "NewReader", "definitions": [{"repo": "go", "path": "bufio/bufio.go", "line": 59,
                                          "text": "func NewReader(rd io.Reader) *Reader {"}]}
```

### `GET /api/v1/references?symbol=NAME&repo=REPO&path=PATH&limit=50`

Uses of an identifier (not its definitions), nearest first; `total` counts all
references found.

```json
{"symbol": "NewReader", "total": 412,
 "references": [{"repo": "go", "path": "bufio/bufio_test.go", "line": 60, "text": "..."}]}
```

### `GET /api/v1/outline?repo=REPO&path=PATH`

The classes, functions and methods defined in a file.

```json
{"repo": "go", "path": "bufio/bufio.go",
 "symbols": [{"name": "Reader", "kind": "struct", "line": 32, "depth": 0}]}
```

## MCP server (`csmcp`)

`csmcp` exposes code search as MCP tools:

| Tool | What it does |
|---|---|
| `search_code` | Search with the full query syntax; returns `repo:path` and matching lines |
| `read_file` | Read a file (with line numbers, a range at a time) or list a directory |
| `find_definitions` | Where an identifier is defined |
| `find_references` | Where an identifier is used |
| `get_outline` | The symbols defined in a file |
| `list_repos` | The indexed repositories |

It reads the index directly, so `csweb` does not have to be running, but `csweb`
or `csindex` must keep the index up to date. It takes the same `-config` and
`-index_dir` flags.

### stdio (most clients)

Configure your MCP client to run `csmcp`. For Claude Code:

```sh
claude mcp add nd-code-search -- csmcp
```

For clients configured with JSON (such as Claude Desktop):

```json
{"mcpServers": {"nd-code-search": {"command": "csmcp", "args": []}}}
```

Use the full path to `csmcp` (for example `~/go/bin/csmcp`) if the client does
not inherit your `PATH`.

### HTTP

```sh
csmcp -http localhost:2474
```

Clients send JSON-RPC messages with `POST` to `http://localhost:2474/mcp`. When
listening on localhost, requests addressed to other host names are refused (to
block DNS rebinding); set `api_token` before listening on other addresses.
