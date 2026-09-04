// Package watcher keeps per-repo selective watch sets over a fleet of git
// repositories and turns raw filesystem events into debounced batches of
// owning repo roots (spec §13).
//
// The design exists because of one incident: a recursive watch on a scan
// root consumed one inotify watch per directory, .git/objects growth ate the
// ~59k kernel budget at around a thousand repos, the failures were silent,
// and the dashboard degraded to periodic sweeps while two git processes ran
// forever. Nothing here watches a scan root recursively. Each repo gets the
// short, audited set composed in set.go, reconcile keeps the sets aligned
// with reality after every sweep, and events that cannot change an answer —
// .git/objects above all — are dropped on sight.
package watcher

import (
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/crueber/coldstorage/internal/discovery"
)

// notifier is the slice of the platform watcher this package needs. It
// exists so the budget-exhaustion path — a registration failing with no
// space left on the kernel's watch budget — can be unit-tested against an
// injected fake instead of a sysctl.
type notifier interface {
	Add(path string) error
	Remove(path string) error
	Close() error
	Events() <-chan fsnotify.Event
	Errors() <-chan error
}

// fsWatch adapts *fsnotify.Watcher, whose event and error fields are
// channels rather than accessors.
type fsWatch struct{ *fsnotify.Watcher }

func (f fsWatch) Events() <-chan fsnotify.Event { return f.Watcher.Events }
func (f fsWatch) Errors() <-chan error          { return f.Watcher.Errors }

// Watcher maintains the fleet's watch sets. Create one with New, feed it
// Reconcile after every discovery sweep, and drain Events from the TUI. The
// zero usage contract is small on purpose: the watcher never probes git
// itself, it only names the repos whose state may have changed — the
// fingerprint gate (spec §6) decides what a re-probe actually costs.
type Watcher struct {
	n     notifier
	prune map[string]bool

	mu      sync.RWMutex
	repos   map[string]map[string]bool // repo root -> registered watch dirs
	dirRepo map[string]string          // registered watch dir -> owning repo root
	gitDirs map[string]string          // repo root -> its git dir (for filtering)
	closed  bool

	// seq guards concurrent reconciles: only the newest request may apply a
	// diff. An older walk that finishes late must never overwrite the watch
	// set the newest reconcile already put in place.
	seq      atomic.Uint64
	warnGate sync.Once

	deb    *debouncer
	done   chan struct{}
	wg     sync.WaitGroup
	events chan []string
	warns  chan error
}

// New creates a watcher over the given repos. Debounce is the quiet window
// a batch waits for after the last event; Prune lists extra directory names
// to skip when bounding working-tree watches, on top of
// discovery.DefaultPrune. Registration itself happens in the background:
// walking a 500-repo fleet must never block the caller.
func New(debounce time.Duration, prune []string, repos []string) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return newWith(fsWatch{fw}, debounce, prune, repos), nil
}

// newWith wires a watcher around an injected platform watcher. The fsnotify
// watcher's Add/Remove calls are safe for concurrent use, and all state
// mutation is funneled through the reconcile paths below.
func newWith(n notifier, debounce time.Duration, prune []string, repos []string) *Watcher {
	w := &Watcher{
		n:       n,
		prune:   pruneSet(prune),
		repos:   make(map[string]map[string]bool),
		dirRepo: make(map[string]string),
		gitDirs: make(map[string]string),
		deb:     newDebouncer(debounce),
		done:    make(chan struct{}),
		events:  make(chan []string, 8),
		warns:   make(chan error, 1),
	}
	w.wg.Add(1)
	go w.run()
	if len(repos) > 0 {
		w.Reconcile(repos)
	}
	return w
}

// pruneSet merges the configured extra prune names with discovery's
// defaults, so both layers agree on which directories are never worth a
// watch.
func pruneSet(extra []string) map[string]bool {
	set := make(map[string]bool, len(discovery.DefaultPrune)+len(extra))
	for _, name := range discovery.DefaultPrune {
		set[name] = true
	}
	for _, name := range extra {
		set[name] = true
	}
	return set
}

// Events delivers debounced batches of repo roots whose watch sets produced
// an interesting event. The channel closes on Close.
func (w *Watcher) Events() <-chan []string { return w.events }

// Warns delivers the single registration warning a watcher may ever emit —
// the spec's contract is "warn exactly once and watching continues", because
// a storm of ENOSPC errors after budget exhaustion is noise on top of
// degradation. The channel is buffered and never blocks its sender; it is
// deliberately never closed, since reconcile goroutines outlive Close.
func (w *Watcher) Warns() <-chan error { return w.warns }

