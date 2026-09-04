// The dashboard's cache (GO-PORT-SPEC.md §15): state.json in the platform
// cache dir, holding the per-repo probe results keyed by repo root. The
// cache exists so the table paints instantly on startup — a restart must not
// stare at an empty screen while a fleet sweep runs — and so a sweep can
// skip unchanged repos through the fingerprint gate without spawning a
// single process (§6). It is never authoritative: a missing or unreadable
// cache is a non-event, and every sweep re-derives the truth from disk.
package tui

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/crueber/coldstorage/internal/gitmode"
)

// cacheName is the cache file inside the config cache dir (§3, §15).
const cacheName = "state.json"

// cacheVersion exists so a future schema change can refuse gracefully. Per
// §15, adding fields must not bump it.
const cacheVersion = 1

// cacheFile is the on-disk shape. Rows are keyed by repo root, which is the
// one identity that survives renames of groups and re-sorts of the table.
type cacheFile struct {
	Version int                  `json:"version"`
	Repos   map[string]cachedRow `json:"repos"`
}

// cachedRow is the persisted half of a RepoState. Probe errors are stored as
// strings: an errored row re-probes until it succeeds (§6), and the cache
// exists to skip success, not to canonize failure.
type cachedRow struct {
	Group        string                  `json:"group"`
	Name         string                  `json:"name"`
	Refs         gitmode.RefsInfo        `json:"refs"`
	Work         *gitmode.WorkInfo       `json:"work,omitempty"`
	WorkKey      gitmode.WorkKey         `json:"work_key"`
	Fingerprint  uint64                  `json:"fingerprint"`
	RefsProbedAt time.Time               `json:"refs_probed_at"`
	WorkProbedAt time.Time               `json:"work_probed_at"`
	Err          string                  `json:"error,omitempty"`
	Visibility   *gitmode.VisibilityInfo `json:"visibility,omitempty"`
}

// loadCache reads the cache file for the given cache dir. Any failure —
// missing file, bad JSON, wrong version — returns an empty cache, because a
// disposable cache must never block a dashboard.
func loadCache(dir string) map[string]RepoState {
	out := map[string]RepoState{}
	data, err := os.ReadFile(filepath.Join(dir, cacheName))
	if err != nil {
		return out
	}
	var cf cacheFile
	if json.Unmarshal(data, &cf) != nil || cf.Version != cacheVersion {
		return out
	}
	for root, row := range cf.Repos {
		rs := RepoState{
			Root:         root,
			Group:        row.Group,
			Name:         row.Name,
			Refs:         row.Refs,
			Work:         row.Work,
			WorkKey:      row.WorkKey,
			Fingerprint:  row.Fingerprint,
			RefsProbedAt: row.RefsProbedAt,
			WorkProbedAt: row.WorkProbedAt,
			Visibility:   row.Visibility,
		}
		if row.Err != "" {
			rs.Err = errors.New(row.Err)
		}
		out[root] = rs
	}
	return out
}

// saveCache writes the cache atomically (temp file + rename, §15). It runs
// off the UI thread, from the engine, after a sweep.
func saveCache(dir string, rows map[string]RepoState) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	cf := cacheFile{Version: cacheVersion, Repos: map[string]cachedRow{}}
	for root, r := range rows {
		row := cachedRow{
			Group:        r.Group,
			Name:         r.Name,
			Work:         r.Work,
			WorkKey:      r.WorkKey,
			Fingerprint:  r.Fingerprint,
			RefsProbedAt: r.RefsProbedAt,
			WorkProbedAt: r.WorkProbedAt,
			Visibility:   r.Visibility,
		}
		row.Refs = r.Refs
		if r.Err != nil {
			row.Err = r.Err.Error()
		}
		cf.Repos[root] = row
	}
	data, err := json.Marshal(cf)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, cacheName+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, cacheName))
}
