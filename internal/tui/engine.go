// The engine is the dashboard's background pipeline (GO-PORT-SPEC.md §12,
// §13, §14): discovery walks, fingerprint-gated per-repo probes, watcher
// event handling behind the §13 storm gates, and cache writes. Everything
// here runs on its own goroutines and reaches the UI only through send(),
// which the program wires to (*tea.Program).Send — background work may never
// block the UI thread, and a flood of probe results may cost the UI at most
// one batch of latency (§14). That is what the collector below buys: probe
// results stream into a channel and are handed to the UI in batches of at
// most 32, with a UI drain between batches.
package tui

import (
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crueber/coldstorage/internal/config"
	"github.com/crueber/coldstorage/internal/discovery"
	"github.com/crueber/coldstorage/internal/gitmode"
	"github.com/crueber/coldstorage/internal/watcher"
)

// maxBatch is the §14 batch bound: background results reach the UI in
// batches of at most this many repos.
const maxBatch = 32

// engine owns all background work for one dashboard session.
type engine struct {
	send func(msg any) // wired to the program after construction

	cfg        config.Config
	refsOpts   gitmode.RefsOptions
	workOpts   gitmode.WorkOptions
	cacheDir   string
	watchOn    bool
	watchDebnc time.Duration

	gate *Gate
	sem  chan struct{} // probe concurrency cap

	results chan RepoState
	quit    chan struct{}
	wg      sync.WaitGroup

	sweeping atomic.Bool
	probing  atomic.Int64

	mu      sync.Mutex
	cache   map[string]RepoState // last good row per root, for the fingerprint gate
	closing bool
	watcher *watcher.Watcher
}

// newEngine builds the engine over the config and the startup cache. The
// send func is assigned by the caller before Start; engine methods that run
// before wiring simply drop their messages, which keeps the engine testable
// without a running program.
func newEngine(cfg config.Config, cacheDir string, cache map[string]RepoState) *engine {
	concurrency := cfg.Remote.Concurrency
	if concurrency <= 0 {
		concurrency = 4
	}
	debounce := time.Second
	if d, err := config.ParseDuration(cfg.Refresh.Debounce); err == nil {
		debounce = d
	}
	e := &engine{
		cfg:        cfg,
		cacheDir:   cacheDir,
		watchOn:    cfg.Refresh.Watch,
		watchDebnc: debounce,
		refsOpts: gitmode.RefsOptions{
			TagPattern:     cfg.Release.TagPattern,
			MaxSubjects:    cfg.Release.MaxSubjects,
			ChangelogFiles: cfg.Release.ChangelogFiles,
			ReadChangelog:  cfg.Release.ReadChangelog,
		},
		workOpts: gitmode.WorkOptions{
			Untracked: gitmode.UntrackedMode(cfg.Status.Untracked),
			MaxFiles:  cfg.Status.MaxFiles,
		},
		gate:    NewGate(30 * time.Second),
		sem:     make(chan struct{}, concurrency),
		results: make(chan RepoState, maxBatch*2),
		quit:    make(chan struct{}),
		cache:   map[string]RepoState{},
	}
	for root, row := range cache {
		if row.Err == nil {
			e.cache[root] = row
		}
	}
	e.wg.Add(1)
	go e.collect()
	return e
}

// Busy reports whether background probes are outstanding — the UI uses it to
// pick the tick cadence and the spinner.
func (e *engine) Busy() bool { return e.sweeping.Load() || e.probing.Load() > 0 }

// Start begins the startup sweep unless one is already running.
func (e *engine) Start() { e.Sweep(false) }

// Sweep launches a full sweep: discovery walk, watcher reconcile, then a
// fingerprint-gated probe of every discovered repo (§6). Force bypasses the
// fingerprint gate (R / ctrl-r: the user asked for a real re-probe).
func (e *engine) Sweep(force bool) {
	e.mu.Lock()
	if e.sweeping.Load() || e.closing {
		e.mu.Unlock()
		return
	}
	e.sweeping.Store(true)
	e.mu.Unlock()

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer e.sweeping.Store(false)
		e.runSweep(force)
	}()
}

// WatchEvents hands one watcher batch to the storm gates and re-probes
// whatever they admit (§13). Watcher events always bypass the fingerprint
// gate — the event exists because something moved (§6).
func (e *engine) WatchEvents(roots []string) {
	for _, root := range e.gate.Admit(roots, time.Now()) {
		e.probeAsync(root, "", "")
	}
}

