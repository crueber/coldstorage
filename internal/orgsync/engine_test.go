package orgsync

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type progressEvent struct {
	done, total int
	label       string
}

func orgPath(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fleet")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func assertOutcome(t *testing.T, out []Outcome, i int, action, name string) string {
	t.Helper()
	if i >= len(out) {
		t.Fatalf("outcome %d missing: %v", i, out)
	}
	got := out[i]
	if got.Action != action || got.Name != name {
		t.Fatalf("outcome %d = %s %s, want %s %s (all: %v)", i, got.Action, got.Name, action, name, out)
	}
	return got.Detail
}

// TestEngineLifecycle walks one repo through the whole §11.3 contract
// against real file:// upstreams: clone, current, updated, diverged-skip,
// orphan-untouched — each phase leaving the checkout exactly as the rule
// demands.
func TestEngineLifecycle(t *testing.T) {
	base := t.TempDir()
	path := orgPath(t)
	bare, seed := seedBare(t, base, "alpha")

	src := Source{Provider: "github", Owner: "acme", Path: path, Protocol: "ssh"}
	opts := Opts{Path: path, Timeout: time.Minute}
	list := stubList([]Repo{{
		Name: "alpha", OwnerLogin: "acme", SSHURL: "file://" + bare,
	}}, nil)
	disk := dirList(t)

	// Fresh clone.
	sp, err := PlanSync(src, opts, disk, list)
	if err != nil {
		t.Fatal(err)
	}
	if len(sp.Plan.ToClone) != 1 || sp.Plan.ToClone[0].Name != "alpha" {
		t.Fatalf("plan = %+v, want one clone", sp.Plan)
	}
	var events []progressEvent
	out := Execute(src, sp.Plan, opts, func(done, total int, label string) {
		events = append(events, progressEvent{done, total, label})
	})
	assertOutcome(t, out, 0, "cloned", "alpha")
	if _, err := os.Stat(filepath.Join(path, "alpha", ".git")); err != nil {
		t.Fatalf("clone did not produce a checkout: %v", err)
	}
	if len(events) != 1 || events[0].total != 1 || events[0].label != "alpha" {
		t.Fatalf("progress events = %v", events)
	}

	// Second run with nothing upstream: current, not updated.
	sp, err = PlanSync(src, opts, disk, list)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sp.Plan.ToUpdate, []string{"alpha"}) {
		t.Fatalf("plan = %+v, want one update", sp.Plan)
	}
	out = Execute(src, sp.Plan, opts, nil)
	assertOutcome(t, out, 0, "current", "alpha")

	// Upstream advanced: behind means updated, nothing else touched.
	pushUpstreamCommit(t, seed, bare, "up1.txt", "one\n")
	out = Execute(src, sp.Plan, opts, nil)
	assertOutcome(t, out, 0, "updated", "alpha")

	// Divergence: a local commit plus an upstream commit. The repo is
	// skipped and left byte-identical — no merge, no rewrite.
	repo := filepath.Join(path, "alpha")
	if err := os.WriteFile(filepath.Join(repo, "local.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAll(t, repo, "local work")
	pushUpstreamCommit(t, seed, bare, "up2.txt", "two\n")
	headBefore := strings.TrimSpace(gitT(t, repo, "rev-parse", "HEAD"))

	out = Execute(src, sp.Plan, opts, nil)
	detail := assertOutcome(t, out, 0, "skipped", "alpha")
	if !strings.Contains(detail, "diverged") {
		t.Errorf("skip detail = %q, want the divergence reason", detail)
	}
	if headAfter := strings.TrimSpace(gitT(t, repo, "rev-parse", "HEAD")); headAfter != headBefore {
		t.Errorf("diverged repo moved: %s -> %s", headBefore, headAfter)
	}

	// Orphan: on disk, not listed, untouched.
	stray := filepath.Join(path, "zz-stray")
	gitT(t, path, "init", "-b", "main", stray)
	sp, err = PlanSync(src, opts, disk, list)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sp.Plan.Orphans, []string{"zz-stray"}) {
		t.Fatalf("orphans = %v, want zz-stray", sp.Plan.Orphans)
	}
	out = Execute(src, sp.Plan, opts, nil)
	assertOutcome(t, out, 0, "skipped", "alpha")
	assertOutcome(t, out, 1, "orphaned", "zz-stray")
	if _, err := os.Stat(filepath.Join(stray, ".git")); err != nil {
		t.Errorf("orphan was touched: %v", err)
	}
}

