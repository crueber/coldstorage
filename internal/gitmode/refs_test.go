package gitmode

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitOut runs one fixture git call and returns its trimmed stdout. Date is
// optional: when set, author and committer dates are pinned, which is what
// makes tag creatordate and commit-date ordering deterministic instead of
// same-second races. Fixture processes build real repos exactly the way the
// Rust tree's tests did — plain git, no network, fixed identity.
func gitOut(t *testing.T, dir, date string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
		"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

// commitFile writes, stages, and commits one file, returning the new HEAD.
func commitFile(t *testing.T, dir, name, msg, date string) string {
	t.Helper()
	writeFile(t, dir, name, name+"\n")
	gitOut(t, dir, date, "add", name)
	gitOut(t, dir, date, "commit", "-q", "-m", msg)
	return gitOut(t, dir, date, "rev-parse", "HEAD")
}

// initRepo is a fresh checkout on branch main with one commit in it.
func initRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitOut(t, root, "", "init", "-q", "-b", "main")
	commitFile(t, root, "file1", "first", "2026-01-01T00:00:00")
	return root
}

// twoCommitRepo is a checkout with two commits — the minimum for fixtures
// that branch off HEAD~1.
func twoCommitRepo(t *testing.T) string {
	t.Helper()
	root := initRepo(t)
	commitFile(t, root, "file2", "second", "2026-01-02T00:00:00")
	return root
}

func refsWith(t *testing.T, root string, cfg RefsOptions) RefsInfo {
	t.Helper()
	info, err := Refs(root, cfg)
	if err != nil {
		t.Fatalf("Refs(%s): %v", root, err)
	}
	return info
}

func TestRefsHeadKinds(t *testing.T) {
	// A fresh init has never had a commit: HEAD names a branch no ref
	// answers, which is unborn — not detached, not an error.
	fresh := t.TempDir()
	gitOut(t, fresh, "", "init", "-q", "-b", "main")
	if head := refsWith(t, fresh, RefsOptions{}).Head; head.Kind != HeadUnborn || head.Label() != "(unborn)" {
		t.Errorf("fresh repo head = %+v (%s), want unborn/(unborn)", head, head.Label())
	}

	root := initRepo(t)
	if head := refsWith(t, root, RefsOptions{}).Head; head.Kind != HeadBranch || head.Branch != "main" || head.Label() != "main" {
		t.Errorf("head = %+v (%s), want branch main", head, head.Label())
	}

	sha := gitOut(t, root, "", "rev-parse", "HEAD")
	gitOut(t, root, "", "checkout", "-q", "--detach", "HEAD")
	if head := refsWith(t, root, RefsOptions{}).Head; head.Kind != HeadDetached || head.SHA != sha || head.Label() != "@"+sha {
		t.Errorf("head = %+v (%s), want detached at %s", head, head.Label(), sha)
	}
}

func TestRefsBranchTracking(t *testing.T) {
	// A real clone over file://: one commit ahead locally, one behind on
	// the remote, exactly the shape the ahead/behind columns render.
	origin := t.TempDir()
	gitOut(t, origin, "", "init", "--bare", "-q", "-b", "main")
	seed := t.TempDir()
	gitOut(t, seed, "", "init", "-q", "-b", "main")
	commitFile(t, seed, "file1", "first", "2026-01-01T00:00:00")
	gitOut(t, seed, "", "push", "-q", "file://"+origin, "main")

	work := t.TempDir()
	gitOut(t, work, "", "clone", "-q", "file://"+origin, ".")

	other := t.TempDir()
	gitOut(t, other, "", "clone", "-q", "file://"+origin, ".")
	commitFile(t, other, "file2", "second", "2026-01-02T00:00:00")
	gitOut(t, other, "", "push", "-q", "origin", "main")

	commitFile(t, work, "file3", "third", "2026-01-03T00:00:00")
	gitOut(t, work, "", "fetch", "-q", "origin")

	info := refsWith(t, work, RefsOptions{})
	b := info.CurrentBranch()
	if b == nil {
		t.Fatal("current branch main not found")
	}
	if b.Name != "main" || b.Upstream != "origin/main" {
		t.Errorf("branch = %s/%s, want main/origin/main", b.Name, b.Upstream)
	}
	if b.Ahead != 1 || b.Behind != 1 {
		t.Errorf("ahead/behind = %d/%d, want 1/1", b.Ahead, b.Behind)
	}
	if b.Gone {
		t.Error("a live upstream must not read as gone")
	}
	if len(info.Branches) != 1 || info.Branches[0].SHA == "" || info.Branches[0].Subject != "third" {
		t.Errorf("branches = %+v, want one branch with sha and tip subject", info.Branches)
	}

	// Point the branch's upstream at a ref that does not exist on the
	// remote: [gone]. Gone is not "in sync" — the upstream is missing, and
	// claiming 0/0 as numbers would be a check nobody ran (§7.2). Git
	// stops reporting ahead/behind once the upstream is gone, which is
	// exactly why the gone flag is kept separate.
	gitOut(t, work, "", "config", "branch.main.merge", "refs/heads/deleted")
	info = refsWith(t, work, RefsOptions{})
	if b := info.CurrentBranch(); b == nil || !b.Gone || b.Behind != 0 {
		t.Errorf("gone branch = %+v, want gone with no behind claim", b)
	}

	// A repo with no remote at all has no upstream to claim.
	solo := initRepo(t)
	if b := refsWith(t, solo, RefsOptions{}).CurrentBranch(); b == nil || b.Upstream != "" || b.Gone || b.Ahead != 0 {
		t.Errorf("solo branch = %+v, want no upstream, not gone, ahead 0", b)
	}
}