// SetSyncActive drops watcher events while an org sync runs (§13).
func (e *engine) SetSyncActive(on bool) { e.gate.SetSync(on) }

// Close stops the engine and waits for its goroutines. The watcher, if one
// was created, is closed with it.
func (e *engine) Close() {
	e.mu.Lock()
	if e.closing {
		e.mu.Unlock()
		return
	}
	e.closing = true
	w := e.watcher
	e.mu.Unlock()

	close(e.quit)
	if w != nil {
		w.Close()
	}
	e.wg.Wait()
}

// runSweep executes one sweep end to end. Phases are announced to the UI so
// the status line can show progress (§12) without the UI ever blocking.
func (e *engine) runSweep(force bool) {
	e.sendMsg(sweepPhaseMsg{phase: "discovering"})

	// One snapshot at the top: a config save landing mid-sweep (the org
	// manager's §11.4 root wiring) must not hand a running walk a
	// half-updated config. The sweep keeps the config it started with.
	cfg := e.cfgSnapshot()

	// A scan root that does not exist is silent at the filesystem level and
	// reads as "the dashboard is broken" — the typo'd-root incident left a
	// 548-repo fleet invisible because one character was missing. Surface
	// every missing root on every sweep; the message is cheap and the
	// alternative is a blank screen with no explanation.
	for _, root := range expandRoots(cfg.Roots) {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			e.sendMsg(warnMsg{err: fmt.Errorf("scan root %s does not exist", root)})
		}
	}
	repos, _, err := discovery.Discover(discovery.Options{
		Roots:             expandRoots(cfg.Roots),
		MaxDepth:          cfg.MaxDepth,
		FollowNestedRepos: cfg.FollowNestedRepos,
		FollowSymlinks:    cfg.FollowSymlinks,
		Exclude:           cfg.Exclude,
		Prune:             cfg.Prune,
	})
	if err != nil {
		e.sendMsg(warnMsg{err: err})
	}
	e.sendMsg(sweepPhaseMsg{phase: "discovered", total: len(repos)})

	if e.watchOn {
		e.ensureWatcher(rootsOf(repos), cfg.Prune)
	}
	e.reconcileWatcher(rootsOf(repos))

	e.sendMsg(sweepPhaseMsg{phase: "probing", total: len(repos)})
	var pwg sync.WaitGroup
	for _, repo := range repos {
		repo := repo
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
			e.probe(repo.Root, repo.Group, repo.Name, force)
		}()
	}
	pwg.Wait()

	e.sendMsg(sweepDoneMsg{roots: rootsOf(repos)})
	e.saveCacheAsync()
}

// probe probes one repo and streams the result to the collector. The
// fingerprint gate (§6): unless force, a repo whose fingerprint matches the
// cached one is answered from the cache without spawning a single process.
// An errored row re-probes until it succeeds, which is why Err rows never
// enter the fingerprint cache.
func (e *engine) probe(root, group, name string, force bool) {
	e.mu.Lock()
	cached, haveCached := e.cache[root]
	e.mu.Unlock()

	if !force && haveCached && cached.Fingerprint == gitmode.Fingerprint(root) {
		e.sendResult(cached)
		return
	}

	row := RepoState{Root: root, Group: group, Name: name}
	refs, err := gitmode.Refs(root, e.refsOpts)
	row.Refs = refs
	row.Err = err
	row.RefsProbedAt = time.Now()
	if err == nil && !refs.IsBare {
		work, key, werr := gitmode.Work(root, e.workOpts)
		row.Work = &work
		row.WorkKey = key
		row.WorkProbedAt = time.Now()
		if werr != nil {
			row.Err = werr
		}
	}
	row.Fingerprint = gitmode.Fingerprint(root)
	if row.Err == nil {
		e.mu.Lock()
		e.cache[root] = row
		e.mu.Unlock()
	}
	e.sendResult(row)
}

// sendResult delivers one probe result to the collector without ever
// blocking on a full channel after shutdown: probes that park on send hold
// their goroutine, and pwg.Wait/Close would wait for them forever.
func (e *engine) sendResult(row RepoState) {
	select {
	case e.results <- row:
	case <-e.quit:
	}
}

