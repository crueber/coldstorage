package watcher

import (
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// stubWatcher stands in for the platform watcher so the reconcile diff, the
// sequence guard and the budget-exhaustion path can be observed directly.
type stubWatcher struct {
	mu        sync.Mutex
	adds      []string
	removes   []string
	failAfter int // adds beyond this many return ENOSPC; negative never fails
	events    chan fsnotify.Event
	errs      chan error
}

func newStub(failAfter int) *stubWatcher {
	return &stubWatcher{
		failAfter: failAfter,
		events:    make(chan fsnotify.Event, 16),
		errs:      make(chan error, 16),
	}
}

func (s *stubWatcher) Add(p string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failAfter >= 0 && len(s.adds) >= s.failAfter {
		return syscall.ENOSPC
	}
	s.adds = append(s.adds, p)
	return nil
}

func (s *stubWatcher) Remove(p string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removes = append(s.removes, p)
	return nil
}

func (s *stubWatcher) Close() error { return nil }

func (s *stubWatcher) Events() <-chan fsnotify.Event { return s.events }
func (s *stubWatcher) Errors() <-chan error          { return s.errs }

func (s *stubWatcher) emit(p string) { s.events <- fsnotify.Event{Name: p, Op: fsnotify.Write} }

func (s *stubWatcher) counts() (adds, removes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.adds), len(s.removes)
}

func (s *stubWatcher) addSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.adds...)
}

func (s *stubWatcher) removeSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.removes...)
}

// mkMiniRepo is the smallest repo shape: a checkout with just a .git dir.
func mkMiniRepo(t *testing.T, root, name string) string {
	t.Helper()
	repo := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return repo
}

// waitFor polls cond until it holds or the deadline passes, failing the test
// with a timeout rather than hanging.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// registeredIs compares the watcher's registered dirs, sorted, to want.
func registeredIs(w *Watcher, want ...string) bool {
	got := w.registeredDirs()
	if len(got) != len(want) {
		return false
	}
	sorted := append([]string(nil), want...)
	sort.Strings(sorted)
	for i := range sorted {
		if got[i] != sorted[i] {
			return false
		}
	}
	return true
}

func TestReconcileDiff(t *testing.T) {
	root := t.TempDir()
	a := mkMiniRepo(t, root, "a")
	b := mkMiniRepo(t, root, "b")

	stub := newStub(-1)
	w := newWith(stub, 25*time.Millisecond, nil, nil)
	defer w.Close()

	w.reconcileNow([]string{a})
	if !registeredIs(w, a, filepath.Join(a, ".git")) {
		t.Fatalf("after first reconcile: %v", w.registeredDirs())
	}

	w.reconcileNow([]string{a, b})
	if !registeredIs(w, a, filepath.Join(a, ".git"), b, filepath.Join(b, ".git")) {
		t.Fatalf("after add: %v", w.registeredDirs())
	}

	w.reconcileNow([]string{b})
	if !registeredIs(w, b, filepath.Join(b, ".git")) {
		t.Fatalf("after remove: %v", w.registeredDirs())
	}
	// Only a's watches were released, and only a's: the removal list names
	// exactly the two dirs a owned, and nothing of b's set.
	removed := stub.removeSnapshot()
	sort.Strings(removed)
	want := []string{filepath.Join(a, ".git"), a}
	sort.Strings(want)
	if len(removed) != len(want) {
		t.Fatalf("removals = %v, want %v", removed, want)
	}
	for i := range want {
		if removed[i] != want[i] {
			t.Errorf("removals[%d] = %q, want %q", i, removed[i], want[i])
		}
	}
}

func TestReconcileIdempotent(t *testing.T) {
	root := t.TempDir()
	a := mkMiniRepo(t, root, "a")

	stub := newStub(-1)
	w := newWith(stub, 25*time.Millisecond, nil, nil)
	defer w.Close()

	w.reconcileNow([]string{a})
	addsBefore, removesBefore := stub.counts()

	w.reconcileNow([]string{a})
	addsAfter, removesAfter := stub.counts()

	if addsAfter != addsBefore || removesAfter != removesBefore {
		t.Errorf("second reconcile churned: adds %d→%d, removes %d→%d",
			addsBefore, addsAfter, removesBefore, removesAfter)
	}
}

