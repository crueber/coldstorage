package tui

// The repo sync (p / P): real origin/clone pairs, real pulls — the §11.3
// semantics must hold from the dashboard: ff updates land, dirt skips,
// divergence skips, and the done tally is honest.

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/crueber/coldstorage/internal/config"
)

// waitPullDone consumes the engine's message sink until the sync pass
// closes — pull messages reach the UI through send, and the pass's
// goroutines are wg members, so quit is only closed after the tally lands.
func waitPullDone(t *testing.T, eng *engine) pullDoneMsg {
	t.Helper()
	sink := make(chan any, 256)
	eng.send = func(m any) {
		select {
		case sink <- m:
		default:
		}
	}
	for {
		select {
		case m := <-sink:
			if d, ok := m.(pullDoneMsg); ok {
				return d
			}
		case <-time.After(30 * time.Second):
			t.Fatal("the sync pass never finished")
		}
	}
}

// syncFixture builds origin (with one commit) and a clone of it.
func syncPaths(clone string) (origin, other string) {
	return filepath.Join(filepath.Dir(clone), "origin"), ""
}

func syncFixture(t *testing.T) (origin, clone string) {
	t.Helper()
	base := t.TempDir()
	origin = filepath.Join(base, "origin")
	clone = filepath.Join(base, "clone")
	gitRun(t, "init", "-q", "-b", "main", origin)
	gitRun(t, "-C", origin, "config", "user.email", "t@t")
	gitRun(t, "-C", origin, "config", "user.name", "t")
	writeRepoFile(t, origin, "f.txt", "one")
	gitRun(t, "-C", origin, "add", ".")
	gitRun(t, "-C", origin, "commit", "-q", "-m", "one")
	gitRun(t, "clone", "-q", origin, clone)
	gitRun(t, "-C", clone, "config", "user.email", "t@t")
	gitRun(t, "-C", clone, "config", "user.name", "t")
	return origin, clone
}

func gitRun(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func writeRepoFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSyncReposPullsFastForward(t *testing.T) {
	origin, clone := syncFixture(t)
	eng := newEngine(config.Default(), t.TempDir(), nil)
	stubExec(t)

	// A new commit lands upstream after the clone.
	writeRepoFile(t, origin, "g.txt", "two")
	gitRun(t, "-C", origin, "add", ".")
	gitRun(t, "-C", origin, "commit", "-q", "-m", "two")

	if !eng.SyncRepos([]string{clone}) {
		t.Fatal("SyncRepos refused on an idle engine")
	}
	done := waitPullDone(t, eng)
	if done.updated != 1 {
		t.Errorf("tally = %+v, want 1 updated", done)
	}
	close(eng.quit)
	eng.wg.Wait()

	head := gitOut(t, clone, "rev-parse", "HEAD")
	up := gitOut(t, origin, "rev-parse", "HEAD")
	if head != up {
		t.Fatalf("clone at %s, origin at %s — the pull did not land", head[:8], up[:8])
	}
}

func TestSyncReposSkipsDirty(t *testing.T) {
	_, clone := syncFixture(t)
	eng := newEngine(config.Default(), t.TempDir(), nil)

	// A local edit to a tracked file the upstream commit also touches:
	// pull --ff-only refuses, and §11.3 leaves the checkout alone.
	writeRepoFile(t, clone, "f.txt", "local edit")
	origin, _ := syncPaths(clone)
	writeRepoFile(t, origin, "f.txt", "upstream edit")
	gitRun(t, "-C", origin, "add", ".")
	gitRun(t, "-C", origin, "commit", "-q", "-m", "upstream edit")

	if !eng.SyncRepos([]string{clone}) {
		t.Fatal("SyncRepos refused")
	}
	if done := waitPullDone(t, eng); done.skipped != 1 {
		t.Errorf("tally = %+v, want 1 skipped (dirty)", done)
	}
	body, err := os.ReadFile(filepath.Join(clone, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "local edit" {
		t.Errorf("the dirty checkout was touched: %q", body)
	}
	close(eng.quit)
	eng.wg.Wait()
}

func TestSyncReposRefusesWhileSweeping(t *testing.T) {
	eng := newEngine(config.Default(), t.TempDir(), nil)
	eng.sweeping.Store(true)
	if eng.SyncRepos([]string{t.TempDir()}) {
		t.Error("SyncRepos must refuse while a sweep owns the background")
	}
}

func TestSyncReposBusySecondCall(t *testing.T) {
	eng := newEngine(config.Default(), t.TempDir(), nil)
	t.Cleanup(func() { close(eng.quit) })
	// Hold the flag the way a running pass does: the CAS must refuse.
	eng.pulling.Store(true)
	if eng.SyncRepos([]string{t.TempDir()}) {
		t.Error("second sync must be refused while one runs")
	}
	eng.pulling.Store(false)
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}
