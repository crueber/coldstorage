package watcher

// Debouncing turns an event storm into one batch per quiet window (spec §13).
// A single `git commit` touches HEAD, index, logs and ref files in a burst;
// without a window the dashboard would schedule a re-probe per write. The
// window resets on every new repo signal, so a batch is flushed once the
// storm has settled, carrying each changed repo exactly once.

import (
	"sort"
	"sync"
	"time"
)

// debouncer accumulates changed repo roots and signals a flush once no new
// signal has arrived for the debounce duration. It is only ever touched from
// the watcher's single event-loop goroutine, but the mutex keeps that an
// implementation detail rather than a precondition.
type debouncer struct {
	d       time.Duration
	mu      sync.Mutex
	pending map[string]bool
	timer   *time.Timer
}

// newDebouncer returns a debouncer whose timer starts stopped: the C channel
// must stay silent until the first mark.
func newDebouncer(d time.Duration) *debouncer {
	b := &debouncer{d: d, pending: make(map[string]bool)}
	b.timer = time.NewTimer(d)
	if !b.timer.Stop() {
		select {
		case <-b.timer.C:
		default:
		}
	}
	return b
}

// C exposes the flush timer's channel to the event loop.
func (b *debouncer) C() <-chan time.Time { return b.timer.C }

// mark records a changed repo and restarts the quiet window. The Stop/Reset
// dance drains a timer that already fired so no stale flush can preempt a
// pending batch.
func (b *debouncer) mark(repo string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending[repo] = true
	if !b.timer.Stop() {
		select {
		case <-b.timer.C:
		default:
		}
	}
	b.timer.Reset(b.d)
}

// take returns the accumulated repo roots, sorted for deterministic batches,
// and starts a fresh window's worth of accumulation.
func (b *debouncer) take() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) == 0 {
		return nil
	}
	batch := make([]string, 0, len(b.pending))
	for repo := range b.pending {
		batch = append(batch, repo)
	}
	b.pending = make(map[string]bool)
	sort.Strings(batch)
	return batch
}

// stop silences the timer at shutdown; the event loop has already exited by
// the time this runs, so nothing can observe a final tick.
func (b *debouncer) stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.timer.Stop()
}
