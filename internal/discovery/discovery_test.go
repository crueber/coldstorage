package discovery

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

// checkout makes dir look like a normal clone: a .git directory.
func checkout(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// worktree makes dir look like a worktree or submodule: a .git file pointing
// at a git dir elsewhere.
func worktree(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: ../elsewhere/.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// bareRepo makes dir look like a bare repository: HEAD file, refs directory,
// no .git.
func bareRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func find(repos []Repo, root string) (Repo, bool) {
	for _, r := range repos {
		if r.Root == root {
			return r, true
		}
	}
	return Repo{}, false
}

func TestStopsAtFirstRepo(t *testing.T) {
	root := t.TempDir()
	checkout(t, filepath.Join(root, "outer"))
	checkout(t, filepath.Join(root, "outer", "nested"))
	if err := os.WriteFile(filepath.Join(root, "outer", "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("default", func(t *testing.T) {
		repos, _, err := Discover(Options{Roots: []string{root}})
		if err != nil {
			t.Fatal(err)
		}
		if len(repos) != 1 || repos[0].Root != filepath.Join(root, "outer") {
			t.Fatalf("want only the outer repo, got %+v", repos)
		}
	})

	t.Run("follow nested", func(t *testing.T) {
		repos, _, err := Discover(Options{Roots: []string{root}, FollowNestedRepos: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := find(repos, filepath.Join(root, "outer", "nested")); !ok {
			t.Fatalf("nested repo missing with FollowNestedRepos: %+v", repos)
		}
	})
}

func TestGroupAndNameSplit(t *testing.T) {
	root := t.TempDir()
	checkout(t, filepath.Join(root, "g1", "alpha"))
	checkout(t, filepath.Join(root, "solo"))
	checkout(t, root) // the root itself is a repo
	checkout(t, filepath.Join(root, "g1", "sub", "deep"))

	repos, _, err := Discover(Options{Roots: []string{root}, FollowNestedRepos: true})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Repo{
		filepath.Join(root, "g1", "alpha"):       {Root: filepath.Join(root, "g1", "alpha"), Group: "g1", Name: "alpha"},
		filepath.Join(root, "solo"):              {Root: filepath.Join(root, "solo"), Group: "", Name: "solo"},
		filepath.Join(root, "g1", "sub", "deep"): {Root: filepath.Join(root, "g1", "sub", "deep"), Group: "g1", Name: "sub/deep"},
		root:                                     {Root: root, Group: "", Name: filepath.Base(root)},
	}
	if len(repos) != len(want) {
		t.Fatalf("want %d repos, got %+v", len(want), repos)
	}
	for _, got := range repos {
		if want[got.Root] != got {
			t.Errorf("repo %+v, want %+v", got, want[got.Root])
		}
	}
}

func TestMaxDepth(t *testing.T) {
	root := t.TempDir()
	direct := filepath.Join(root, "direct")
	checkout(t, direct)
	grouped := filepath.Join(root, "g", "repo")
	checkout(t, grouped)

	t.Run("depth 1 sees only immediate children", func(t *testing.T) {
		repos, _, err := Discover(Options{Roots: []string{root}, MaxDepth: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(repos) != 1 || repos[0].Root != direct {
			t.Fatalf("want only %s, got %+v", direct, repos)
		}
	})

	t.Run("depth 2 reaches the grouped repo", func(t *testing.T) {
		repos, _, err := Discover(Options{Roots: []string{root}, MaxDepth: 2})
		if err != nil {
			t.Fatal(err)
		}
		if len(repos) != 2 {
			t.Fatalf("want both repos, got %+v", repos)
		}
	})
}

func TestPrune(t *testing.T) {
	root := t.TempDir()
	checkout(t, filepath.Join(root, "node_modules", "stray"))
	checkout(t, filepath.Join(root, "thirdparty", "stray"))
	kept := filepath.Join(root, "g", "kept")
	checkout(t, kept)

	repos, stats, err := Discover(Options{Roots: []string{root}, Prune: []string{"thirdparty"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Root != kept {
		t.Fatalf("pruned dirs leaked repos: %+v", repos)
	}
	if stats.Pruned == 0 {
		t.Errorf("expected pruned dirs to be counted, got %+v", stats)
	}
}

func TestDefaultPruneContents(t *testing.T) {
	root := t.TempDir()
	for _, name := range DefaultPrune {
		checkout(t, filepath.Join(root, name, "stray"))
	}
	kept := filepath.Join(root, "g", "kept")
	checkout(t, kept)

	repos, _, err := Discover(Options{Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Root != kept {
		t.Fatalf("a default-pruned name was descended into: %+v", repos)
	}
}

func TestExclude(t *testing.T) {
	root := t.TempDir()
	checkout(t, filepath.Join(root, "sub", "deep", "gone"))
	checkout(t, filepath.Join(root, "other", "gone"))
	kept := filepath.Join(root, "kept")
	checkout(t, kept)

	repos, _, err := Discover(Options{Roots: []string{root},
		Exclude: []string{"sub/**", "other"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Root != kept {
		t.Fatalf(`"sub/**" and "other" failed to exclude: %+v`, repos)
	}
}

func TestBareRepo(t *testing.T) {
	root := t.TempDir()
	bare := filepath.Join(root, "bare")
	bareRepo(t, bare)
	// HEAD alone is not enough — refs must be there too.
	incomplete := filepath.Join(root, "incomplete")
	if err := os.MkdirAll(incomplete, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incomplete, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repos, _, err := Discover(Options{Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Root != bare {
		t.Fatalf("want only the bare repo, got %+v", repos)
	}
}

func TestWorktreeGitFile(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "wt")
	worktree(t, wt)

	repos, _, err := Discover(Options{Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Root != wt {
		t.Fatalf("a .git file must count as a repo: %+v", repos)
	}
}

func TestMissingRootSkipped(t *testing.T) {
	root := t.TempDir()
	kept := filepath.Join(root, "kept")
	checkout(t, kept)

	repos, _, err := Discover(Options{Roots: []string{filepath.Join(root, "absent"), root}})
	if err != nil {
		t.Fatalf("a missing root must not be an error: %v", err)
	}
	if len(repos) != 1 || repos[0].Root != kept {
		t.Fatalf("want the surviving root's repo, got %+v", repos)
	}
}

func TestSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows; this repo targets darwin/linux")
	}
	root := t.TempDir()
	actual := filepath.Join(root, "actual")
	checkout(t, actual)
	if err := os.Symlink(actual, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	t.Run("not followed by default", func(t *testing.T) {
		repos, _, err := Discover(Options{Roots: []string{root}})
		if err != nil {
			t.Fatal(err)
		}
		if len(repos) != 1 || repos[0].Root != actual {
			t.Fatalf("symlink was followed without FollowSymlinks: %+v", repos)
		}
	})

	t.Run("followed when asked", func(t *testing.T) {
		repos, _, err := Discover(Options{Roots: []string{root}, FollowSymlinks: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := find(repos, filepath.Join(root, "link")); !ok {
			t.Fatalf("symlinked repo missing with FollowSymlinks: %+v", repos)
		}
	})
}

func TestDeterministicOrder(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{
		"zebra/last", "alpha/first", "middle/inner/nested", "solo", "alpha/second",
	} {
		checkout(t, filepath.Join(root, filepath.FromSlash(p)))
	}
	checkout(t, filepath.Join(root, "middle", "inner", "nested", "deeper"))

	repos, _, err := Discover(Options{Roots: []string{root}, FollowNestedRepos: true})
	if err != nil {
		t.Fatal(err)
	}
	if !sort.SliceIsSorted(repos, func(i, j int) bool { return repos[i].Root < repos[j].Root }) {
		t.Fatalf("output not sorted by root path: %+v", repos)
	}
	for i := 0; i < 3; i++ {
		again, _, err := Discover(Options{Roots: []string{root}, FollowNestedRepos: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(again) != len(repos) {
			t.Fatalf("sweep %d found a different repo count: %d vs %d", i, len(again), len(repos))
		}
		for j := range repos {
			if again[j] != repos[j] {
				t.Fatalf("sweep %d reordered output:\n got %+v\nwant %+v", i, again, repos)
			}
		}
	}
}
