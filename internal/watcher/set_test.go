package watcher

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// mkCheckout builds a realistic checkout layout by hand — git is never
// invoked in these tests. The .git tree deliberately carries an objects/
// subtree: it must never appear in any watch set.
func mkCheckout(t *testing.T, repo string) {
	t.Helper()
	for _, d := range []string{
		".git/refs/heads", ".git/refs/tags",
		".git/logs/refs/heads",
		".git/objects/ab",
		"src",
		"docs",
	} {
		if err := os.MkdirAll(filepath.Join(repo, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{".git/HEAD", ".git/index", ".git/config", ".git/packed-refs", ".git/ORIG_HEAD"} {
		if err := os.WriteFile(filepath.Join(repo, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// targetPaths flattens a watch set into path -> recursive for assertions.
func targetPaths(rw repoWatch) map[string]bool {
	m := make(map[string]bool, len(rw.Targets))
	for _, t := range rw.Targets {
		m[t.Path] = t.Recursive
	}
	return m
}

func TestWatchSetCheckout(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "alpha")
	mkCheckout(t, repo)

	rw := watchSetFor(repo, pruneSet(nil))
	got := targetPaths(rw)
	if rw.GitDir != filepath.Join(repo, ".git") {
		t.Errorf("GitDir = %q, want repo/.git", rw.GitDir)
	}

	// Root: watched, non-recursive — a fallback signal only.
	if rec, ok := got[repo]; !ok || rec {
		t.Errorf("root target = (%v, recursive=%v), want present non-recursive", ok, rec)
	}
	// .git non-recursive; refs and logs recursive; nothing under objects.
	if rec, ok := got[filepath.Join(repo, ".git")]; !ok || rec {
		t.Errorf(".git target = (%v, recursive=%v), want present non-recursive", ok, rec)
	}
	for _, sub := range []string{"refs", "logs"} {
		p := filepath.Join(repo, ".git", sub)
		if rec, ok := got[p]; !ok || !rec {
			t.Errorf("%s target = (%v, recursive=%v), want present recursive", sub, ok, rec)
		}
	}
	// Working dirs: each non-recursive, one per directory.
	for _, want := range []string{"src", "docs"} {
		p := filepath.Join(repo, want)
		if rec, ok := got[p]; !ok || rec {
			t.Errorf("%s target = (%v, recursive=%v), want present non-recursive", want, ok, rec)
		}
	}
	// Pruned trees, nested repos and .git/objects must not appear anywhere.
	for _, banned := range []string{
		filepath.Join(repo, "node_modules"),
		filepath.Join(repo, "vendor"),
		filepath.Join(repo, "nested"),
		filepath.Join(repo, "nested", "inner"),
		filepath.Join(repo, ".git", "objects"),
		filepath.Join(repo, ".git", "objects", "ab"),
	} {
		if _, ok := got[banned]; ok {
			t.Errorf("banned path %q in watch set", banned)
		}
	}
	for p := range got {
		base := filepath.Base(p)
		if base == "objects" || base == "node_modules" {
			t.Errorf("watch set contains %q", p)
		}
	}
}

func TestWatchSetPrune(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "alpha")
	for _, d := range []string{".git", "scratch", "node_modules"} {
		if err := os.MkdirAll(filepath.Join(repo, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	defaults := targetPaths(watchSetFor(repo, pruneSet(nil)))
	if _, ok := defaults[filepath.Join(repo, "scratch")]; !ok {
		t.Error("scratch should be watched with default prune")
	}
	if _, ok := defaults[filepath.Join(repo, "node_modules")]; ok {
		t.Error("node_modules watched despite default prune")
	}

	extra := targetPaths(watchSetFor(repo, pruneSet([]string{"scratch"})))
	if _, ok := extra[filepath.Join(repo, "scratch")]; ok {
		t.Error("scratch watched despite configured prune")
	}
}

func TestWatchSetCap(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "alpha")
	mkCheckout(t, repo)
	for i := range 550 {
		dir := filepath.Join(repo, fmt.Sprintf("d%03d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	rw := watchSetFor(repo, pruneSet(nil))
	if len(rw.Targets) == 0 {
		t.Fatal("empty watch set for populated repo")
	}
	// Root + .git + refs + logs are fixed; the rest is the capped walk.
	fixed := 4
	if got := len(rw.Targets); got != fixed+maxWorkingDirsPerRepo {
		t.Errorf("watch set size = %d, want %d (fixed %d + cap %d)",
			got, fixed+maxWorkingDirsPerRepo, fixed, maxWorkingDirsPerRepo)
	}
}

func TestWatchSetBare(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "fleet.git")
	for _, d := range []string{"refs/heads", "logs/refs/heads", "worktrees/wt1"} {
		if err := os.MkdirAll(filepath.Join(repo, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "HEAD"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	rw := watchSetFor(repo, pruneSet(nil))
	got := targetPaths(rw)
	if rw.GitDir != repo {
		t.Errorf("GitDir = %q, want the repo root itself", rw.GitDir)
	}
	if rec, ok := got[repo]; !ok || rec {
		t.Errorf("root = (%v, recursive=%v), want non-recursive", ok, rec)
	}
	for _, want := range []string{"refs", "logs", "worktrees"} {
		p := filepath.Join(repo, want)
		if rec, ok := got[p]; !ok || !rec {
			t.Errorf("bare %s = (%v, recursive=%v), want recursive", want, ok, rec)
		}
	}
	if _, ok := got[filepath.Join(repo, ".git")]; ok {
		t.Error("bare repo must not have a .git target")
	}
}

func TestWatchSetMissingOrShapeless(t *testing.T) {
	root := t.TempDir()
	if rw := watchSetFor(filepath.Join(root, "nope"), pruneSet(nil)); len(rw.Targets) != 0 {
		t.Errorf("missing repo got %d targets, want 0", len(rw.Targets))
	}
	plain := filepath.Join(root, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if rw := watchSetFor(plain, pruneSet(nil)); len(rw.Targets) != 0 {
		t.Errorf("shapeless dir got %d targets, want 0", len(rw.Targets))
	}
}

func TestWatchSetGitFile(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "wt")
	gitDir := filepath.Join(root, "mainrepo", ".git", "worktrees", "wt")
	for _, d := range []string{gitDir, filepath.Join(gitDir, "refs", "heads"), repo} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rw := watchSetFor(repo, pruneSet(nil))
	got := targetPaths(rw)
	if rw.GitDir != gitDir {
		t.Errorf("GitDir = %q, want resolved gitdir %q", rw.GitDir, gitDir)
	}
	if rec, ok := got[gitDir]; !ok || rec {
		t.Errorf("gitdir = (%v, recursive=%v), want non-recursive", ok, rec)
	}
	if rec, ok := got[filepath.Join(gitDir, "refs")]; !ok || !rec {
		t.Errorf("gitdir refs = (%v, recursive=%v), want recursive", ok, rec)
	}
}

func TestExpandTargetsDedupes(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "alpha")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "refs", "heads"), 0o755); err != nil {
		t.Fatal(err)
	}
	targets := []WatchTarget{
		{Path: repo},
		{Path: repo}, // duplicate collapses
		{Path: filepath.Join(repo, ".git")},
		{Path: filepath.Join(repo, ".git", "refs"), Recursive: true},
	}
	got := expandTargets(targets, maxWorkingDirsPerRepo)
	want := []string{
		repo,
		filepath.Join(repo, ".git"),
		filepath.Join(repo, ".git", "refs"),
		filepath.Join(repo, ".git", "refs", "heads"),
	}
	if len(got) != len(want) {
		t.Fatalf("expandTargets = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("expandTargets[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestInterestingPath(t *testing.T) {
	cases := []struct {
		name, repo, git, path string
		want                  bool
	}{
		{"working file", "/r", "/r/.git", "/r/src/main.go", true},
		{"repo root itself", "/r", "/r/.git", "/r", true},
		{"HEAD", "/r", "/r/.git", "/r/.git/HEAD", true},
		{"index", "/r", "/r/.git", "/r/.git/index", true},
		{"packed-refs", "/r", "/r/.git", "/r/.git/packed-refs", true},
		{"FETCH_HEAD", "/r", "/r/.git", "/r/.git/FETCH_HEAD", true},
		{"config", "/r", "/r/.git", "/r/.git/config", true},
		{"merge marker", "/r", "/r/.git", "/r/.git/MERGE_HEAD", true},
		{"revert marker", "/r", "/r/.git", "/r/.git/REVERT_HEAD", true},
		{"cherry-pick marker", "/r", "/r/.git", "/r/.git/CHERRY_PICK_HEAD", true},
		{"bisect log", "/r", "/r/.git", "/r/.git/BISECT_LOG", true},
		{"shallow marker", "/r", "/r/.git", "/r/.git/shallow", true},
		{"ref update", "/r", "/r/.git", "/r/.git/refs/heads/main", true},
		{"reflog", "/r", "/r/.git", "/r/.git/logs/refs/heads/main", true},
		{"worktree bookkeeping", "/r", "/r/.git", "/r/.git/worktrees/wt/HEAD", true},
		{"rebase state", "/r", "/r/.git", "/r/.git/rebase-merge/msgnum", true},
		{"rebase apply", "/r", "/r/.git", "/r/.git/rebase-apply/0001", true},
		{"objects churn", "/r", "/r/.git", "/r/.git/objects/ab/cdef", false},
		{"ORIG_HEAD", "/r", "/r/.git", "/r/.git/ORIG_HEAD", false},
		{"COMMIT_EDITMSG", "/r", "/r/.git", "/r/.git/COMMIT_EDITMSG", false},
		{"hooks", "/r", "/r/.git", "/r/.git/hooks/pre-commit", false},
		{"description", "/r", "/r/.git", "/r/.git/description", false},
		{"outside repo", "/r", "/r/.git", "/other/x", false},
		{"nested objects via cut", "/r", "/r/.git", "/r/sub/.git/objects/ab", false},
		{"nested HEAD via cut", "/r", "/r/.git", "/r/sub/.git/HEAD", true},

		{"bare config", "/b", "/b", "/b/config", true},
		{"bare refs", "/b", "/b", "/b/refs/heads/main", true},
		{"bare reflog", "/b", "/b", "/b/logs/refs/heads/main", true},
		{"bare objects", "/b", "/b", "/b/objects/ab/cdef", false},

		{"worktree file", "/wt", "/main/.git/worktrees/wt", "/wt/src/a.go", true},
		{"worktree git-side HEAD", "/wt", "/main/.git/worktrees/wt", "/main/.git/worktrees/wt/HEAD", true},
		{"main repo objects are not ours", "/wt", "/main/.git/worktrees/wt", "/main/.git/objects/x", false},
	}
	for _, tc := range cases {
		if got := interestingPath(tc.repo, tc.git, tc.path); got != tc.want {
			t.Errorf("%s: interestingPath(%s) = %v, want %v", tc.name, tc.path, got, tc.want)
		}
	}
}
