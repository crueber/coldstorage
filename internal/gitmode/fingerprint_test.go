package gitmode

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fingerprintRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitCmd(t, root, "init", "-q", "-b", "main")
	// gc.auto=0: a commit auto-triggers background maintenance, whose
	// objects/maintenance.lock appears and vanishes under any test that
	// reads .git — the race that once failed this suite in CI.
	gitCmd(t, root, "config", "gc.auto", "0")
	writeFile(t, root, "tracked.txt", "one")
	gitCmd(t, root, "add", ".")
	gitCmd(t, root, "commit", "-q", "-m", "one")
	return root
}

func TestFingerprintStableOnUntouchedRepo(t *testing.T) {
	root := fingerprintRepo(t)

	first := Fingerprint(root)
	if first == 0 {
		t.Fatal("fingerprint of a real repo folded to zero")
	}
	for range 3 {
		if again := Fingerprint(root); again != first {
			t.Fatalf("fingerprint moved without any input moving: %d != %d", again, first)
		}
	}
}

func TestFingerprintChangesOnCommit(t *testing.T) {
	root := fingerprintRepo(t)
	before := Fingerprint(root)

	// Give the filesystem room to move: coarse mtimes must not read as
	// unchanged, or the gate would swallow real commits.
	time.Sleep(20 * time.Millisecond)
	writeFile(t, root, "tracked.txt", "two")
	gitCmd(t, root, "commit", "-q", "-am", "two")

	if after := Fingerprint(root); after == before {
		t.Error("fingerprint survived a commit: new work would never be probed")
	}
}

func TestFingerprintChangesWhenRefFileRewritten(t *testing.T) {
	root := fingerprintRepo(t)
	gitCmd(t, root, "branch", "side") // a second ref, rewritten in place below
	before := Fingerprint(root)

	time.Sleep(20 * time.Millisecond)
	// The case that makes recursion required: a loose ref rewritten in place
	// does not move refs/ or refs/heads/ mtimes. Hashing only directories
	// would let the gate eat every branch update that reuses a ref name.
	if err := os.WriteFile(filepath.Join(root, ".git", "refs", "heads", "side"), []byte("9999999999999999999999999999999999999999\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if after := Fingerprint(root); after == before {
		t.Error("fingerprint survived a rewritten loose ref: branch updates would be swallowed")
	}
}

func TestFingerprintChangesOnFetchHead(t *testing.T) {
	root := fingerprintRepo(t)
	before := Fingerprint(root)

	time.Sleep(20 * time.Millisecond)
	// A repo that has never fetched has no FETCH_HEAD; its arrival must be
	// state, not silence — behind counts are only as fresh as this file.
	if err := os.WriteFile(filepath.Join(root, ".git", "FETCH_HEAD"), []byte("0000000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if after := Fingerprint(root); after == before {
		t.Error("fingerprint could not tell never-fetched from just-fetched")
	}
}

func TestFingerprintUnaffectedByWorkingTreeEdit(t *testing.T) {
	root := fingerprintRepo(t)
	before := Fingerprint(root)

	time.Sleep(20 * time.Millisecond)
	writeFile(t, root, "untracked.txt", "unstaged noise")
	writeFile(t, root, "tracked.txt", "edited, uncommitted")

	if after := Fingerprint(root); after != before {
		t.Error("working-tree edit moved the fingerprint: every editor save would re-probe the fleet. This trade is documented in spec §6 — working-tree edits are the watcher's job")
	}
}

func TestFingerprintIdenticalTrees(t *testing.T) {
	a := fingerprintRepo(t)
	b := t.TempDir()

	// Copy .git with mtimes preserved; identical git state must fold to the
	// identical fingerprint, or the persisted cache could never be shared
	// or restored across processes.
	copyTree(t, filepath.Join(a, ".git"), filepath.Join(b, ".git"))

	if fa, fb := Fingerprint(a), Fingerprint(b); fa != fb {
		t.Errorf("identical trees fingerprint differently: %d != %d", fa, fb)
	}
}

// copyTree copies a directory tree, preserving mtimes — the fingerprint is
// built from mtimes, so a stripped copy would prove nothing.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			copyTree(t, s, d)
			continue
		}
		data, err := os.ReadFile(s)
		if err != nil {
			if os.IsNotExist(err) {
				continue // vanished mid-copy (a gc lock) — not fingerprint state
			}
			t.Fatal(err)
		}
		if err := os.WriteFile(d, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if fi, err := os.Stat(s); err == nil {
			os.Chtimes(d, fi.ModTime(), fi.ModTime())
		}
	}
}

func TestFingerprintMissingRepoIsStable(t *testing.T) {
	// A missing checkout folds sentinels deterministically instead of
	// erroring — the probe layer owns the error path, and the gate must not
	// panic on a repo that vanished mid-sweep.
	ghost := filepath.Join(t.TempDir(), "gone")
	if a, b := Fingerprint(ghost), Fingerprint(ghost); a != b {
		t.Errorf("missing repo fingerprint unstable: %d != %d", a, b)
	}
	if a := Fingerprint(t.TempDir()); a == 0 {
		t.Error("empty dir fingerprint folded to zero; missing sentinels should not vanish")
	}
}

func TestFingerprintWorktreeUsesCommonRefs(t *testing.T) {
	// A linked worktree keeps HEAD and index locally but shares refs,
	// packed-refs, config and FETCH_HEAD with its parent. A commit landing
	// in the parent's refs must move the worktree's fingerprint too, or the
	// gate would serve a worktree stale until its own .git moved.
	main := fingerprintRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	gitCmd(t, main, "worktree", "add", "-q", wt, "-b", "wt-branch")
	before := Fingerprint(wt)

	time.Sleep(20 * time.Millisecond)
	gitCmd(t, main, "branch", "another")

	if after := Fingerprint(wt); after == before {
		t.Error("worktree fingerprint ignored shared ref updates")
	}
}