func TestRefsNewestTagVsDescribedTag(t *testing.T) {
	// Git-flow: v1.0.0 cut on main, work continues on develop where v2.0.0
	// and a non-release tag land. Newest-by-date and nearest-reachable are
	// different tags here, and both facts must survive (§7.3).
	root := twoCommitRepo(t)
	gitOut(t, root, "2026-01-03T00:00:00", "tag", "-a", "v1.0.0", "-m", "v1.0.0")
	commitFile(t, root, "file3", "third", "2026-01-04T00:00:00")

	gitOut(t, root, "", "checkout", "-q", "-b", "develop")
	commitFile(t, root, "file4", "fourth", "2026-02-01T00:00:00")
	gitOut(t, root, "2026-02-02T00:00:00", "tag", "-a", "v2.0.0", "-m", "v2.0.0")
	commitFile(t, root, "file5", "fifth", "2026-03-01T00:00:00")
	gitOut(t, root, "2026-03-01T00:00:00", "tag", "wip") // lightweight, no digits: not a release

	gitOut(t, root, "", "checkout", "-q", "main")

	info := refsWith(t, root, RefsOptions{TagPattern: "*[0-9]*"})
	if info.NewestTag == nil || info.NewestTag.Name != "v2.0.0" {
		t.Errorf("newest tag = %+v, want v2.0.0 (wip must fail the release glob)", info.NewestTag)
	}
	if info.DescribedTag == nil || info.DescribedTag.Name != "v1.0.0" {
		t.Errorf("described tag = %+v, want v1.0.0 (nearest reachable from main)", info.DescribedTag)
	}
	if info.TagsOrphaned {
		t.Error("v2.0.0 sits on develop; nothing is orphaned here")
	}
	if !info.TagOffBranch() {
		t.Error("newest tag on develop while HEAD is on main: off-branch")
	}
	if info.CommitsSinceTag != 1 || len(info.SinceTagSubjects) != 1 || info.SinceTagSubjects[0] != "third" {
		t.Errorf("since tag = %d %v, want 1 [third]", info.CommitsSinceTag, info.SinceTagSubjects)
	}

	// An empty pattern matches everything: the lightweight wip, cut latest,
	// wins the newest-tag slot.
	all := refsWith(t, root, RefsOptions{})
	if all.NewestTag == nil || all.NewestTag.Name != "wip" {
		t.Errorf("newest tag with no glob = %+v, want wip", all.NewestTag)
	}

	// Tag dates come from creatordate, verbatim.
	if want := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC); info.DescribedTag != nil && !info.DescribedTag.At.Equal(want) {
		t.Errorf("described tag date = %v, want %v", info.DescribedTag.At, want)
	}
}

