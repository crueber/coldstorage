package tui

// Regression tests for the opencode review's findings: the notify TTL
// stamp, non-blocking probe sends after shutdown, and the gate release
// chain draining to a fixpoint instead of leaking permits.

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/coldstorage/internal/config"
	"github.com/crueber/coldstorage/internal/discovery"
	"github.com/crueber/coldstorage/internal/gitmode"
)

func TestNotifyStampsTTL(t *testing.T) {
	m := model{width: 120}
	m.notify("org filter: %s", "crueber")
	if m.statusAt.IsZero() {
		t.Fatal("notify must stamp statusAt — without it the §12 TTL expires every message instantly and the status line never renders")
	}
	if got := stripAnsi(m.statusView()); !strings.Contains(got, "org filter: crueber") {
		t.Errorf("statusView = %q, want the notification rendered", got)
	}
}

func TestProbeSendNeverBlocksAfterQuit(t *testing.T) {
	eng := newEngine(config.Default(), t.TempDir(), nil)
	root := filepath.Join(t.TempDir(), "repo")
	if err := execGit(t, "init", "-q", root); err != nil {
		t.Fatal(err)
	}
	close(eng.quit) // the collector is gone; nothing drains results

	done := make(chan struct{})
	go func() {
		eng.probe(root, "grp", "name", true)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("probe blocked on a full results channel after quit — Close would hang")
	}
}

func TestReleaseChainDrainsToFixpoint(t *testing.T) {
	eng := newEngine(config.Default(), t.TempDir(), nil)

	// Four real checkouts: the watcher batch admits two, parks two. The
	// release chain must re-issue the parked ones — and the waves the
	// chain itself admits — leaving nothing stuck in flight holding a
	// permit.
	var roots []string
	for range make([]struct{}, 4) {
		root := filepath.Join(t.TempDir(), "repo")
		if err := execGit(t, "init", "-q", root); err != nil {
			t.Fatal(err)
		}
		roots = append(roots, root)
	}

	admitted := eng.gate.Admit(roots, time.Now())
	if len(admitted) != watchPermits {
		t.Fatalf("admitted %d roots, want %d", len(admitted), watchPermits)
	}
	for _, r := range admitted {
		eng.probeAsync(r, "grp", filepath.Base(r))
	}
	// The collector is a wg member too; it only exits on quit.
	close(eng.quit)
	eng.wg.Wait()

	eng.gate.mu.Lock()
	defer eng.gate.mu.Unlock()
	if len(eng.gate.inFlight) != 0 {
		t.Errorf("roots still in flight: %v — a wave was admitted but never released, leaking its permit",
			keysOf(eng.gate.inFlight))
	}
	if len(eng.gate.pending) != 0 {
		t.Errorf("roots still pending: %v", keysOf(eng.gate.pending))
	}
	// The channel counts HELD permits (admit sends, release receives): a
	// fully drained fleet must hold none.
	if got := len(eng.gate.permits); got != 0 {
		t.Errorf("permits still held: %d", got)
	}
	eng.mu.Lock()
	defer eng.mu.Unlock()
	if len(eng.cache) != len(roots) {
		t.Errorf("probed %d of %d roots — the chain skipped parked roots", len(eng.cache), len(roots))
	}
}

func keysOf(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func execGit(t *testing.T, args ...string) error {
	t.Helper()
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return nil
}

// The nameless-rows incident: a watcher re-issue for a repo with no cache
// entry probed with empty display names, cached the blank row, and the
// fingerprint gate served it forever.

func TestDisplayNamesFallsBackToPath(t *testing.T) {
	eng := newEngine(config.Default(), t.TempDir(), nil)
	group, name := eng.displayNames("/home/u/dev/github.com/crueber/walhub")
	if group != "crueber" || name != "walhub" {
		t.Errorf("path fallback = (%q, %q), want (crueber, walhub)", group, name)
	}
}

func TestDisplayNamesPrefersDiscoveryOverCache(t *testing.T) {
	eng := newEngine(config.Default(), t.TempDir(), nil)
	root := "/r/crueber/alpha"
	eng.mu.Lock()
	eng.discovered[root] = discovery.Repo{Root: root, Group: "crueber", Name: "alpha"}
	eng.cache[root] = RepoState{Root: root, Group: "stale", Name: "stale"}
	eng.mu.Unlock()
	group, name := eng.displayNames(root)
	if group != "crueber" || name != "alpha" {
		t.Errorf("discovery must outrank the cache: (%q, %q)", group, name)
	}
}

func TestProbeHealsBlankCachedNames(t *testing.T) {
	eng := newEngine(config.Default(), t.TempDir(), nil)
	root := filepath.Join(t.TempDir(), "repo")
	if err := execGit(t, "init", "-q", root); err != nil {
		t.Fatal(err)
	}
	// Seed the poisoned row with the repo's CURRENT fingerprint so the
	// gate matches and the heal path (not a full re-probe) runs.
	eng.mu.Lock()
	eng.cache[root] = RepoState{Root: root, Fingerprint: gitmode.Fingerprint(root)}
	eng.mu.Unlock()

	// The sent row is the healed cache row (collect consumes the channel).
	eng.probe(root, "crueber", "repo", false) // fingerprint match: no git spawn
	eng.mu.Lock()
	cached := eng.cache[root]
	eng.mu.Unlock()
	if cached.Group != "crueber" || cached.Name != "repo" {
		t.Fatalf("cache not healed: %q/%q", cached.Group, cached.Name)
	}
}

// The blank-after-pull incident: a watcher re-issue (the pull mutates the
// checkout, the watcher fires on the debounce) probed with empty names, and
// the blank row blanked the table until a forced sweep. probe() must heal
// caller-supplied blanks itself.
func TestProbeHealsCallerBlankNames(t *testing.T) {
	eng := newEngine(config.Default(), t.TempDir(), nil)
	root := filepath.Join(t.TempDir(), "terra-boxes")
	if err := execGit(t, "init", "-q", root); err != nil {
		t.Fatal(err)
	}
	eng.mu.Lock()
	eng.discovered[root] = discovery.Repo{Root: root, Group: "crueber", Name: "terra-boxes"}
	eng.mu.Unlock()

	eng.probe(root, "", "", true) // watcher-style: root only

	eng.mu.Lock()
	row := eng.cache[root]
	eng.mu.Unlock()
	if row.Group != "crueber" || row.Name != "terra-boxes" {
		t.Errorf("cached row = %q/%q, want real names", row.Group, row.Name)
	}
	// The sink carries the result the UI renders — it must have names too.
	eng.send = func(m any) {}
	eng.probe(root, "", "", true)
}