func TestReconcileSequenceGuard(t *testing.T) {
	root := t.TempDir()
	a := mkMiniRepo(t, root, "a")
	b := mkMiniRepo(t, root, "b")

	w := newWith(newStub(-1), 25*time.Millisecond, nil, nil)
	defer w.Close()

	// Two overlapping reconciles: whichever walk finishes late must not
	// overwrite the newest request's watch set.
	w.Reconcile([]string{a})
	w.Reconcile([]string{b})

	want := []string{b, filepath.Join(b, ".git")}
	waitFor(t, "newest reconcile applied", func() bool { return registeredIs(w, want...) })
	time.Sleep(150 * time.Millisecond) // give a straggling older walk its chance
	if !registeredIs(w, want...) {
		t.Fatalf("older reconcile overwrote newer: %v", w.registeredDirs())
	}
}

func TestWatcherDeliversOwningRepo(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "alpha")
	mkCheckout(t, repo)

	w, err := New(50*time.Millisecond, nil, []string{repo})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	batches := make(chan []string, 16)
	go func() {
		for b := range w.Events() {
			batches <- b
		}
	}()

	waitFor(t, "watch set registered", func() bool {
		reg := w.registeredDirs()
		return contains(reg, repo) && contains(reg, filepath.Join(repo, "src")) &&
			contains(reg, filepath.Join(repo, ".git"))
	})

	if err := os.WriteFile(filepath.Join(repo, "src", "main.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case batch := <-batches:
		if !contains(batch, repo) {
			t.Fatalf("batch %v does not name the owning repo", batch)
		}
		if len(batch) != 1 {
			t.Fatalf("batch %v should carry exactly one repo", batch)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no batch delivered for a working-file edit")
	}
}

func TestWatcherFiltersGitNoise(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "alpha")
	mkCheckout(t, repo)

	w, err := New(50*time.Millisecond, nil, []string{repo})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	batches := make(chan []string, 16)
	go func() {
		for b := range w.Events() {
			batches <- b
		}
	}()

	waitFor(t, "watch set registered", func() bool {
		return contains(w.registeredDirs(), filepath.Join(repo, ".git"))
	})

	// ORIG_HEAD sits in the watched .git dir but cannot change an answer:
	// it must be dropped on sight (spec §13).
	if err := os.WriteFile(filepath.Join(repo, ".git", "ORIG_HEAD"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case batch := <-batches:
		t.Fatalf("uninteresting git path produced batch %v", batch)
	case <-time.After(400 * time.Millisecond):
	}

	// config is allowlisted and must get through.
	if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case batch := <-batches:
		if !contains(batch, repo) {
			t.Fatalf("batch %v does not name the owning repo", batch)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no batch delivered for an allowlisted git path")
	}
}

func TestBudgetFailureWarnsOnce(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "alpha")
	mkCheckout(t, repo)

	stub := newStub(1) // the first registration succeeds; the budget is then spent
	w := newWith(stub, 25*time.Millisecond, nil, nil)
	defer w.Close()

	w.reconcileNow([]string{repo})

	select {
	case err := <-w.Warns():
		if err == nil {
			t.Fatal("nil warning on the warns channel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no warning after budget exhaustion")
	}

	// Watching continues with what registered: the repo root made it, and a
	// change under it still reaches the batch.
	if !registeredIs(w, repo) {
		t.Fatalf("registered after ENOSPC = %v, want just the repo root", w.registeredDirs())
	}
	stub.emit(filepath.Join(repo, "notes.txt"))
	select {
	case batch := <-w.Events():
		if !contains(batch, repo) {
			t.Fatalf("batch %v does not name the owning repo", batch)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("partial watch set stopped delivering events")
	}

	// Retries that keep failing must not produce a second warning.
	w.reconcileNow([]string{repo})
	select {
	case err := <-w.Warns():
		t.Fatalf("second warning emitted: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	root := t.TempDir()
	a := mkMiniRepo(t, root, "a")

	stub := newStub(-1)
	w := newWith(stub, 25*time.Millisecond, nil, []string{a})
	waitFor(t, "registration", func() bool { return len(w.registeredDirs()) > 0 })

	w.Close()
	w.Close() // second close must not panic

	if _, ok := <-w.Events(); ok {
		t.Error("Events still open after Close")
	}
	// A reconcile after close is a no-op, not a panic and not a registration.
	addsBefore, _ := stub.counts()
	w.Reconcile([]string{a})
	time.Sleep(100 * time.Millisecond)
	addsAfter, _ := stub.counts()
	if addsAfter != addsBefore {
		t.Errorf("post-close reconcile registered %d more watches", addsAfter-addsBefore)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
