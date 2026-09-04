// Cache tests (spec §15): round-trip through state.json, and a missing or
// corrupt cache is a non-event.
package tui

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crueber/coldstorage/internal/gitmode"
)

func TestCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Truncate(time.Second)

	rows := map[string]RepoState{
		"/fleet/grp/demo": {
			Root:  "/fleet/grp/demo",
			Group: "grp",
			Name:  "demo",
			Refs: gitmode.RefsInfo{
				Head:      gitmode.Head{Kind: gitmode.HeadBranch, Branch: "main"},
				Stashes:   2,
				FetchedAt: now,
				NewestTag: &gitmode.TagInfo{Name: "v1.2.3", At: now},
			},
			Work:         &gitmode.WorkInfo{Staged: 1, Untracked: 2},
			Fingerprint:  123456789,
			RefsProbedAt: now,
			WorkProbedAt: now,
		},
		"/fleet/grp/broken": {
			Root: "/fleet/grp/broken", Group: "grp", Name: "broken",
			Err: errors.New("not a repository"),
		},
	}

	if err := saveCache(dir, rows); err != nil {
		t.Fatalf("saveCache: %v", err)
	}

	got := loadCache(dir)
	if len(got) != 2 {
		t.Fatalf("loadCache rows = %d, want 2", len(got))
	}
	demo := got["/fleet/grp/demo"]
	if demo.Name != "demo" || demo.Group != "grp" || demo.Fingerprint != 123456789 {
		t.Errorf("demo row = %+v", demo)
	}
	if demo.Refs.Head.Branch != "main" || demo.Refs.Stashes != 2 {
		t.Errorf("demo refs = %+v", demo.Refs)
	}
	if demo.Work == nil || demo.Work.Staged != 1 || demo.Work.Untracked != 2 {
		t.Errorf("demo work = %+v", demo.Work)
	}
	if !demo.Refs.FetchedAt.Equal(now) || !demo.Refs.NewestTag.At.Equal(now) {
		t.Errorf("demo times = %+v, want %v", demo.Refs, now)
	}
	if got["/fleet/grp/broken"].Err == nil {
		t.Error("broken row must keep its error")
	}
}

func TestCacheMissingIsNonEvent(t *testing.T) {
	if got := loadCache(t.TempDir()); len(got) != 0 {
		t.Errorf("missing cache = %d rows, want 0", len(got))
	}
}

func TestCacheCorruptIsNonEvent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, cacheName), []byte("not json{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadCache(dir); len(got) != 0 {
		t.Errorf("corrupt cache = %d rows, want 0", len(got))
	}
}

func TestCacheAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	if err := saveCache(dir, nil); err != nil {
		t.Fatalf("saveCache: %v", err)
	}
	// The temp file must be gone: rename, not leftovers.
	if _, err := os.Stat(filepath.Join(dir, cacheName+".tmp")); !os.IsNotExist(err) {
		t.Error("cache write left a temp file behind")
	}
}
