// Package config loads coldstorage's TOML configuration, resolves the
// platform file locations, and validates organization registrations.
//
// The configuration is deliberately append-friendly: every new field ships
// with a default so an older config file keeps loading on a newer binary.
// Unknown keys are the exception — they are a hard error naming the key,
// because a silently ignored key is a config change that never took
// effect (see §4 and the incidents behind AGENTS.md invariant 7).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"

	"github.com/BurntSushi/toml"
)

// Config is the entire configuration file, mirroring the TOML in §4.
// Sections are structs rather than pointer-optional: absent sections keep
// their defaults, so older files load unchanged when new sections appear.
type Config struct {
	Roots             []string `toml:"roots"`               // dirs to scan; each immediate subdir is a "group"
	MaxDepth          int      `toml:"max_depth"`           // how deep below a root to look
	FollowNestedRepos bool     `toml:"follow_nested_repos"` // descend into a repo looking for more repos
	FollowSymlinks    bool     `toml:"follow_symlinks"`     // follow symlinked dirs while scanning
	Exclude           []string `toml:"exclude"`             // glob patterns, matched relative to the root
	Prune             []string `toml:"prune"`               // extra dir names never to descend into

	Refresh    RefreshConfig    `toml:"refresh"`
	Status     StatusConfig     `toml:"status"`
	Remote     RemoteConfig     `toml:"remote"`
	Visibility VisibilityConfig `toml:"visibility"`
	Release    ReleaseConfig    `toml:"release"`
	UI         UIConfig         `toml:"ui"`

	Orgs []OrgConfig `toml:"orgs"`
}

// RefreshConfig governs the backstop sweep: how often the whole fleet is
// re-walked even if filesystem watching is doing its job, because watchers
// miss events under load and across editor renames.
type RefreshConfig struct {
	Interval string `toml:"interval"` // backstop sweep interval
	Watch    bool   `toml:"watch"`    // filesystem watching on/off
	Debounce string `toml:"debounce"` // fs event coalescing window
}

// StatusConfig caps the expensive part of the per-repo probe: the
// working-tree scan. A repo with 5,000 changed files must not stall the
// fleet, and a stale cache entry is better than an unbounded wait.
type StatusConfig struct {
	Untracked string `toml:"untracked"` // normal | all | no  (git status untracked mode)
	MaxFiles  int    `toml:"max_files"` // cap changed files listed per repo
	MaxAge    string `toml:"max_age"`   // working-tree cache age backstop
}

// RemoteConfig governs periodic background fetches — opt-in, because a
// fleet-wide fetch is network traffic the owner did not ask for by
// default.
type RemoteConfig struct {
	Fetch       bool   `toml:"fetch"`       // periodic background fetch
	Interval    string `toml:"interval"`    // fetch timer
	Concurrency int    `toml:"concurrency"` // concurrent fetches / tier-2 scans
	Timeout     string `toml:"timeout"`     // per-fetch timeout
}

// VisibilityConfig governs the opt-in GitHub public/private check; the
// default is off because it costs API traffic for information most fleets
// never act on.
type VisibilityConfig struct {
	Enabled     bool   `toml:"enabled"`     // opt-in public/private checks
	Interval    string `toml:"interval"`    // recheck cadence
	Concurrency int    `toml:"concurrency"` // concurrent checks
	Timeout     string `toml:"timeout"`     // per-check timeout
}

// ReleaseConfig decides which tags count as releases and how much
// per-release material is kept. max_subjects exists because commit
// subjects are display data, not archives.
type ReleaseConfig struct {
	TagPattern     string   `toml:"tag_pattern"`     // which tags count as releases
	MaxSubjects    int      `toml:"max_subjects"`    // commit subjects kept per repo
	ReadChangelog  bool     `toml:"read_changelog"`  // parse changelog files for release notes
	ChangelogFiles []string `toml:"changelog_files"` // filenames tried in order
}

// UIConfig sets the dashboard's initial state and the external tools it
// launches. An empty git_client_command or file_manager_command means
// auto-detect from the usual candidates; terminal_command has no sensible
// auto-detect, so a placeholder is shipped in the defaults.
type UIConfig struct {
	DefaultFilters     []string `toml:"default_filters"`      // e.g. ["dirty", "unpushed"]
	DefaultSort        string   `toml:"default_sort"`         // e.g. "activity"
	DefaultSince       string   `toml:"default_since"`        // e.g. "1w"
	Theme              string   `toml:"theme"`                // auto (default), dark, or light
	EditorCommand      []string `toml:"editor_command"`       // {path} is replaced by the repo root
	GitClientCommand   []string `toml:"git_client_command"`   // empty = auto-detect (lazygit, gitui)
	FileManagerCommand []string `toml:"file_manager_command"` // empty = auto-detect (superfile, nnn, ranger)
	TerminalCommand    []string `toml:"terminal_command"`     // T and ctrl-o
}