func TestRefsCommitsSinceTagExcludesMerges(t *testing.T) {
	// Two feature commits plus their --no-ff merge commit: the merge is
	// history, the two are work. Since-tag count is 2, and the merge's
	// subject must not leak into the subjects list (§7.3).
	root := twoCommitRepo(t)
	gitOut(t, root, "2026-01-03T00:00:00", "tag", "-a", "v1.0.0", "-m", "rel1")
	gitOut(t, root, "", "checkout", "-q", "-b", "feat", "HEAD~1")
	commitFile(t, root, "feat1", "feat one", "2026-01-04T00:00:00")
	commitFile(t, root, "feat2", "feat two", "2026-01-05T00:00:00")
	gitOut(t, root, "", "checkout", "-q", "main")
	gitOut(t, root, "", "merge", "--no-ff", "-q", "-m", "Merge branch 'feat'", "feat")

	info := refsWith(t, root, RefsOptions{})
	if info.CommitsSinceTag != 2 {
		t.Errorf("commits since tag = %d, want 2 (merge excluded)", info.CommitsSinceTag)
	}
	if len(info.SinceTagSubjects) != 2 {
		t.Fatalf("subjects = %v, want the two feature subjects", info.SinceTagSubjects)
	}
	if info.SinceTagSubjects[0] != "feat two" || info.SinceTagSubjects[1] != "feat one" {
		t.Errorf("subjects = %v, want newest first", info.SinceTagSubjects)
	}
	for _, s := range info.SinceTagSubjects {
		if strings.Contains(s, "Merge") {
			t.Errorf("merge subject %q leaked into since-tag subjects", s)
		}
	}

	// A lone merge commit whose tree did not move counts as nothing. The
	// merge is fabricated with commit-tree so its second parent stays an
	// ancestor of the tag — the one way a merge can be the sole commit
	// since a tag without carrying feature commits with it.
	noop := twoCommitRepo(t)
	gitOut(t, noop, "2026-01-03T00:00:00", "tag", "-a", "v1.0.0", "-m", "rel1")
	tree := gitOut(t, noop, "", "rev-parse", "HEAD^{tree}")
	parent := gitOut(t, noop, "", "rev-parse", "HEAD")
	second := gitOut(t, noop, "", "rev-parse", "HEAD~1")
	m := gitOut(t, noop, "", "commit-tree", tree, "-p", parent, "-p", second, "-m", "noop merge")
	gitOut(t, noop, "", "reset", "-q", "--hard", m)
	if got := refsWith(t, noop, RefsOptions{}).CommitsSinceTag; got != 0 {
		t.Errorf("sole no-op merge counted as %d, want 0", got)
	}

	// The mirror case: a lone merge that DOES change the tree is one
	// commit's worth of work, not zero — that is the §7.3 exception.
	tweak := twoCommitRepo(t)
	gitOut(t, tweak, "2026-01-03T00:00:00", "tag", "-a", "v1.0.0", "-m", "rel1")
	writeFile(t, tweak, "tweak.txt", "changed\n")
	gitOut(t, tweak, "", "add", "tweak.txt")
	tree2 := gitOut(t, tweak, "", "write-tree")
	parent = gitOut(t, tweak, "", "rev-parse", "HEAD")
	second = gitOut(t, tweak, "", "rev-parse", "HEAD~1")
	m = gitOut(t, tweak, "", "commit-tree", tree2, "-p", parent, "-p", second, "-m", "merge with tweak")
	gitOut(t, tweak, "", "reset", "-q", "--hard", m)
	if got := refsWith(t, tweak, RefsOptions{}).CommitsSinceTag; got != 1 {
		t.Errorf("sole tree-changing merge counted as %d, want 1", got)
	}
}

func TestRefsSubjectsCap(t *testing.T) {
	root := twoCommitRepo(t)
	gitOut(t, root, "2026-01-03T00:00:00", "tag", "-a", "v1.0.0", "-m", "rel1")
	for i := 1; i <= 5; i++ {
		digit := string(rune('0' + i))
		commitFile(t, root, "f"+digit, "f"+digit, "2026-01-1"+digit+"T00:00:00")
	}

	info := refsWith(t, root, RefsOptions{MaxSubjects: 3})
	if len(info.SinceTagSubjects) != 3 {
		t.Fatalf("subjects = %v, want capped at 3", info.SinceTagSubjects)
	}
	if info.SinceTagSubjects[0] != "f5" {
		t.Errorf("subjects[0] = %q, want f5 (newest first)", info.SinceTagSubjects[0])
	}

	// Zero means the default cap, not unbounded.
	if got := subjectsCap(0); got != 30 {
		t.Errorf("subjectsCap(0) = %d, want 30", got)
	}
	all := refsWith(t, root, RefsOptions{})
	if len(all.SinceTagSubjects) != 5 {
		t.Errorf("subjects with default cap = %v, want all 5", all.SinceTagSubjects)
	}
}

