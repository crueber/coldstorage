// The repo sync (§11.3 semantics, applied from the dashboard): p pulls the
// selected repo, P pulls every discovered repo — always `pull --ff-only`,
// always under the §2 rails, and every refusal (divergence, dirt, detached
// HEAD, no upstream) is a skip with its reason, never a merge. Progress
// streams into the header's operation widget; when the pass finishes, a
// full sweep re-probes whatever moved.
package tui

import (
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/crueber/coldstorage/internal/orgsync"
)

// pullProgressMsg streams one finished repo into the operation widget.
type pullProgressMsg struct {
	done, total int
	name        string
}

// pullDoneMsg closes the sync pass with the §11.5 tally.
type pullDoneMsg struct {
	updated, current, skipped, failed int
}

// Pulling reports whether a repo sync pass is running.
func (e *engine) Pulling() bool { return e.pulling.Load() }

// SyncRepos pulls the given roots in bounded parallel, one §2-rails
// `pull --ff-only` per repo. It refuses while a sweep or another sync pass
// owns the background (the UI says so): interleaving a pull pass with a
// sweep double-spawns git across the fleet for no information.
func (e *engine) SyncRepos(roots []string) bool {
	if !e.pulling.CompareAndSwap(false, true) {
		return false
	}
	if e.sweeping.Load() {
		e.pulling.Store(false)
		return false
	}
	roots = append([]string{}, roots...)
	sort.Strings(roots)

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer e.pulling.Store(false)
		e.runSync(roots)
	}()
	return true
}

func (e *engine) runSync(roots []string) {
	// While the pulls run, watcher events are dropped wholesale (§13) —
	// the closing sweep re-covers whatever moved.
	if e.watchOn {
		e.SetSyncActive(true)
		defer e.SetSyncActive(false)
	}

	timeout := e.pullTimeout
	total := len(roots)
	var done atomic.Int64
	var mu sync.Mutex
	tally := map[string]int{}

	var pwg sync.WaitGroup
	for _, root := range roots {
		root := root
		pwg.Add(1)
		go func() {
			defer pwg.Done()
			e.sem <- struct{}{}
			defer func() { <-e.sem }()
			select {
			case <-e.quit:
				return
			default:
			}
			out := orgsync.SyncCheckout(root, timeout)
			done.Add(1)
			mu.Lock()
			tally[out.Action]++
			mu.Unlock()
			e.sendMsg(pullProgressMsg{done: int(done.Load()), total: total, name: filepath.Base(root)})
		}()
	}
	pwg.Wait()

	mu.Lock()
	result := pullDoneMsg{
		updated: tally["updated"],
		current: tally["current"],
		skipped: tally["skipped"],
		failed:  tally["error"],
	}
	mu.Unlock()
	e.sendMsg(result)
}
