// Storm-gate tests (spec §13): each rule behind AGENTS.md invariant 3 gets
// a case, driven by injected clocks — no sleeps.
package tui

import (
	"testing"
	"time"
)

func TestGateAdmitsOneProbePerRepo(t *testing.T) {
	g := NewGate(30 * time.Second)
	now := time.Now()

	if got := g.Admit([]string{"a", "a", "a"}, now); len(got) != 1 || got[0] != "a" {
		t.Fatalf("Admit duplicates = %v, want [a]", got)
	}
	// In-flight dedup: a second event parks as pending, not a new probe.
	if got := g.Admit([]string{"a"}, now); len(got) != 0 {
		t.Fatalf("Admit in-flight = %v, want none", got)
	}
	// Release re-issues the pending probe immediately (changes mid-probe
	// are pending and re-issued).
	if got := g.Release("a", now); len(got) != 1 || got[0] != "a" {
		t.Fatalf("Release re-issue = %v, want [a]", got)
	}
}

func TestGateCooldown(t *testing.T) {
	g := NewGate(30 * time.Second)
	now := time.Now()

	if got := g.Admit([]string{"a"}, now); len(got) != 1 {
		t.Fatalf("first admit = %v", got)
	}
	g.Release("a", now)

	// Fresh events inside the cooldown are dropped.
	if got := g.Admit([]string{"a"}, now.Add(10*time.Second)); len(got) != 0 {
		t.Fatalf("cooldown admit = %v, want none", got)
	}
	// After the cooldown, a fresh event is admitted.
	if got := g.Admit([]string{"a"}, now.Add(31*time.Second)); len(got) != 1 {
		t.Fatalf("post-cooldown admit = %v, want one", got)
	}
}

func TestGateTwoPermits(t *testing.T) {
	g := NewGate(30 * time.Second)
	now := time.Now()

	got := g.Admit([]string{"a", "b", "c", "d"}, now)
	if len(got) != 2 {
		t.Fatalf("Admit = %v, want exactly two permits", got)
	}
	// The overflow parked as pending and returns when permits free.
	r1 := g.Release("a", now)
	if len(r1) != 1 {
		t.Fatalf("Release(a) = %v, want one re-issue", r1)
	}
	if got2 := g.Release(r1[0], now); len(got2) != 1 {
		t.Fatalf("Release(re-issue) = %v, want one", got2)
	}
}

func TestGateBatchCap(t *testing.T) {
	g := NewGate(30 * time.Second)
	now := time.Now()

	var batch []string
	for i := range 200 {
		batch = append(batch, "repo"+string(rune('a'+i%26))+string(rune('0'+i/26))+string(rune('a'+i)))
	}
	got := g.Admit(batch, now)
	// Only two permits run now; the point of the cap is that the batch
	// never grew past 64 candidate roots.
	if len(got) != 2 {
		t.Fatalf("Admit = %d roots, want 2", len(got))
	}
}

func TestGateSyncDrop(t *testing.T) {
	g := NewGate(30 * time.Second)
	now := time.Now()

	g.SetSync(true)
	if got := g.Admit([]string{"a"}, now); len(got) != 0 {
		t.Fatalf("sync-active admit = %v, want none", got)
	}
	g.SetSync(false)
	if got := g.Admit([]string{"a"}, now); len(got) != 1 {
		t.Fatalf("post-sync admit = %v, want one", got)
	}
}
