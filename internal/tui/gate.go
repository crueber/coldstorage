// Package tui is the coldstorage dashboard (GO-PORT-SPEC.md §12): the live
// table over a fleet of git repositories, the detail and help overlays, and
// the background pipeline that feeds it. The behavioral contract for the
// event loop is the preemption contract (§12, §14): user input always wins,
// background work arrives in bounded batches, and no key handler ever spawns
// a process or touches the filesystem synchronously.
package tui

import (
	"sort"
	"sync"
	"time"
)

// maxWatchBatch is the §13 batch cap: a single watcher batch may never set
// off more re-probes than this, whatever the size of the event storm.
const maxWatchBatch = 64

// watchPermits is the §13 fleet-wide cap: at most two watcher-triggered
// re-probes run at once, so a storm costs milliseconds of CPU per second,
// not a pegged core.
const watchPermits = 2

// Gate implements the §13 re-probe storm gates for watcher events: in-flight
// dedup (one re-probe per repo, changes mid-probe pending and re-issued), a
// 30s cooldown on fresh events, a two-permit fleet-wide semaphore, a 64-repo
// batch cap, and a total drop while an org sync runs. It is a plain struct
// with no goroutines, so the storm rules are unit-testable without wiring.
//
// The cooldown deliberately does not apply to a pending re-issue: a change
// that landed while a repo was already being probed must be re-probed when
// the probe finishes, or the dashboard would show stale state for a full
// cooldown. Fresh events after a probe are what the cooldown gates.
type Gate struct {
	cooldown time.Duration
	permits  chan struct{}

	mu         sync.Mutex
	inFlight   map[string]bool
	pending    map[string]bool
	last       map[string]time.Time
	syncActive bool
}

// NewGate builds a gate with the given cooldown (30s per spec §13) and the
// fleet-wide two-permit semaphore.
func NewGate(cooldown time.Duration) *Gate {
	return &Gate{
		cooldown: cooldown,
		permits:  make(chan struct{}, watchPermits),
		inFlight: map[string]bool{},
		pending:  map[string]bool{},
		last:     map[string]time.Time{},
	}
}

// Admit filters one watcher batch through the gates and returns the roots
// that may be probed right now — at most two, one permit each. Roots not
// admitted are either dropped (cooldown, sync active) or parked as pending
// (in-flight, permits exhausted); pending roots come back from Release.
// now is injected so tests can drive the cooldown without sleeping.
func (g *Gate) Admit(roots []string, now time.Time) []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	// The batch cap comes first: a batch larger than 64 is truncated, not
	// deferred, because the remaining events are the tail of one storm and
	// the next sweep will re-cover them.
	if len(roots) > maxWatchBatch {
		roots = roots[:maxWatchBatch]
	}
	if g.syncActive {
		return nil // §13: the sync's final sweep is authoritative
	}

	var out []string
	for _, r := range roots {
		if g.inFlight[r] {
			g.pending[r] = true
			continue
		}
		if now.Sub(g.last[r]) < g.cooldown {
			continue
		}
		if len(g.inFlight) >= watchPermits {
			// Every permit is busy (some from earlier batches); park.
			g.pending[r] = true
			continue
		}
		g.admitLocked(r, now)
		out = append(out, r)
	}
	return out
}

// Release reports a finished re-probe and returns pending roots that are now
// eligible, taking a permit for each — at most two per call.
func (g *Gate) Release(root string, now time.Time) []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.inFlight[root] {
		return nil
	}
	delete(g.inFlight, root)
	<-g.permits

	var out []string
	for _, r := range g.sortedPendingLocked() {
		// Permits are held by still-in-flight repos; a freed one is the
		// one this call just returned. Never block on the semaphore.
		if len(g.inFlight) >= watchPermits {
			break
		}
		if g.inFlight[r] {
			continue
		}
		g.admitLocked(r, now) // pending re-issues bypass the cooldown
		out = append(out, r)
	}
	return out
}

// SetSync toggles the org-sync drop: while a sync runs, watcher events are
// discarded wholesale (§13), because the sync's final sweep re-covers them.
func (g *Gate) SetSync(on bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.syncActive = on
}

// admitLocked marks a root in flight, records its cooldown stamp, and takes
// a permit. Callers hold mu.
func (g *Gate) admitLocked(root string, now time.Time) {
	g.inFlight[root] = true
	g.last[root] = now
	delete(g.pending, root)
	g.permits <- struct{}{}
}

// sortedPendingLocked returns the pending roots in a stable order so
// re-issues are deterministic.
func (g *Gate) sortedPendingLocked() []string {
	out := make([]string, 0, len(g.pending))
	for r := range g.pending {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}