// Default returns the configuration a fresh install runs with, matching
// §4 verbatim. Every field is set so that decoding an old file over a
// primed copy keeps absent keys at these values.
func Default() Config {
	return Config{
		Roots:             []string{"~/Projects"},
		MaxDepth:          4,
		FollowNestedRepos: false,
		FollowSymlinks:    false,
		Exclude:           []string{},
		Prune:             []string{},

		Refresh: RefreshConfig{
			Interval: "5m",
			Watch:    true,
			Debounce: "1s",
		},
		Status: StatusConfig{
			Untracked: "normal",
			MaxFiles:  200,
			MaxAge:    "1h",
		},
		Remote: RemoteConfig{
			Fetch:       false,
			Interval:    "1h",
			Concurrency: 4,
			Timeout:     "20s",
		},
		Visibility: VisibilityConfig{
			Enabled:     false,
			Interval:    "24h",
			Concurrency: 4,
			Timeout:     "10s",
		},
		Release: ReleaseConfig{
			TagPattern:     "*[0-9]*",
			MaxSubjects:    30,
			ReadChangelog:  true,
			ChangelogFiles: []string{"CHANGELOG.md", "CHANGELOG", "changelog.md"},
		},
		UI: UIConfig{
			DefaultFilters:     []string{},
			DefaultSort:        "activity",
			DefaultSince:       "",
			Theme:              "auto",
			EditorCommand:      []string{"zed", "{path}"},
			GitClientCommand:   []string{},
			FileManagerCommand: []string{},
			TerminalCommand:    []string{""},
		},
		Orgs: []OrgConfig{},
	}
}

// Load reads and decodes the TOML file at path. A missing file is not an
// error — a fresh install has no config and must run on defaults. Anything
// else that goes wrong is loud and names the file: an unreadable file, a
// syntax error, a wrong-typed value, or an unknown key all mean the owner
// edited something that did not take effect.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Default(), nil
		}
		return Config{}, fmt.Errorf("coldstorage: %s: %w", path, err)
	}

	if err := checkKeys(data); err != nil {
		return Config{}, fmt.Errorf("coldstorage: %s: %w", path, err)
	}

	cfg := Default()
	if _, err := toml.NewDecoder(bytes.NewReader(data)).Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("coldstorage: %s: %w", path, err)
	}

	// [[orgs]] entries decode over a primed OrgConfig so an entry that
	// omits, say, enabled still lands on the spec default rather than the
	// zero value. The first pass above already type-checked the orgs; this
	// pass only re-decodes each table for its defaults.
	var primed struct {
		Orgs []toml.Primitive `toml:"orgs"`
	}
	md, err := toml.NewDecoder(bytes.NewReader(data)).Decode(&primed)
	if err != nil {
		return Config{}, fmt.Errorf("coldstorage: %s: %w", path, err)
	}
	orgs := make([]OrgConfig, 0, len(primed.Orgs))
	for i, prim := range primed.Orgs {
		org := defaultOrg()
		if err := md.PrimitiveDecode(prim, &org); err != nil {
			return Config{}, fmt.Errorf("coldstorage: %s: orgs[%d]: %w", path, i, err)
		}
		orgs = append(orgs, org)
	}
	cfg.Orgs = orgs
	return cfg, nil
}

// The TOML key sets of every legal section. Strictness is enforced by hand
// rather than by the decoder: BurntSushi toml has no strict mode in v1.4,
// and [[orgs]] entries decoded as primitives would hide their keys from
// MetaData.Undecoded entirely.
var topLevelKeys = map[string]bool{
	"roots": true, "max_depth": true, "follow_nested_repos": true,
	"follow_symlinks": true, "exclude": true, "prune": true,
	"refresh": true, "status": true, "remote": true,
	"visibility": true, "release": true, "ui": true, "orgs": true,
}

var sectionKeys = map[string]map[string]bool{
	"refresh":    {"interval": true, "watch": true, "debounce": true},
	"status":     {"untracked": true, "max_files": true, "max_age": true},
	"remote":     {"fetch": true, "interval": true, "concurrency": true, "timeout": true},
	"visibility": {"enabled": true, "interval": true, "concurrency": true, "timeout": true},
	"ui":         {"default_filters": true, "default_sort": true, "default_since": true, "theme": true, "editor_command": true, "git_client_command": true, "file_manager_command": true, "terminal_command": true},
}

var orgKeys = map[string]bool{
	"provider": true, "host": true, "owner": true, "path": true,
	"login": true, "protocol": true, "include_forks": true,
	"include_archived": true, "include_subgroups": true, "exclude": true,
	"enabled": true,
}

// checkKeys walks the parsed document and rejects any key outside the
// schema, naming it: a typo'd key must fail loudly, because the config it
// was meant to change silently keeps its default instead. Type errors are
// left to the real decode, which reports them with better context.
func checkKeys(data []byte) error {
	var doc map[string]any
	if _, err := toml.NewDecoder(bytes.NewReader(data)).Decode(&doc); err != nil {
		return err
	}
	for _, key := range sortedKeys(doc) {
		if key == "orgs" {
			if err := checkOrgKeys(doc[key]); err != nil {
				return err
			}
			continue
		}
		if !topLevelKeys[key] {
			return fmt.Errorf("unknown key %q", key)
		}
		if allowed, ok := sectionKeys[key]; ok {
			table, ok := doc[key].(map[string]any)
			if !ok {
				continue // a wrong type for the whole section fails at decode
			}
			for _, sub := range sortedKeys(table) {
				if !allowed[sub] {
					return fmt.Errorf("unknown key %q", key+"."+sub)
				}
			}
		}
	}
	return nil
}

func checkOrgKeys(v any) error {
	entries, ok := v.([]map[string]any)
	if !ok {
		return nil // a wrong shape for [[orgs]] fails at decode
	}
	for i, entry := range entries {
		for _, sub := range sortedKeys(entry) {
			if !orgKeys[sub] {
				return fmt.Errorf("unknown key %q", fmt.Sprintf("orgs[%d].%s", i, sub))
			}
		}
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