func TestRefsOrphanedTag(t *testing.T) {
	// History rewrite residue: the tag points at commits no branch reaches
	// any more. Reported as orphaned — never silently read as a release —
	// and an orphaned tag is not off-branch, since it is not a release of
	// anything checked out (§7.4).
	root := initRepo(t)
	gitOut(t, root, "2026-01-02T00:00:00", "tag", "-a", "v1.0.0", "-m", "rel1")
	gitOut(t, root, "", "checkout", "-q", "--orphan", "scratch")
	gitOut(t, root, "", "commit", "-q", "-m", "orphaned line of history")
	gitOut(t, root, "", "branch", "-q", "-D", "main")

	info := refsWith(t, root, RefsOptions{})
	if !info.TagsOrphaned {
		t.Error("tag unreachable from any branch must read as orphaned")
	}
	if info.TagOffBranch() {
		t.Error("an orphaned tag is not off-branch")
	}
	if info.NewestTag == nil || info.NewestTag.Name != "v1.0.0" {
		t.Errorf("newest tag = %+v, want v1.0.0", info.NewestTag)
	}
	if info.DescribedTag != nil {
		t.Errorf("described tag = %+v, want nil: HEAD carries none of the tag's history", info.DescribedTag)
	}
	if info.CommitsSinceTag != 0 {
		t.Errorf("commits since tag = %d, want 0 with no reachable tag", info.CommitsSinceTag)
	}
}

func TestRefsStashCount(t *testing.T) {
	root := initRepo(t)
	if got := refsWith(t, root, RefsOptions{}).Stashes; got != 0 {
		t.Errorf("stashes = %d, want 0 for a repo that never stashed", got)
	}
	writeFile(t, root, "file1", "one\n")
	gitOut(t, root, "", "stash", "-q")
	writeFile(t, root, "file1", "two\n")
	gitOut(t, root, "", "stash", "-q")
	if got := refsWith(t, root, RefsOptions{}).Stashes; got != 2 {
		t.Errorf("stashes = %d, want 2, counted from logs/refs/stash with no process (§7.8)", got)
	}
}

