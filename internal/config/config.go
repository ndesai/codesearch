// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package config loads the codesearch configuration file, which lists the
// repositories (folders) to index and where index data is stored.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultExcludes are skipped in every repository unless
// use_default_excludes is set to false. Hidden files and directories
// (names beginning with ".") are always skipped unless include_hidden is set.
var DefaultExcludes = []string{
	"node_modules", "bower_components", "__pycache__", "*.pyc",
	"*.o", "*.a", "*.so", "*.so.*", "*.dylib", "*.dll", "*.exe", "*.class", "*.jar",
	"*.apk", "*.aar", "*.dex", "*.obj", "*.lib", "*.pdb", "*.wasm",
	"*.zip", "*.tar", "*.gz", "*.tgz", "*.bz2", "*.xz", "*.7z", "*.rar", "*.zst",
	"*.png", "*.jpg", "*.jpeg", "*.gif", "*.bmp", "*.ico", "*.webp", "*.tif", "*.tiff", "*.psd",
	"*.mp3", "*.mp4", "*.wav", "*.ogg", "*.flac", "*.mov", "*.avi", "*.mkv", "*.webm",
	"*.pdf", "*.ttf", "*.otf", "*.woff", "*.woff2", "*.eot",
	"*.img", "*.iso", "*.bin", "*.dat", "*.db", "*.sqlite", "*.pak",
	"*.min.js", "*.min.css", "*.map",
}

// Config is the top-level configuration.
type Config struct {
	// IndexDir is where index shards and metadata are stored.
	// $CODESEARCH_INDEX_DIR, if set, overrides it (see IndexDirEnv),
	// and commands may override both with SetIndexDir.
	IndexDir string `yaml:"index_dir"`
	// Listen is the address csweb listens on.
	Listen string `yaml:"listen"`
	// UpdateInterval is how often csweb re-indexes in the background.
	// Zero disables background indexing.
	UpdateInterval Duration `yaml:"update_interval"`
	// MaxFileSize is the largest file that is indexed, in bytes.
	MaxFileSize Size `yaml:"max_file_size"`
	// ShardFiles is the target number of files per index shard.
	// Large repositories are split into multiple shards so that an
	// incremental update only rewrites the shards that changed.
	ShardFiles int `yaml:"shard_files"`
	// Parallelism is the number of shards indexed concurrently.
	// Each indexing worker uses roughly 400 MB of memory.
	Parallelism int `yaml:"parallelism"`
	// Exclude lists glob patterns excluded from every repository.
	Exclude []string `yaml:"exclude"`
	// UseDefaultExcludes controls whether DefaultExcludes apply (default true).
	UseDefaultExcludes *bool `yaml:"use_default_excludes"`
	// IncludeHidden indexes files and directories whose names begin with ".".
	IncludeHidden bool `yaml:"include_hidden"`
	// APIToken, if set, is required (as "Authorization: Bearer <token>")
	// by the /api/v1 JSON API and by csmcp's HTTP transport.
	APIToken string `yaml:"api_token"`
	// MonitorWebhook, if set, receives a JSON POST when a monitored saved
	// search has new matches.
	MonitorWebhook string `yaml:"monitor_webhook"`
	// RankRecent boosts recently modified files in search results
	// (default true).
	RankRecent *bool `yaml:"rank_recent"`
	// UseGitignore skips files matched by .gitignore files and
	// .git/info/exclude in the indexed folders (default true).
	UseGitignore *bool `yaml:"use_gitignore"`
	// Repos lists the folders to index.
	Repos []*Repo `yaml:"repos"`

	// File is the path the configuration was loaded from.
	File string `yaml:"-"`
}

// Repo is a single indexed folder.
type Repo struct {
	// Name is the short name shown in the UI and used in repo: queries.
	// It defaults to the base name of Path.
	Name string `yaml:"name"`
	// Path is the folder to index.
	Path string `yaml:"path"`
	// Exclude lists additional glob patterns to skip in this repository.
	Exclude []string `yaml:"exclude"`
	// SCIP is a SCIP index file (or a directory of .scip files) with
	// precise code intelligence for this repository, produced by tools
	// such as scip-go, scip-java or scip-clang.
	SCIP string `yaml:"scip"`

	// Excludes is the effective exclude list (global + default + repo).
	Excludes *Matcher `yaml:"-"`
}

// Duration is a time.Duration that unmarshals from strings like "15m".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Value == "" || n.Value == "0" {
		*d = 0
		return nil
	}
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: bad duration %q", n.Line, n.Value)
	}
	*d = Duration(v)
	return nil
}

// Size is a byte count that unmarshals from strings like "2MB".
type Size int64