// A dirty working tree with a conflicting upstream change is skipped with
// its reason; the local edit survives byte-identically.
func TestEngineDirtyTreeSkipped(t *testing.T) {
	base := t.TempDir()
	path := orgPath(t)
	bare, seed := seedBare(t, base, "beta")

	src := Source{Path: path, Protocol: "ssh"}
	opts := Opts{Path: path, Timeout: time.Minute}
	list := stubList([]Repo{{Name: "beta", SSHURL: "file://" + bare}}, nil)
	sp, err := PlanSync(src, opts, dirList(t), list)
	if err != nil {
		t.Fatal(err)
	}
	Execute(src, sp.Plan, opts, nil)

	// Edit the tracked file locally, then advance the same file upstream so
	// the pull would overwrite it.
	repo := filepath.Join(path, "beta")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("upstream\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAll(t, seed, "upstream edit")
	gitT(t, seed, "push", bare, "main")

	out := Execute(src, Plan{ToUpdate: []string{"beta"}}, opts, nil)
	detail := assertOutcome(t, out, 0, "skipped", "beta")
	if !strings.Contains(detail, "local changes") {
		t.Errorf("skip detail = %q, want the dirt reason", detail)
	}
	dirty, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil || string(dirty) != "dirty\n" {
		t.Errorf("dirty working tree was modified: %q, %v", dirty, err)
	}
}

// A detached HEAD is a repository-state refusal: skipped with its reason,
// HEAD untouched.
func TestEngineDetachedHeadSkipped(t *testing.T) {
	base := t.TempDir()
	path := orgPath(t)
	bare, seed := seedBare(t, base, "gamma")

	src := Source{Path: path, Protocol: "ssh"}
	opts := Opts{Path: path, Timeout: time.Minute}
	list := stubList([]Repo{{Name: "gamma", SSHURL: "file://" + bare}}, nil)
	sp, err := PlanSync(src, opts, dirList(t), list)
	if err != nil {
		t.Fatal(err)
	}
	Execute(src, sp.Plan, opts, nil)

	repo := filepath.Join(path, "gamma")
	gitT(t, repo, "checkout", "--detach", "HEAD")
	pushUpstreamCommit(t, seed, bare, "up.txt", "later\n")

	out := Execute(src, Plan{ToUpdate: []string{"gamma"}}, opts, nil)
	detail := assertOutcome(t, out, 0, "skipped", "gamma")
	if !strings.Contains(detail, "detached") {
		t.Errorf("skip detail = %q, want the detached-HEAD reason", detail)
	}
}

// An empty clone URL is a loud error row, never a doomed clone (§11.3).
func TestEngineEmptyCloneURL(t *testing.T) {
	path := orgPath(t)
	opts := Opts{Path: path, Timeout: time.Minute}

	out := Execute(Source{}, Plan{ToClone: []Repo{{Name: "ghost"}}}, opts, nil)
	if got := assertOutcome(t, out, 0, "error", "ghost"); got != "the provider listing gave no clone URL" {
		t.Errorf("detail = %q", got)
	}
	if entries, _ := os.ReadDir(path); len(entries) != 0 {
		t.Errorf("nothing must be cloned: %v", entries)
	}
}

// A failed clone removes the partial directory it created and nothing else.
func TestEngineFailedCloneCleansUp(t *testing.T) {
	path := orgPath(t)
	opts := Opts{Path: path, Timeout: time.Minute}
	missing := filepath.Join(t.TempDir(), "no-such-upstream.git")

	out := Execute(Source{}, Plan{
		ToClone: []Repo{{Name: "wreck", SSHURL: "file://" + missing}},
	}, opts, nil)
	detail := assertOutcome(t, out, 0, "error", "wreck")
	if !strings.Contains(detail, "clone failed") {
		t.Errorf("detail = %q, want the clone failure", detail)
	}
	if entries, _ := os.ReadDir(path); len(entries) != 0 {
		t.Errorf("partial clone left debris: %v", entries)
	}
}

// A pre-existing directory is never touched, even when the plan says clone.
func TestEngineNeverTouchesExistingDir(t *testing.T) {
	path := orgPath(t)
	opts := Opts{Path: path, Timeout: time.Minute}
	existing := filepath.Join(path, "kept")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "precious.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := Execute(Source{}, Plan{ToClone: []Repo{{Name: "kept", SSHURL: "file:///nowhere.git"}}}, opts, nil)
	detail := assertOutcome(t, out, 0, "error", "kept")
	if !strings.Contains(detail, "already exists") {
		t.Errorf("detail = %q", detail)
	}
	if _, err := os.Stat(filepath.Join(existing, "precious.txt")); err != nil {
		t.Errorf("pre-existing directory was touched: %v", err)
	}
}

// The engine is strictly serial: clones then updates, each bucket in name
// order, progress events one at a time with a running count.
func TestEngineStrictSerialOrder(t *testing.T) {
	base := t.TempDir()
	path := orgPath(t)
	opts := Opts{Path: path, Timeout: time.Minute}

	urls := map[string]string{}
	for _, name := range []string{"alpha", "bravo", "charlie"} {
		bare, _ := seedBare(t, base, name)
		urls[name] = "file://" + bare
	}
	repos := []Repo{
		{Name: "alpha", SSHURL: urls["alpha"]},
		{Name: "bravo", SSHURL: urls["bravo"]},
		{Name: "charlie", SSHURL: urls["charlie"]},
	}
	src := Source{Path: path, Protocol: "ssh"}

	// Phase 1: clone all three; phase 2: all three are on disk and update
	// to current. Both phases must arrive strictly sorted, one event at a
	// time, with a running count.
	var events []progressEvent
	out := Execute(src, NewPlan(repos, nil, src), opts, func(done, total int, label string) {
		events = append(events, progressEvent{done, total, label})
	})
	out = append(out, Execute(src, NewPlan(repos, []string{"alpha", "bravo", "charlie"}, src), opts, func(done, total int, label string) {
		events = append(events, progressEvent{done, total, label})
	})...)

	want := []string{"alpha", "bravo", "charlie", "alpha", "bravo", "charlie"}
	if len(events) != 6 {
		t.Fatalf("progress events = %v, want 6", events)
	}
	for i, e := range events {
		if e.done != (i%3)+1 || e.total != 3 {
			t.Errorf("event %d = (%d, %d), want (%d, 3)", i, e.done, e.total, (i%3)+1)
		}
		if e.label != want[i] {
			t.Errorf("event %d label = %q, want %q (sorted order)", i, e.label, want[i])
		}
	}
	for i, name := range want {
		action := "cloned"
		if i >= 3 {
			action = "current"
		}
		assertOutcome(t, out, i, action, name)
	}
}

// A listing failure degrades to update-only with a leading error row — it
// must never read as "the whole org is orphans" (§11.3).
func TestListingFailureDegrades(t *testing.T) {
	path := orgPath(t)
	opts := Opts{Path: path, Timeout: time.Minute}
	src := Source{Owner: "acme", Path: path, Protocol: "ssh"}

	for _, name := range []string{"app", "other"} {
		if err := os.MkdirAll(filepath.Join(path, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	disk := dirList(t)
	list := stubList(nil, errors.New("gh: auth expired"))

	if _, err := PlanSync(src, opts, disk, list); err == nil || !strings.Contains(err.Error(), "auth expired") {
		t.Fatalf("PlanSync error = %v, want the listing failure", err)
	}

	sp := ListSync(src, opts, disk, list)
	if sp.Err == nil {
		t.Fatal("ListSync must carry the listing error")
	}
	if len(sp.Plan.ToClone) != 0 || len(sp.Plan.Orphans) != 0 {
		t.Errorf("degraded plan = %+v, want update-only with no orphans", sp.Plan)
	}
	if !reflect.DeepEqual(sp.Plan.ToUpdate, []string{"app", "other"}) {
		t.Errorf("degraded ToUpdate = %v, want the disk list", sp.Plan.ToUpdate)
	}
	if len(sp.Rows) != 1 || sp.Rows[0].Action != "error" || !strings.Contains(sp.Rows[0].Detail, "auth expired") {
		t.Errorf("leading rows = %v, want one error row naming the failure", sp.Rows)
	}
}

// A missing checkout path is the NORMAL state for a new registration —
// creating it by cloning is the whole point of the first sync. PlanSync
// must plan against an empty disk, and the executor must create the path
// when the first clone lands. (The earlier behavior — failing loudly on a
// missing path — blocked every fresh registration and read as "sync did
// nothing".)
func TestPlanSyncTreatsMissingPathAsEmptyDisk(t *testing.T) {
	base := t.TempDir()
	bare, _ := seedBare(t, base, "upstream")
	absent := filepath.Join(t.TempDir(), "brand", "new", "org")
	opts := Opts{Path: absent, Timeout: time.Minute}
	src := Source{Provider: "github", Owner: "acme", Path: absent, Protocol: "ssh"}

	plan, err := PlanSync(src, opts, dirList(t), stubList([]Repo{
		{Name: "r1", OwnerLogin: "acme", SSHURL: "file://" + bare},
	}, nil))
	if err != nil {
		t.Fatalf("a missing checkout path is not an error: %v", err)
	}
	if len(plan.Plan.ToClone) != 1 || plan.Plan.ToClone[0].Name != "r1" {
		t.Fatalf("plan = %+v, want the listing cloned into the new path", plan)
	}

	out := Execute(src, plan.Plan, opts, nil)
	assertOutcome(t, out, 0, "cloned", "r1")
	if _, err := os.Stat(filepath.Join(absent, "r1", ".git")); err != nil {
		t.Fatalf("the clone created the checkout path: %v", err)
	}
}

// The wedged-repo incident: an interrupted clone left .git debris (a lone
// objects dir, no HEAD) and the repo could neither be updated (dead git
// dir) nor re-cloned (directory exists) — stuck forever.
func TestCloneOneCleansDebris(t *testing.T) {
	path := t.TempDir()
	name := "pricing"
	target := filepath.Join(path, name)
	if err := os.MkdirAll(filepath.Join(target, ".git", "objects", "pack"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(target, ".git", "objects", "pack", "tmp_rev_X"), []byte("junk"), 0o644)

	// A local origin to clone from keeps the test network-free.
	origin := t.TempDir() + "/origin.git"
	if out, err := exec.Command("git", "init", "-q", "--bare", origin).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v: %s", err, out)
	}
	seed := t.TempDir() + "/seed"
	runGitCmd(t, "clone", "-q", origin, seed)
	runGitCmd(t, "-C", seed, "config", "user.email", "t@t")
	runGitCmd(t, "-C", seed, "config", "user.name", "t")
	os.WriteFile(filepath.Join(seed, "f.txt"), []byte("one"), 0o644)
	runGitCmd(t, "-C", seed, "add", ".")
	runGitCmd(t, "-C", seed, "commit", "-q", "-m", "one")
	runGitCmd(t, "-C", seed, "push", "-q", "origin", "HEAD")

	src := Source{Provider: "github", Host: "github.com", Owner: "acme", Path: path, Protocol: "https"}
	repos := []Repo{{Name: name, OwnerLogin: "acme", HTTPSURL: origin}}

	// The disk list comes from orgDiskRepos, which excludes clone debris —
	// so the plan classifies pricing as ToClone and cloneOne clears the
	// corpse before cloning.
	out := Execute(src, NewPlan(repos, nil, src), Opts{Path: path, Timeout: time.Minute}, func(int, int, string) {})
	if len(out) == 0 || out[0].Action != "cloned" {
		t.Fatalf("outcome = %+v, want a clone", out)
	}
	head := filepath.Join(target, ".git", "HEAD")
	if _, err := os.Stat(head); err != nil {
		t.Fatalf("debris was not replaced with a clone: %v", err)
	}
}

func TestIsCloneDebris(t *testing.T) {
	dead := t.TempDir()
	os.MkdirAll(filepath.Join(dead, ".git", "objects"), 0o755)
	if !IsCloneDebris(dead) {
		t.Error("a .git dir with no HEAD and no user files is debris")
	}

	live := t.TempDir()
	os.MkdirAll(filepath.Join(live, ".git"), 0o755)
	os.WriteFile(filepath.Join(live, ".git", "HEAD"), []byte("ref: refs/heads/main"), 0o644)
	if IsCloneDebris(live) {
		t.Error("a live checkout is not debris")
	}

	mixed := t.TempDir()
	os.MkdirAll(filepath.Join(mixed, ".git", "objects"), 0o755)
	os.WriteFile(filepath.Join(mixed, "main.go"), []byte("package x"), 0o644)
	if IsCloneDebris(mixed) {
		t.Error("a directory with user files outside .git is not debris")
	}

	bare := t.TempDir()
	os.MkdirAll(filepath.Join(bare, "refs"), 0o755)
	os.WriteFile(filepath.Join(bare, "HEAD"), []byte("ref: refs/heads/main"), 0o644)
	if IsCloneDebris(bare) {
		t.Error("a bare repo is not debris")
	}
}

func TestSyncCheckoutNamesBrokenCheckout(t *testing.T) {
	dead := t.TempDir()
	os.MkdirAll(filepath.Join(dead, ".git", "objects"), 0o755)
	out := SyncCheckout(dead, time.Minute)
	if out.Action != "error" {
		t.Fatalf("action = %q, want error", out.Action)
	}
	if !strings.Contains(out.Detail, "broken checkout") {
		t.Errorf("detail = %q, want the broken-checkout guidance", out.Detail)
	}
}

func runGitCmd(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