func TestRefsOperations(t *testing.T) {
	markers := []struct {
		path  string
		isDir bool
		want  Operation
		label string
	}{
		{"MERGE_HEAD", false, OpMerge, "merging"},
		{"rebase-merge", true, OpRebase, "rebasing"},
		{"rebase-apply", true, OpRebase, "rebasing"},
		{"CHERRY_PICK_HEAD", false, OpCherryPick, "cherry-picking"},
		{"REVERT_HEAD", false, OpRevert, "reverting"},
		{"BISECT_LOG", false, OpBisect, "bisecting"},
	}
	for _, m := range markers {
		root := initRepo(t)
		p := filepath.Join(root, ".git", m.path)
		if m.isDir {
			if err := os.Mkdir(p, 0o755); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		info := refsWith(t, root, RefsOptions{})
		if info.Operation != m.want || info.Operation.Label() != m.label {
			t.Errorf("marker %s: operation = %q (%q), want %q (%q)",
				m.path, info.Operation, info.Operation.Label(), m.want, m.label)
		}
	}

	// No markers: the empty operation, not a string of spaces or a word.
	if got := refsWith(t, initRepo(t), RefsOptions{}).Operation; got != "" {
		t.Errorf("clean repo operation = %q, want empty", got)
	}
}

func TestRefsDiskFacts(t *testing.T) {
	origin := t.TempDir()
	gitOut(t, origin, "", "init", "--bare", "-q", "-b", "main")
	seed := t.TempDir()
	gitOut(t, seed, "", "init", "-q", "-b", "main")
	commitFile(t, seed, "file1", "first", "2026-01-01T00:00:00")
	gitOut(t, seed, "", "push", "-q", "file://"+origin, "main")

	root := initRepo(t)
	info := refsWith(t, root, RefsOptions{})
	if info.IndexMtime.IsZero() {
		t.Error("index mtime = zero after a commit; the cache key depends on it")
	}
	if !info.FetchedAt.IsZero() {
		t.Error("a repo that never fetched must show the zero time — 'never', not 'in sync' (§7.2)")
	}
	if info.IsBare || info.IsShallow {
		t.Error("a normal checkout is neither bare nor shallow")
	}
	if info.LastCommit == nil || info.LastCommit.Subject != "first" || info.LastCommit.Author != "t" {
		t.Errorf("last commit = %+v, want subject first by author t", info.LastCommit)
	}

	// Remote URL comes off .git/config: origin wins; without origin, the
	// first remote defined does.
	gitOut(t, root, "", "remote", "add", "upstream", "/first/remote")
	if got := refsWith(t, root, RefsOptions{}).RemoteURL; got != "/first/remote" {
		t.Errorf("remote url = %q, want the first remote", got)
	}
	gitOut(t, root, "", "remote", "add", "origin", "/origin/remote")
	if got := refsWith(t, root, RefsOptions{}).RemoteURL; got != "/origin/remote" {
		t.Errorf("remote url = %q, want origin's", got)
	}

	// A fetch is what creates FETCH_HEAD; until then the zero time stands.
	clone := t.TempDir()
	gitOut(t, clone, "", "clone", "-q", "file://"+origin, ".")
	gitOut(t, clone, "", "fetch", "-q", "origin")
	if got := refsWith(t, clone, RefsOptions{}).FetchedAt; got.IsZero() {
		t.Error("a fetched repo has a FETCH_HEAD; fetched_at must not be zero")
	}
	// Shallow is the existence of .git/shallow — cut off history reads as
	// such rather than probing the full graph.
	shallow := initRepo(t)
	writeFile(t, shallow, filepath.Join(".git", "shallow"), "")
	if !refsWith(t, shallow, RefsOptions{}).IsShallow {
		t.Error(".git/shallow present must read as shallow")
	}

	// A bare repo probes: bare flag on, HEAD unborn, branches empty, and
	// no working tree to mistake it for (§7.7).
	bare := t.TempDir()
	gitOut(t, bare, "", "init", "--bare", "-q", "-b", "main")
	bi := refsWith(t, bare, RefsOptions{})
	if !bi.IsBare || len(bi.Branches) != 0 || bi.Stashes != 0 {
		t.Errorf("bare info = %+v, want bare, no branches, real zero stashes", bi)
	}
}

func TestRefsNotARepo(t *testing.T) {
	// A directory that is not a checkout is an error value, not a row of
	// zeros and not a panic — the caller decides what to render.
	info, err := Refs(t.TempDir(), RefsOptions{})
	if err == nil {
		t.Error("Refs on a non-repo must fail")
	}
	if info.LastCommit != nil || len(info.Branches) != 0 {
		t.Errorf("error path returned %+v, want no probe results", info)
	}
}

func TestRails(t *testing.T) {
	root := initRepo(t)

	// A failing command is an error carrying stderr, never a panic.
	if _, err := RunGit(root, time.Second, "rev-parse", "--verify", "definitely-not-a-ref"); err == nil {
		t.Error("RunGit on a failing command must error")
	}
	if out, ok := RunGitOK(root, time.Second, "rev-parse", "--verify", "definitely-not-a-ref"); ok || out != "" {
		t.Errorf("RunGitOK on failure = (%q, %v), want empty, false", out, ok)
	}

	// The expected non-zero exit reads as a false, not a crash.
	if _, ok := RunGitOK(root, time.Second, "diff", "--quiet", "HEAD", "HEAD~1"); ok {
		t.Error("diff --quiet reporting a change must come back as false")
	}

	// Success returns stdout raw — callers trim, the rails do not guess.
	head := gitOut(t, root, "", "rev-parse", "HEAD")
	if out, err := RunGit(root, time.Second, "rev-parse", "HEAD"); err != nil || out != head+"\n" {
		t.Errorf("RunGit rev-parse = (%q, %v), want %q", out, err, head+"\n")
	}

	// A wedged child dies at the timeout with a named error instead of
	// stalling the probe — the whole reason every call carries a ceiling.
	if _, err := run(root, 150*time.Millisecond, "sleep", "2"); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("slow child error = %v, want a timeout", err)
	}
}