// probeAsync probes one watcher-admitted repo on its own goroutine and runs
// the pending re-issue chain when it finishes (§13): a change that landed
// while the repo was already in flight is re-probed immediately, bypassing
// both the fingerprint gate and the cooldown.
func (e *engine) probeAsync(root, group, name string) {
	e.mu.Lock()
	closing := e.closing
	e.mu.Unlock()
	if closing {
		return
	}

	e.probing.Add(1)
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer e.probing.Add(-1)
		e.probe(root, group, name, true)
		// The re-issue chain runs to a fixpoint: Release admits pending
		// roots (in-flight + permit) and returns the next wave, and a wave
		// admitted but never released would hold its permit forever,
		// starving every later re-probe.
		pending := e.gate.Release(root, time.Now())
		for len(pending) > 0 {
			var next []string
			for _, r := range pending {
				e.probe(r, e.groupOf(r), e.nameOf(r), true)
				next = append(next, e.gate.Release(r, time.Now())...)
			}
			pending = next
		}
	}()
}

// collect batches probe results for the UI (§14): at most 32 per message,
// with a short coalescing window so a fleet sweep lands as a handful of
// messages instead of one per repo.
func (e *engine) collect() {
	defer e.wg.Done()
	for {
		select {
		case r := <-e.results:
			batch := []RepoState{r}
		drain:
			for len(batch) < maxBatch {
				select {
				case r := <-e.results:
					batch = append(batch, r)
				case <-time.After(2 * time.Millisecond):
					break drain
				case <-e.quit:
					e.sendMsg(probeBatchMsg{repos: batch})
					return
				}
			}
			e.sendMsg(probeBatchMsg{repos: batch})
		case <-e.quit:
			return
		}
	}
}

// ensureWatcher creates the fleet watcher once, after the first discovery
// walk, and drains its events and warnings for the rest of the session
// (§13). Registration failures surface as warnings, once.
func (e *engine) ensureWatcher(repoRoots []string, prune []string) {
	e.mu.Lock()
	if e.watcher != nil || !e.watchOn {
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()

	w, err := watcher.New(e.watchDebnc, prune, repoRoots)
	if err != nil {
		e.sendMsg(warnMsg{err: err})
		return
	}
	e.mu.Lock()
	if e.closing || e.watcher != nil {
		e.mu.Unlock()
		w.Close()
		return
	}
	e.watcher = w
	e.mu.Unlock()

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		for {
			select {
			case batch, ok := <-w.Events():
				if !ok {
					return
				}
				e.WatchEvents(batch)
			case err := <-w.Warns():
				e.sendMsg(warnMsg{err: err})
			case <-e.quit:
				return
			}
		}
	}()
}

// reconcileWatcher re-aligns the watch sets with the fleet after a sweep
// (§13): repos appear and vanish, and the watch sets must follow.
func (e *engine) reconcileWatcher(repoRoots []string) {
	e.mu.Lock()
	w := e.watcher
	e.mu.Unlock()
	if w != nil {
		w.Reconcile(repoRoots)
	}
}

// saveCacheAsync writes the cache off the UI thread (§15), from a snapshot
// of the last good rows.
func (e *engine) saveCacheAsync() {
	e.mu.Lock()
	snap := make(map[string]RepoState, len(e.cache))
	for k, v := range e.cache {
		snap[k] = v
	}
	e.mu.Unlock()
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		select {
		case <-e.quit:
			return
		default:
		}
		if err := saveCache(e.cacheDir, snap); err != nil {
			e.sendMsg(warnMsg{err: err}) // §17: an unwritable cache is a log line
		}
	}()
}

func (e *engine) sendMsg(msg any) {
	if e.send != nil {
		e.send(msg)
	}
}

// groupOf and nameOf recover display names for a re-issued pending root.
func (e *engine) groupOf(root string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if row, ok := e.cache[root]; ok {
		return row.Group
	}
	return ""
}

func (e *engine) nameOf(root string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if row, ok := e.cache[root]; ok {
		return row.Name
	}
	return ""
}

// rootsOf projects discovered repos to their paths.
func rootsOf(repos []discovery.Repo) []string {
	out := make([]string, len(repos))
	for i, r := range repos {
		out[i] = r.Root
	}
	return out
}

// expandRoots expands ~ in configured roots; discovery expands too, but the
// watcher wants absolute paths and there is no reason to hand it tildes.
func expandRoots(rs []string) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, config.Expand(r))
	}
	sort.Strings(out)
	return out
}
