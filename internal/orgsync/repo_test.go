package orgsync

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/crueber/coldstorage/internal/gitmode"
)

// writeScript drops an executable shell script, used to stand in for the
// provider CLIs: the tests exercise the real exec path and the real rails
// without a network or a provider login.
func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// putOnPATH prepends a directory holding fake provider CLIs.
func putOnPATH(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// gitT runs one git call and fails the test on error. Tests may use real
// git — local only, with explicit author identity.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitmode.RunGit(dir, time.Minute, args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return out
}

func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", msg)
}

// seedBare creates a one-commit work repo and a bare mirror of it, the
// file:// "upstream" the sync engine clones from. Network-free by
// construction.
func seedBare(t *testing.T, base, name string) (barePath, seedWork string) {
	t.Helper()
	seedWork = filepath.Join(base, "seed-"+name)
	if err := os.MkdirAll(seedWork, 0o755); err != nil {
		t.Fatal(err)
	}
	gitT(t, base, "init", "-b", "main", seedWork)
	if err := os.WriteFile(filepath.Join(seedWork, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAll(t, seedWork, "seed")

	barePath = filepath.Join(base, name+".git")
	gitT(t, base, "init", "--bare", "-b", "main", barePath)
	gitT(t, seedWork, "push", barePath, "main")
	return barePath, seedWork
}

// pushUpstreamCommit advances the bare upstream by one commit, simulating
// what the provider-side repo did since the last sync.
func pushUpstreamCommit(t *testing.T, seedWork, barePath, file, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(seedWork, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAll(t, seedWork, "add "+file)
	gitT(t, seedWork, "push", barePath, "main")
}

// dirList is the disk seam the engine hands the TUI's discovery layer:
// subdirectory names under the org path.
func dirList(t *testing.T) func(string) []string {
	t.Helper()
	return func(path string) []string {
		entries, err := os.ReadDir(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			t.Fatal(err)
		}
		var names []string
		for _, e := range entries {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		return names
	}
}

// stubList pins a listing answer for PlanSync/ListSync tests.
func stubList(repos []Repo, err error) ListFn {
	return func(Source, time.Duration) ([]Repo, error) { return repos, err }
}