func TestChangelogHeadingShapes(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		tags    []string
		version string
		tagged  bool
		blocks  int
	}{
		{"plain", "# 1.2.3\n\n- fix\n", []string{"1.2.3"}, "1.2.3", true, 0},
		{"bracketed", "## [1.2.3] - 2026-01-01\n\n- fix\n", []string{"1.2.3"}, "1.2.3", true, 0},
		{"v-prefixed with date", "# v1.2.3 - 2026-01-01\n", []string{"v1.2.3"}, "v1.2.3", true, 0},
		{"v normalization", "# v1.2.3\n", []string{"1.2.3"}, "v1.2.3", true, 0},
		{"semver suffix", "## [1.2.3-beta.1] - date\n", []string{"v1.2.3-beta.1"}, "1.2.3-beta.1", true, 0},
		// Nothing in the file is tagged, so the lone version heading is an
		// unreleased block by the same reading that counts any other.
		{"untagged", "# 1.2.3\n", nil, "1.2.3", false, 1},
		{"unreleased heading is not a version", "## Unreleased\n\n## 1.0.0\n", []string{"v1.0.0"}, "1.0.0", true, 0},
		// 1.1.0 sits above 1.0.0 but is itself tagged: released versions are
		// never unreleased blocks, wherever they sit.
		{"one block above last tagged", "# 1.2.0\n# 1.1.0\n# 1.0.0\n", []string{"v1.1.0", "v1.0.0"}, "1.2.0", false, 1},
		{"blocks counted distinctly", "# 1.3.0\n# 1.3.0\n# 1.2.0\n# 1.1.0\n", []string{"v1.1.0"}, "1.3.0", false, 2},
		{"prose only", "# Changelog\n\nAll the things.\n", nil, "", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseChangelog(tc.text, tc.tags)
			if got.Version != tc.version || got.Tagged != tc.tagged || got.UnreleasedBlocks != tc.blocks {
				t.Errorf("parseChangelog = %+v, want version %q tagged %v blocks %d",
					got, tc.version, tc.tagged, tc.blocks)
			}
		})
	}
}

func TestReadChangelogFileSelection(t *testing.T) {
	// The first configured file that exists wins; none existing answers
	// nil rather than an error — a repo without a changelog is normal.
	root := t.TempDir()
	if got := ReadChangelog(root, []string{"CHANGELOG.md", "CHANGELOG"}, nil); got != nil {
		t.Errorf("no changelog present = %+v, want nil", got)
	}
	writeFile(t, root, "CHANGELOG", "# 0.9.0\n")
	got := ReadChangelog(root, []string{"CHANGELOG.md", "CHANGELOG"}, []string{"v0.9.0"})
	if got == nil || got.Version != "0.9.0" || !got.Tagged {
		t.Errorf("fallback file = %+v, want 0.9.0 tagged", got)
	}
	writeFile(t, root, "CHANGELOG.md", "# 1.0.0\n")
	got = ReadChangelog(root, []string{"CHANGELOG.md", "CHANGELOG"}, []string{"v0.9.0"})
	if got == nil || got.Version != "1.0.0" {
		t.Errorf("first file = %+v, want 1.0.0 from CHANGELOG.md", got)
	}
}

func TestRefsChangelogIntegration(t *testing.T) {
	// Tier 1 wires the changelog against the repo's real tags: a top
	// version no tag records is an in-flight release waiting to be cut,
	// and the block above the last tagged one counts as unreleased.
	root := initRepo(t)
	gitOut(t, root, "2026-01-02T00:00:00", "tag", "-a", "v1.0.0", "-m", "rel1")
	writeFile(t, root, "CHANGELOG.md", "# 1.1.0\n\n- pending\n\n# 1.0.0\n\n- shipped\n")

	info := refsWith(t, root, RefsOptions{ReadChangelog: true})
	if info.Changelog == nil {
		t.Fatal("changelog not read despite ReadChangelog")
	}
	if info.Changelog.Version != "1.1.0" || info.Changelog.Tagged {
		t.Errorf("changelog = %+v, want 1.1.0 untagged", info.Changelog)
	}
	if info.Changelog.UnreleasedBlocks != 1 {
		t.Errorf("unreleased blocks = %d, want 1", info.Changelog.UnreleasedBlocks)
	}

	// Off by default: a probe asked not to read changelogs reads none.
	if got := refsWith(t, root, RefsOptions{}).Changelog; got != nil {
		t.Errorf("changelog = %+v, want nil without ReadChangelog", got)
	}
}
