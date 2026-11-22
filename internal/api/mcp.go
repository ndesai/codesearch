// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api

// A Model Context Protocol (MCP) server exposing code search as tools,
// so that AI coding agents can search and read the indexed repositories.
// It speaks JSON-RPC 2.0; csmcp carries it over stdio (one message per
// line) or HTTP.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// MCPProtocolVersion is the protocol version this server implements.
const MCPProtocolVersion = "2025-06-18"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	run         func(ctx context.Context, s *Service, args map[string]any) (string, error)
}

func schema(required []string, props map[string]any) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }

const queryHelp = `Query syntax: terms are regular expressions (all must appear in a file); "quoted text" is literal; -term excludes; ` +
	`foo OR bar; file:REGEXP or file:GLOB (e.g. file:src/**/*.kt); lang:LANGUAGE; repo:NAME; sym:Identifier finds definitions; ` +
	`class:REGEXP / function:REGEXP list definitions by name; usage:/comment:/string: restrict where a term matches; ` +
	`-is:test excludes tests; case:yes for case-sensitive (default case-insensitive).`

var tools = []*tool{
	{
		Name:        "search_code",
		Description: "Search the indexed repositories for code. Returns matching lines as repo:path:line: text, best matches first. " + queryHelp,
		InputSchema: schema([]string{"query"}, map[string]any{
			"query":  str("The search query."),
			"limit":  num(fmt.Sprintf("Maximum number of files to return (default %d, at most %d).", DefaultLimit, MaxLimit)),
			"offset": num("Number of files to skip, for paging."),
		}),
		run: func(ctx context.Context, s *Service, a map[string]any) (string, error) {
			r, err := s.Search(ctx, argStr(a, "query"), argInt(a, "limit"), argInt(a, "offset"))
			if err != nil {
				return "", err
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%d matching lines in %d files", r.NumMatches, r.NumFiles)
			if r.Truncated {
				b.WriteString(" (search stopped early; refine the query)")
			}
			fmt.Fprintf(&b, "; showing %d files.\n", len(r.Files))
			for _, f := range r.Files {
				fmt.Fprintf(&b, "\n%s:%s (%s, %d matches)\n", f.Repo, f.Path, f.Lang, f.NumMatches)
				for _, m := range f.Matches {
					def := ""
					if m.Def {
						def = " [definition]"
					}
					fmt.Fprintf(&b, "  %d:%s %s\n", m.Line, def, strings.TrimSpace(m.Text))
				}
			}
			return b.String(), nil
		},
	},
	{
		Name:        "read_file",
		Description: fmt.Sprintf("Read a file from an indexed repository, with line numbers (at most %d lines per call), or list a directory.", MaxReadLines),
		InputSchema: schema([]string{"repo", "path"}, map[string]any{
			"repo":       str("Repository name, as returned by list_repos or search_code."),
			"path":       str("Path within the repository, e.g. src/main.go; empty for the root directory."),
			"start_line": num("First line to return (1-based, default 1)."),
			"end_line":   num("Last line to return (default: the end of the file)."),
		}),
		run: func(ctx context.Context, s *Service, a map[string]any) (string, error) {
			f, err := s.ReadFile(argStr(a, "repo"), argStr(a, "path"), argInt(a, "start_line"), argInt(a, "end_line"))
			if err != nil {
				return "", err
			}
			var b strings.Builder
			switch {
			case f.Kind == "dir":
				fmt.Fprintf(&b, "Directory %s:%s/\n", f.Repo, f.Path)
				for _, e := range f.Entries {
					if e.Dir {
						fmt.Fprintf(&b, "  %s/\n", e.Name)
					} else {
						fmt.Fprintf(&b, "  %s (%d bytes)\n", e.Name, e.Size)
					}
				}
			case f.Binary:
				fmt.Fprintf(&b, "%s:%s is a binary file (%d bytes).\n", f.Repo, f.Path, f.Size)
			default:
				last := f.StartLine + len(f.Lines) - 1
				fmt.Fprintf(&b, "%s:%s (%s), lines %d-%d of %d\n", f.Repo, f.Path, f.Lang, f.StartLine, last, f.TotalLines)
				for i, l := range f.Lines {
					fmt.Fprintf(&b, "%6d\t%s\n", f.StartLine+i, l)
				}
				if last < f.TotalLines {
					fmt.Fprintf(&b, "(%d more lines; use start_line=%d to continue)\n", f.TotalLines-last, last+1)
				}
			}
			return b.String(), nil
		},
	},
	{
		Name:        "find_definitions",
		Description: "Find where an identifier (function, type, variable, ...) is defined. Optionally give the file you are looking at (repo and path) to rank nearby definitions first.",
		InputSchema: schema([]string{"symbol"}, map[string]any{
			"symbol": str("The identifier, e.g. NewReader."),
			"repo":   str("Repository of the file the symbol was seen in (optional)."),
			"path":   str("Path of the file the symbol was seen in (optional)."),
		}),
		run: func(ctx context.Context, s *Service, a map[string]any) (string, error) {
			defs, err := s.Definitions(ctx, argStr(a, "symbol"), argStr(a, "repo"), argStr(a, "path"))
			if err != nil {
				return "", err
			}
			if len(defs) == 0 {
				return "No definitions found for " + argStr(a, "symbol") + ".", nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%d definitions of %s:\n", len(defs), argStr(a, "symbol"))
			for _, d := range defs {
				fmt.Fprintf(&b, "%s:%s:%d: %s\n", d.Repo, d.Path, d.Line, strings.TrimSpace(d.Text))
			}
			return b.String(), nil
		},
	},
	{
		Name:        "find_references",
		Description: "Find uses of an identifier (excluding its definitions), nearest to the given file first.",
		InputSchema: schema([]string{"symbol"}, map[string]any{
			"symbol": str("The identifier."),
			"repo":   str("Repository of the file the symbol was seen in (optional)."),
			"path":   str("Path of the file the symbol was seen in (optional)."),
			"limit":  num("Maximum number of references to return (default 50)."),
		}),
		run: func(ctx context.Context, s *Service, a map[string]any) (string, error) {
			refs, total, err := s.References(ctx, argStr(a, "symbol"), argStr(a, "repo"), argStr(a, "path"), argInt(a, "limit"))
			if err != nil {
				return "", err
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%d references to %s", total, argStr(a, "symbol"))
			if len(refs) < total {
				fmt.Fprintf(&b, " (showing %d)", len(refs))
			}
			b.WriteString(":\n")
			for _, r := range refs {
				fmt.Fprintf(&b, "%s:%s:%d: %s\n", r.Repo, r.Path, r.Line, strings.TrimSpace(r.Text))
			}
			return b.String(), nil
		},
	},
	{
		Name:        "get_outline",
		Description: "List the classes, functions and methods defined in a file, with line numbers.",
		InputSchema: schema([]string{"repo", "path"}, map[string]any{
			"repo": str("Repository name."),
			"path": str("Path of the file within the repository."),
		}),
		run: func(ctx context.Context, s *Service, a map[string]any) (string, error) {
			syms, err := s.Outline(argStr(a, "repo"), argStr(a, "path"))
			if err != nil {
				return "", err
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%d symbols in %s:%s\n", len(syms), argStr(a, "repo"), argStr(a, "path"))
			for _, sym := range syms {
				fmt.Fprintf(&b, "%s%s %s (line %d)\n", strings.Repeat("  ", sym.Depth), sym.Kind, sym.Name, sym.Line)
			}
			return b.String(), nil
		},
	},
	{
		Name:        "list_repos",
		Description: "List the indexed repositories with their file counts.",
		InputSchema: schema(nil, map[string]any{}),
		run: func(ctx context.Context, s *Service, a map[string]any) (string, error) {
			var b strings.Builder
			for _, r := range s.Repos() {
				state := "not indexed yet"
				if r.Indexed {
					state = fmt.Sprintf("%d files, %d bytes", r.Files, r.Bytes)
				}
				fmt.Fprintf(&b, "%s: %s (%s)\n", r.Name, r.Root, state)
			}
			return b.String(), nil
		},
	},
}

func argStr(a map[string]any, k string) string {
	s, _ := a[k].(string)
	return s
}

func argInt(a map[string]any, k string) int {
	switch v := a[k].(type) {
	case float64:
		return int(v)
	case string:
		var n int
		fmt.Sscanf(v, "%d", &n)
		return n
	}
	return 0
}

// HandleMCP processes one JSON-RPC message and returns the response to
// send, or nil for notifications.
func (s *Service) HandleMCP(ctx context.Context, msg []byte) []byte {
	var req rpcRequest
	if err := json.Unmarshal(msg, &req); err != nil {
		return marshal(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
	}
	if len(req.ID) == 0 {
		return nil // a notification, such as notifications/initialized
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		version := MCPProtocolVersion
		if p.ProtocolVersion != "" && p.ProtocolVersion <= MCPProtocolVersion {
			version = p.ProtocolVersion
		}
		resp.Result = map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "nd-code-search", "title": "ND Code Search", "version": "1.0.0"},
			"instructions": "Search and read the code in the user's indexed repositories. Use search_code to find code, " +
				"find_definitions and find_references to navigate, and read_file to read files.",
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": tools}
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			resp.Error = &rpcError{-32602, "invalid params"}
			break
		}
		var t *tool
		for _, x := range tools {
			if x.Name == p.Name {
				t = x
			}
		}
		if t == nil {
			resp.Error = &rpcError{-32602, "unknown tool: " + p.Name}
			break
		}
		if p.Arguments == nil {
			p.Arguments = map[string]any{}
		}
		text, err := t.run(ctx, s, p.Arguments)
		if err != nil {
			// Tool errors are results the model can see and act on.
			resp.Result = map[string]any{"content": []map[string]any{{"type": "text", "text": "Error: " + err.Error()}}, "isError": true}
			break
		}
		resp.Result = map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": false}
	default:
		resp.Error = &rpcError{-32601, "method not found: " + req.Method}
	}
	return marshal(resp)
}

func marshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
