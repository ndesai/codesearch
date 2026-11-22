// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

// Code monitors: after each incremental index update, monitored saved
// searches are re-run on just the files the update added or changed,
// and new matches are recorded as alerts (and optionally posted to a
// webhook). Only changed files are searched, so a monitor costs little
// even on very large trees.

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/ndesai/codesearch/internal/search"
)

type monitorAlert struct {
	Time  time.Time   `json:"time"`
	Repo  string      `json:"repo"`
	Path  string      `json:"path"`
	Lines []alertLine `json:"lines,omitempty"`
}

type alertLine struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

const (
	maxAlerts          = 100 // kept per saved search
	maxAlertLines      = 3
	maxMonitorMatches  = 200 // files per search per update
	monitorTimeout     = 30 * time.Second
	webhookTimeout     = 10 * time.Second
	maxAlertLineLength = 300
)

// runMonitors runs the monitored saved searches on the files an update
// of repo changed.
func (s *server) runMonitors(repo string, changed []string) {
	savedMu.Lock()
	list := s.loadSaved()
	savedMu.Unlock()
	only := map[string]map[string]bool{repo: {}}
	for _, p := range changed {
		only[repo][p] = true
	}
	ctx, cancel := context.WithTimeout(context.Background(), monitorTimeout)
	defer cancel()
	now := time.Now()
	found := map[string][]monitorAlert{} // query -> new alerts
	for _, ss := range list {
		if !ss.Monitor {
			continue
		}
		q, err := search.ParseQuery(ss.Query)
		if err != nil || q.Type != "" || len(q.SymFilters) > 0 {
			continue
		}
		res := s.engine.Search(ctx, q, search.Options{Only: only, MaxFiles: maxMonitorMatches})
		for _, f := range res.Files {
			a := monitorAlert{Time: now, Repo: f.Repo, Path: f.Path}
			for _, l := range f.Lines[:min(len(f.Lines), maxAlertLines)] {
				text := l.Text
				if len(text) > maxAlertLineLength {
					text = text[:maxAlertLineLength]
				}
				a.Lines = append(a.Lines, alertLine{l.Line, text})
			}
			found[ss.Query] = append(found[ss.Query], a)
		}
	}
	if len(found) == 0 {
		return
	}

	savedMu.Lock()
	list = s.loadSaved()
	for i := range list {
		alerts := found[list[i].Query]
		if len(alerts) == 0 || !list[i].Monitor {
			continue
		}
		list[i].Alerts = append(slices.Clone(alerts), list[i].Alerts...)
		if len(list[i].Alerts) > maxAlerts {
			list[i].Alerts = list[i].Alerts[:maxAlerts]
		}
		list[i].Unseen += len(alerts)
		log.Printf("monitor %q: %d new match(es) in %s", list[i].Name, len(alerts), repo)
		if hook := s.engine.Config().MonitorWebhook; hook != "" {
			go postWebhook(hook, list[i], alerts)
		}
	}
	err := s.writeSaved(list)
	savedMu.Unlock()
	if err != nil {
		log.Printf("saving monitor alerts: %v", err)
	}
}

// postWebhook sends new alerts to the configured webhook as JSON.
func postWebhook(url string, ss savedSearch, alerts []monitorAlert) {
	body, _ := json.Marshal(map[string]any{
		"search":  ss.Name,
		"query":   ss.Query,
		"matches": alerts,
		"text":    ss.Name + ": " + plural(len(alerts), "new match", "new matches"),
	})
	ctx, cancel := context.WithTimeout(context.Background(), webhookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		log.Printf("monitor webhook: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("monitor webhook: %v", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("monitor webhook: %s", resp.Status)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
