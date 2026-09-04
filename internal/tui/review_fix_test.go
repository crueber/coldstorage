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