func (s *Size) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseSize(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %v", n.Line, err)
	}
	*s = Size(v)
	return nil
}

// ParseSize parses a size such as "512", "64KB", "2MB" or "1GiB".
func ParseSize(s string) (int64, error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	t = strings.TrimSuffix(strings.TrimSuffix(t, "IB"), "B")
	mult := int64(1)
	if t != "" {
		switch t[len(t)-1] {
		case 'K':
			mult = 1 << 10
		case 'M':
			mult = 1 << 20
		case 'G':
			mult = 1 << 30
		}
		if mult != 1 {
			t = t[:len(t)-1]
		}
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("bad size %q", s)
	}
	return int64(v * float64(mult)), nil
}

// IndexDirEnv is the environment variable that overrides index_dir.
const IndexDirEnv = "CODESEARCH_INDEX_DIR"

// DefaultIndexDir returns the index directory used when none is
// configured: codesearch in the user's cache directory
// (~/.cache/codesearch on Linux, ~/Library/Caches/codesearch on macOS,
// %LocalAppData%\codesearch on Windows).
func DefaultIndexDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = filepath.Join(home(), ".cache")
	}
	return filepath.Join(dir, "codesearch")
}

// SetIndexDir overrides the index directory, for example from a
// command-line flag. A relative dir is relative to the current
// directory; ~ and $VARS are expanded.
func (c *Config) SetIndexDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("empty index directory")
	}
	abs, err := expand(dir)
	if err != nil {
		return err
	}
	c.IndexDir = abs
	return nil
}

// DefaultFile returns the default configuration file path:
// $CODESEARCH_CONFIG, or else $XDG_CONFIG_HOME/codesearch/config.yaml
// (~/.config/codesearch/config.yaml).
func DefaultFile() string {
	if f := os.Getenv("CODESEARCH_CONFIG"); f != "" {
		return f
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(home(), ".config")
	}
	return filepath.Join(dir, "codesearch", "config.yaml")
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// expand expands a leading ~ and environment variables, and makes p absolute.
func expand(p string) (string, error) {
	p = os.ExpandEnv(p)
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		p = filepath.Join(home(), p[1:])
	}
	return filepath.Abs(p)
}

// Load reads and validates the configuration file.
func Load(file string) (*Config, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config file %s does not exist; see codesearch.example.yaml for a template", file)
		}
		return nil, err
	}
	return Parse(file, data)
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Parse parses configuration data. The file name is used for error
// messages and to resolve relative paths.
func Parse(file string, data []byte) (*Config, error) {
	c := new(Config)
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("%s: %v", file, err)
	}
	if abs, err := filepath.Abs(file); err == nil {
		file = abs
	}
	c.File = file
	errorf := func(format string, args ...any) error {
		return fmt.Errorf("%s: %s", file, fmt.Sprintf(format, args...))
	}

	// Resolve relative paths against the config file's directory.
	base := filepath.Dir(file)
	resolve := func(p string) (string, error) {
		p = os.ExpandEnv(p)
		if !strings.HasPrefix(p, "~") && !filepath.IsAbs(p) {
			p = filepath.Join(base, p)
		}
		return expand(p)
	}

	var err error
	if env := os.Getenv(IndexDirEnv); env != "" {
		// Relative to the current directory, like a command-line flag.
		if err := c.SetIndexDir(env); err != nil {
			return nil, errorf("$%s: %v", IndexDirEnv, err)
		}
	} else if c.IndexDir == "" {
		c.IndexDir = DefaultIndexDir()
	} else if c.IndexDir, err = resolve(c.IndexDir); err != nil {
		return nil, errorf("index_dir: %v", err)
	}
	if c.MonitorWebhook != "" && !strings.HasPrefix(c.MonitorWebhook, "http://") && !strings.HasPrefix(c.MonitorWebhook, "https://") {
		return nil, errorf("monitor_webhook must be an http:// or https:// URL")
	}
	if c.Listen == "" {
		c.Listen = "localhost:2473"
	}
	if c.MaxFileSize <= 0 {
		c.MaxFileSize = 2 << 20
	}
	if c.ShardFiles <= 0 {
		c.ShardFiles = 25000
	}
	if c.Parallelism <= 0 {
		c.Parallelism = min(4, max(1, runtime.NumCPU()/2))
	}
	if c.UseDefaultExcludes == nil {
		t := true
		c.UseDefaultExcludes = &t
	}
	if c.UseGitignore == nil {
		t := true
		c.UseGitignore = &t
	}
	if len(c.Repos) == 0 {
		return nil, errorf("no repos configured")
	}

	seen := map[string]bool{}
	for i, r := range c.Repos {
		if r == nil || r.Path == "" {
			return nil, errorf("repos[%d]: missing path", i)
		}
		if r.Path, err = resolve(r.Path); err != nil {
			return nil, errorf("repos[%d]: %v", i, err)
		}
		if r.SCIP != "" {
			if r.SCIP, err = resolve(r.SCIP); err != nil {
				return nil, errorf("repos[%d] scip: %v", i, err)
			}
		}
		if r.Name == "" {
			r.Name = filepath.Base(r.Path)
		}
		if !validName.MatchString(r.Name) {
			return nil, errorf("repos[%d]: invalid name %q (use letters, digits, '.', '_' or '-')", i, r.Name)
		}
		if seen[r.Name] {
			return nil, errorf("duplicate repo name %q; set a unique name: for each repo", r.Name)
		}
		seen[r.Name] = true

		var pats []string
		if *c.UseDefaultExcludes {
			pats = append(pats, DefaultExcludes...)
		}
		pats = append(pats, c.Exclude...)
		pats = append(pats, r.Exclude...)
		m, err := NewMatcher(pats, c.IncludeHidden)
		if err != nil {
			return nil, errorf("repo %s: %v", r.Name, err)
		}
		r.Excludes = m
	}
	return c, nil
}

