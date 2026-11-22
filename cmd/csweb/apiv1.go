// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

// The versioned JSON API (/api/v1/...), documented in docs/api.md.
// Unlike the UI's internal endpoints, it returns plain text, and its
// response shapes are kept stable.

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strconv"

	"github.com/ndesai/codesearch/internal/api"
)

func (s *server) registerAPIv1(mux *http.ServeMux) {
	svc := &api.Service{Engine: s.engine}
	token := s.engine.Config().APIToken
	handle := func(path string, h func(w http.ResponseWriter, r *http.Request) (any, error)) {
		mux.HandleFunc("/api/v1/"+path, func(w http.ResponseWriter, r *http.Request) {
			if token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
				writeError(w, http.StatusUnauthorized, errors.New("missing or wrong API token"))
				return
			}
			if r.Method != http.MethodGet {
				writeError(w, http.StatusMethodNotAllowed, errors.New("GET required"))
				return
			}
			v, err := h(w, r)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, v)
		})
	}
	num := func(r *http.Request, k string) int {
		n, _ := strconv.Atoi(r.FormValue(k))
		return n
	}
	handle("repos", func(w http.ResponseWriter, r *http.Request) (any, error) {
		return map[string]any{"repos": svc.Repos()}, nil
	})
	handle("search", func(w http.ResponseWriter, r *http.Request) (any, error) {
		return svc.Search(r.Context(), r.FormValue("q"), num(r, "limit"), num(r, "offset"))
	})
	handle("file", func(w http.ResponseWriter, r *http.Request) (any, error) {
		return svc.ReadFile(r.FormValue("repo"), r.FormValue("path"), num(r, "start"), num(r, "end"))
	})
	handle("definitions", func(w http.ResponseWriter, r *http.Request) (any, error) {
		defs, err := svc.Definitions(r.Context(), r.FormValue("symbol"), r.FormValue("repo"), r.FormValue("path"))
		return map[string]any{"symbol": r.FormValue("symbol"), "definitions": defs}, err
	})
	handle("references", func(w http.ResponseWriter, r *http.Request) (any, error) {
		refs, total, err := svc.References(r.Context(), r.FormValue("symbol"), r.FormValue("repo"), r.FormValue("path"), num(r, "limit"))
		return map[string]any{"symbol": r.FormValue("symbol"), "references": refs, "total": total}, err
	})
	handle("outline", func(w http.ResponseWriter, r *http.Request) (any, error) {
		syms, err := svc.Outline(r.FormValue("repo"), r.FormValue("path"))
		return map[string]any{"repo": r.FormValue("repo"), "path": r.FormValue("path"), "symbols": syms}, err
	})
}