// Reconcile aligns the watch sets with the given repo list, diffing desired
// against registered so an unchanged fleet costs nothing on the platform
// side. The walk runs on its own goroutine — reconciling a large fleet is a
// filesystem walk, and the TUI calls this after every sweep — guarded by a
// sequence counter so a slow, older reconcile can never clobber a newer
// one's result.
func (w *Watcher) Reconcile(repos []string) {
	want := make([]string, 0, len(repos))
	for _, repo := range repos {
		if !filepath.IsAbs(repo) {
			if abs, err := filepath.Abs(repo); err == nil {
				repo = abs
			}
		}
		want = append(want, filepath.Clean(repo))
	}
	n := w.seq.Add(1)
	go w.reconcile(n, want)
}

// reconcileNow applies a reconcile synchronously; the tests' window into
// what Reconcile does asynchronously.
func (w *Watcher) reconcileNow(repos []string) {
	w.reconcile(w.seq.Add(1), repos)
}

// reconcile composes the desired watch sets and, if this request is still
// the newest, applies the diff.
func (w *Watcher) reconcile(n uint64, repos []string) {
	desired := make(map[string]map[string]bool, len(repos))
	gitDirs := make(map[string]string, len(repos))
	for _, repo := range repos {
		rw := watchSetFor(repo, w.prune)
		if len(rw.Targets) == 0 {
			continue // missing or unreadable: no watches, never a failure
		}
		dirs := expandTargets(rw.Targets, maxWorkingDirsPerRepo)
		if len(dirs) == 0 {
			continue
		}
		set := make(map[string]bool, len(dirs))
		for _, d := range dirs {
			set[d] = true
		}
		desired[repo] = set
		gitDirs[repo] = rw.GitDir
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.seq.Load() != n {
		return // a newer reconcile owns the watch set now
	}
	w.applyDiff(desired, gitDirs)
}

// applyDiff mutates registration toward the desired state. Removals go
// first so a vanishing repo frees its watches before additions contend for
// the same budget; registration failures above the kernel's watch limit warn
// once and are otherwise ignored, because watching with a partial set is the
// specified degradation.
func (w *Watcher) applyDiff(desired map[string]map[string]bool, gitDirs map[string]string) {
	for repo, dirs := range w.repos {
		keep := desired[repo]
		if keep == nil {
			for d := range dirs {
				_ = w.n.Remove(d) // the dir may already be gone; Remove noise is not a signal
				delete(w.dirRepo, d)
			}
			delete(w.repos, repo)
			delete(w.gitDirs, repo)
			continue
		}
		for d := range dirs {
			if !keep[d] {
				_ = w.n.Remove(d)
				delete(dirs, d)
				delete(w.dirRepo, d)
			}
		}
	}

	for repo, dirs := range desired {
		registered := w.repos[repo]
		// Registration proceeds in sorted order so the repo root always
		// claims a watch before its children: when the budget gives out
		// partway through, what survives is predictable and the fallback
		// signal (the root) is the last thing to be dropped.
		pending := make([]string, 0, len(dirs))
		for d := range dirs {
			pending = append(pending, d)
		}
		sort.Strings(pending)
		for _, d := range pending {
			if registered[d] {
				continue
			}
			if err := w.n.Add(d); err != nil {
				if isBudgetErr(err) {
					w.warn(err)
				}
				// Any other failure (a dir that vanished mid-walk, a
				// permission change) is transient noise: skip the dir and
				// keep registering the rest.
				continue
			}
			if registered == nil {
				registered = make(map[string]bool)
				w.repos[repo] = registered
			}
			registered[d] = true
			w.dirRepo[d] = repo
		}
		w.gitDirs[repo] = gitDirs[repo]
	}
}

// run is the single event-loop goroutine: it consumes platform events,
// filters them, and flushes debounced batches. It exits when the notifier
// closes its channels or Close signals done, whichever comes first.
func (w *Watcher) run() {
	defer w.wg.Done()
	for {
		select {
		case ev, ok := <-w.n.Events():
			if !ok {
				return
			}
			w.handle(ev)
		case err, ok := <-w.n.Errors():
			if !ok {
				return
			}
			if err != nil {
				w.warn(err)
			}
		case <-w.deb.C():
			if batch := w.deb.take(); len(batch) > 0 {
				select {
				case w.events <- batch:
				case <-w.done:
					return
				}
			}
		case <-w.done:
			return
		}
	}
}

// handle routes one platform event to its owning repo and, if the path can
// change an answer, feeds the debouncer. Unowned and uninteresting paths
// die here, never in a re-probe.
func (w *Watcher) handle(ev fsnotify.Event) {
	w.mu.RLock()
	repo, gitDir := w.ownerOf(ev.Name)
	w.mu.RUnlock()
	if repo == "" {
		return
	}
	if !interestingPath(repo, gitDir, ev.Name) {
		return
	}
	w.deb.mark(repo)
}

// ownerOf maps an event path to its repo by walking up until a registered
// watch dir matches. The walk handles both containment (an event anywhere
// under a registered dir) and the git-dir aliasing of linked worktrees,
// whose watch dirs live outside the repo root.
func (w *Watcher) ownerOf(path string) (repo, gitDir string) {
	p := path
	for {
		if r, ok := w.dirRepo[p]; ok {
			return r, w.gitDirs[r]
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", ""
		}
		p = parent
	}
}

// interestingPath applies the §13 interest rule: a path is interesting only
// if it can change an answer. Working files qualify; inside a git dir only
// the ref, reflog, worktree and operation-marker paths qualify, and a path
// that wandered in from outside the repo qualifies for nothing.
func interestingPath(repo, gitDir, path string) bool {
	if gitDir != "" && under(path, gitDir) {
		rel, ok := relTo(path, gitDir)
		if !ok {
			return false
		}
		return gitInteresting(rel)
	}
	rel, ok := relTo(path, repo)
	if !ok {
		return false
	}
	if suffix, ok := gitCut(rel); ok {
		// A nested checkout's .git lives inside this repo's tree; route it
		// through the same allowlist so object churn stays noise.
		return gitInteresting(suffix)
	}
	return true
}

// gitInteresting is the git-dir allowlist: the state files, ref and reflog
// trees, worktree bookkeeping and the operation markers whose presence
// decides repo state. Everything else — objects above all — is dropped on
// sight.
func gitInteresting(rel string) bool {
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == "" {
		return true // the git dir itself: cheap re-probe trigger
	}
	switch rel {
	case "HEAD", "index", "packed-refs", "FETCH_HEAD", "config",
		"MERGE_HEAD", "REVERT_HEAD", "CHERRY_PICK_HEAD", "BISECT_LOG", "shallow":
		return true
	}
	for _, prefix := range []string{"refs/", "logs/", "worktrees/", "rebase-merge/", "rebase-apply/"} {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

// gitCut reports the portion of a repo-relative path that follows a ".git"
// segment, if any — the "cut" that lets nested checkouts share the
// allowlist with top-level git dirs.
func gitCut(rel string) (suffix string, ok bool) {
	rel = filepath.ToSlash(rel)
	switch {
	case rel == ".git":
		return ".", true
	case strings.HasPrefix(rel, ".git/"):
		return rel[len(".git/"):], true
	}
	if i := strings.Index(rel, "/.git/"); i >= 0 {
		return rel[i+len("/.git/"):], true
	}
	return "", false
}

// under reports whether path is dir itself or lies somewhere beneath it.
func under(path, dir string) bool {
	rel, ok := relTo(path, dir)
	return ok && rel != ".."
}

// relTo relates path to base, rejecting paths outside it.
func relTo(path, base string) (string, bool) {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// isBudgetErr reports whether a registration failure means the kernel's
// watch budget is spent (no space left on device) or the descriptor table
// is (too many open files) — the two failures the spec says to warn about
// exactly once and otherwise ignore.
func isBudgetErr(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EMFILE)
}

// warnOnce emits the watcher's single lifetime warning, dropping it if no
// one is reading Warns. A lost warning must never block the event loop.
func (w *Watcher) warn(err error) {
	w.warnGate.Do(func() {
		select {
		case w.warns <- err:
		default:
		}
	})
}

// Close stops the event loop and releases every platform watch. Events
// closes; Warns does not. Reconcile calls after Close are no-ops.
func (w *Watcher) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.mu.Unlock()

	close(w.done)
	_ = w.n.Close() // closes the platform channels, ending the run loop
	w.wg.Wait()
	w.deb.stop()
	close(w.events)
}

// registeredDirs snapshots the currently registered watch dirs; used by
// tests to wait for background registration to land.
func (w *Watcher) registeredDirs() []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]string, 0, len(w.dirRepo))
	for d := range w.dirRepo {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}