// RecencyRanking reports whether recently modified files rank higher.
func (c *Config) RecencyRanking() bool {
	return c.RankRecent == nil || *c.RankRecent
}

// Gitignore reports whether .gitignore files are honored.
func (c *Config) Gitignore() bool {
	return c.UseGitignore == nil || *c.UseGitignore
}

// Repo returns the repo with the given name, or nil.
func (c *Config) Repo(name string) *Repo {
	for _, r := range c.Repos {
		if r.Name == name {
			return r
		}
	}
	return nil
}

// Fingerprint returns a string that changes whenever settings that
// affect which files are indexed for r change.
func (c *Config) Fingerprint(r *Repo) string {
	h := sha256.New()
	// v2: shards have symbol tables.
	fmt.Fprintf(h, "v2\x00%s\x00%d\x00%v\x00%v\x00", r.Path, c.MaxFileSize, c.IncludeHidden, c.Gitignore())
	for _, p := range r.Excludes.patterns {
		fmt.Fprintf(h, "%s\x00", p)
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}

// A Matcher decides whether a repository-relative path is excluded.
//
// Patterns use shell glob syntax (see path.Match). A pattern without a
// slash matches any single path element, so "node_modules" skips every
// directory with that name and "*.min.js" skips matching files anywhere.
// A pattern containing a slash is matched against the leading elements of
// the slash-separated path relative to the repository root, so "out" and
// "/out" differ: "/out" (or "out/") skips only the top-level out directory.
type Matcher struct {
	patterns []string
	elem     []string // patterns matched against a single element
	exact    map[string]bool
	rooted   [][]string // patterns matched against leading path elements
	hidden   bool
}

// NewMatcher returns a Matcher for the given patterns.
func NewMatcher(patterns []string, includeHidden bool) (*Matcher, error) {
	m := &Matcher{patterns: patterns, exact: map[string]bool{}, hidden: includeHidden}
	for _, p := range patterns {
		p = strings.TrimSpace(filepath.ToSlash(p))
		if p == "" {
			continue
		}
		if _, err := path.Match(p, ""); err != nil {
			return nil, fmt.Errorf("bad exclude pattern %q", p)
		}
		trimmed := strings.Trim(p, "/")
		if !strings.Contains(p, "/") {
			if strings.ContainsAny(p, `*?[\`) {
				m.elem = append(m.elem, p)
			} else {
				m.exact[p] = true
			}
			continue
		}
		m.rooted = append(m.rooted, strings.Split(trimmed, "/"))
	}
	return m, nil
}

// Excluded reports whether the entry with the given slash-separated
// repository-relative path should be skipped.
func (m *Matcher) Excluded(rel string) bool {
	name := rel
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		name = rel[i+1:]
	}
	if name == "" {
		return false
	}
	if !m.hidden && name[0] == '.' {
		return true
	}
	// Editor backup and temporary files.
	if name[0] == '#' || name[0] == '~' || name[len(name)-1] == '~' {
		return true
	}
	if m.exact[name] {
		return true
	}
	for _, p := range m.elem {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	if len(m.rooted) > 0 {
		elems := strings.Split(rel, "/")
	Rooted:
		for _, pe := range m.rooted {
			if len(pe) > len(elems) {
				continue
			}
			for i, p := range pe {
				if ok, _ := path.Match(p, elems[i]); !ok {
					continue Rooted
				}
			}
			return true
		}
	}
	return false
}
