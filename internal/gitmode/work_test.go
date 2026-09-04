package gitmode

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixtureRepoStatus is a porcelain v2 capture with one of everything:
// branch headers (parsed and discarded), a staged file, an unstaged file, a
// file that is both, a rename, an untracked file, and an unmerged pair.
var fixtureRepoStatus = strings.Join([]string{
	"# branch.oid 7c9f3c1a2b",
	"# branch.head develop",
	"# branch.upstream origin/develop",
	"# branch.ab +0 -0",
	"1 M. N... 100644 100644 100644 h1 h2 staged.rs",
	"1 .M N... 100644 100644 100644 h1 h2 unstaged.rs",
	"1 MM N... 100644 100644 100644 h1 h2 both.rs",
	// Rename records separate the new path from the original with a tab.
	"2 R. N... 100644 100644 100644 h1 h2 R100 renamed.rs\told-name.rs",
	"? untracked.rs",
	"u UU N... 100644 100644 100644 100644 h1 h2 h3 conflicted.rs",
}, "\n")

func TestParseStatusV2Counts(t *testing.T) {
	info := parseStatusV2(fixtureRepoStatus, t.TempDir(), 0)

	if info.Staged != 3 { // M., R., and MM
		t.Errorf("Staged = %d, want 3", info.Staged)
	}
	if info.Unstaged != 2 { // .M and MM
		t.Errorf("Unstaged = %d, want 2", info.Unstaged)
	}
	if info.Untracked != 1 {
		t.Errorf("Untracked = %d, want 1", info.Untracked)
	}
	if info.Conflicts != 1 {
		t.Errorf("Conflicts = %d, want 1", info.Conflicts)
	}
	if info.Truncated {
		t.Error("Truncated = true, want false with no cap")
	}
	if info.Total() != 7 {
		t.Errorf("Total = %d, want 7", info.Total())
	}
}

func TestParseStatusV2Files(t *testing.T) {
	info := parseStatusV2(fixtureRepoStatus, t.TempDir(), 0)

	want := []ChangedFile{
		{Path: "staged.rs", Code: "M.", Kind: ChangeStaged},
		{Path: "unstaged.rs", Code: ".M", Kind: ChangeUnstaged},
		// A record can be both staged and unstaged; the kind follows the
		// worktree side, whose mtime the file carries.
		{Path: "both.rs", Code: "MM", Kind: ChangeUnstaged},
		{Path: "renamed.rs", Code: "R.", Kind: ChangeStaged},
		{Path: "untracked.rs", Code: "??", Kind: ChangeUntracked},
		{Path: "conflicted.rs", Code: "UU", Kind: ChangeConflicted},
	}
	if len(info.Files) != len(want) {
		t.Fatalf("got %d files, want %d: %+v", len(info.Files), len(want), info.Files)
	}
	for i := range want {
		got := info.Files[i]
		if got.Path != want[i].Path || got.Code != want[i].Code || got.Kind != want[i].Kind {
			t.Errorf("Files[%d] = {Path: %q, Code: %q, Kind: %q}, want {Path: %q, Code: %q, Kind: %q}",
				i, got.Path, got.Code, got.Kind, want[i].Path, want[i].Code, want[i].Kind)
		}
	}
}

func TestParseStatusV2RenameTakesNewPath(t *testing.T) {
	info := parseStatusV2("2 R. N... 100644 100644 100644 h1 h2 R100 new/path.rs\told/path.rs\n", t.TempDir(), 0)
	if len(info.Files) != 1 || info.Files[0].Path != "new/path.rs" {
		t.Fatalf("rename path = %+v, want new/path.rs", info.Files)
	}
}

func TestParseStatusV2PathsContainSpaces(t *testing.T) {
	raw := "1 .M N... 100644 100644 100644 h1 h2 my notes.md\n? a b c.txt\n"
	info := parseStatusV2(raw, t.TempDir(), 0)
	if info.Files[0].Path != "my notes.md" || info.Files[1].Path != "a b c.txt" {
		t.Fatalf("paths = %q, %q; spaces are part of the path", info.Files[0].Path, info.Files[1].Path)
	}
}

func TestParseStatusV2IgnoresBranchHeaders(t *testing.T) {
	// --branch makes git emit ahead/behind headers it would revwalk for —
	// tier 1 owns those numbers — so headers must parse as noise, not files.
	info := parseStatusV2(fixtureRepoStatus, t.TempDir(), 0)
	for _, f := range info.Files {
		if len(f.Path) > 0 && f.Path[0] == '#' {
			t.Errorf("branch header leaked into files as %q", f.Path)
		}
	}
}

func TestParseStatusV2FileMtimes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "edit.rs"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	info := parseStatusV2("1 .M N... 100644 100644 100644 h1 h2 edit.rs\n1 .M N... 100644 100644 100644 h1 h2 gone.rs\n", root, 0)

	mt := info.Files[0].Mtime
	if mt.IsZero() {
		t.Fatal("existing file has no mtime: activity-from-file-edit would never fire")
	}
	fi, err := os.Stat(filepath.Join(root, "edit.rs"))
	if err != nil || !fi.ModTime().Equal(mt) {
		t.Errorf("mtime = %v, want the file's actual mtime", mt)
	}
	if !info.Files[1].Mtime.IsZero() {
		t.Error("deleted file should carry no mtime")
	}
	if !info.NewestMtime.Equal(mt) {
		t.Errorf("NewestMtime = %v, want %v", info.NewestMtime, mt)
	}
}

// gitCmd is the test-side git driver: no network, no user config, fixed
// identity, exactly like the rails the probes themselves use.
func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// plainRepo builds a checkout with one staged file, one unstaged file and
// one untracked file on top of two commits, and no conflict — so tests that
// keep mutating it (commits, restages) can.
func plainRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitCmd(t, root, "init", "-q", "-b", "main")
	writeFile(t, root, "tracked.txt", "base")
	gitCmd(t, root, "add", ".")
	gitCmd(t, root, "commit", "-q", "-m", "base")
	writeFile(t, root, "staged.txt", "hello")
	gitCmd(t, root, "add", ".")
	gitCmd(t, root, "commit", "-q", "-m", "second")
	// On top of the commits: a staged edit, an unstaged edit, an untracked
	// file — one change of each kind.
	writeFile(t, root, "staged.txt", "staged content")
	gitCmd(t, root, "add", "staged.txt")
	writeFile(t, root, "unstaged.txt", "edited")
	writeFile(t, root, "untracked.txt", "new")
	return root
}

// conflictRepo builds a real checkout with one staged file, two untracked
// files, and one unmerged pair from a diverged merge.
func conflictRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitCmd(t, root, "init", "-q", "-b", "main")
	writeFile(t, root, "base.txt", "base")
	gitCmd(t, root, "add", ".")
	gitCmd(t, root, "commit", "-q", "-m", "base")

	gitCmd(t, root, "checkout", "-q", "-b", "feature")
	writeFile(t, root, "clash.txt", "feature side")
	gitCmd(t, root, "add", ".")
	gitCmd(t, root, "commit", "-q", "-m", "feature side")

	gitCmd(t, root, "checkout", "-q", "main")
	writeFile(t, root, "clash.txt", "main side")
	gitCmd(t, root, "add", ".")
	gitCmd(t, root, "commit", "-q", "-m", "main side")

	// The merge fails loudly and leaves the conflict in place — that is the
	// state being probed, so the error is expected, not a test failure.
	cmd := exec.Command("git", "merge", "feature", "--no-edit")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	if _, err := cmd.CombinedOutput(); err == nil {
		t.Fatal("merge unexpectedly succeeded; fixture no longer contains a conflict")
	}

	// Around the conflict, one staged file and two untracked ones. Nothing
	// commits while the conflict is open — git refuses — so staged work is
	// made by adding, untracked by writing.
	writeFile(t, root, "staged.txt", "staged content")
	gitCmd(t, root, "add", "staged.txt")
	writeFile(t, root, "edited.txt", "modified in the working tree")
	writeFile(t, root, "untracked.txt", "new")
	return root
}

func TestWorkRealRepo(t *testing.T) {
	root := conflictRepo(t)

	info, key, err := Work(root, WorkOptions{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	if info.Conflicts != 1 {
		t.Errorf("Conflicts = %d, want 1 (the diverged merge)", info.Conflicts)
	}
	if info.Staged != 1 {
		t.Errorf("Staged = %d, want 1", info.Staged)
	}
	if info.Untracked != 2 {
		t.Errorf("Untracked = %d, want 2", info.Untracked)
	}
	if len(info.Files) != 4 {
		t.Fatalf("Files = %+v, want 4 entries", info.Files)
	}
	if info.Files[0].Mtime.IsZero() {
		t.Error("changed file carries no mtime; activity-from-file-edit would be broken")
	}
	if key.HeadSHA == "" {
		t.Error("WorkKey.HeadSHA empty: the key could never distinguish commits")
	}
	if !key.HasIndex || key.IndexSize == 0 {
		t.Errorf("WorkKey = %+v, want a real index mtime and size", key)
	}
}

func TestWorkUntrackedNoMode(t *testing.T) {
	root := plainRepo(t)

	info, _, err := Work(root, WorkOptions{Untracked: UntrackedNo})
	if err != nil {
		t.Fatal(err)
	}
	if info.Untracked != 0 {
		t.Errorf("Untracked = %d, want 0: mode `no` means no enumeration at all", info.Untracked)
	}
	for _, f := range info.Files {
		if f.Kind == ChangeUntracked {
			t.Errorf("untracked file %q listed under mode `no`", f.Path)
		}
	}
}

func TestWorkCapAndTruncation(t *testing.T) {
	root := plainRepo(t)

	info, _, err := Work(root, WorkOptions{MaxFiles: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Files) != 2 {
		t.Errorf("len(Files) = %d, want 2 (the cap)", len(info.Files))
	}
	if !info.Truncated {
		t.Error("Truncated = false, want true: three changes exceed the cap")
	}
	// Counts stay exact across the cap — the CHANGES column would lie
	// otherwise, and the scan still knows how much work is outstanding.
	if info.Total() != 3 {
		t.Errorf("Total = %d, want 3 despite the cap", info.Total())
	}
}

func TestWorkKeyMatchesResolution(t *testing.T) {
	root := plainRepo(t)
	_, key, err := Work(root, WorkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if key.HeadSHA != string(out[:len(out)-1]) { // strip the newline
		t.Errorf("HeadSHA = %q, want %q (git's own resolution)", key.HeadSHA, out)
	}
}

func TestWorkFresh(t *testing.T) {
	root := plainRepo(t)
	_, key, err := Work(root, WorkOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if !WorkFresh(key, root) {
		t.Error("fresh scan reported stale before anything moved")
	}

	// A working-tree edit moves neither HEAD nor the index: the cache holds.
	// (The watcher, not tier 2, is what notices working-tree edits.)
	writeFile(t, root, "unstaged.txt", "edited again")
	if !WorkFresh(key, root) {
		t.Error("unstaged edit invalidated the key; working-tree noise would re-scan the fleet")
	}

	// A commit moves both HEAD and the index: stale.
	writeFile(t, root, "unstaged.txt", "commit me")
	gitCmd(t, root, "add", "unstaged.txt")
	gitCmd(t, root, "commit", "-q", "-m", "move HEAD")
	if WorkFresh(key, root) {
		t.Error("key survived a commit; a staged scan would be served stale")
	}
}

func TestWorkFreshIndexMtime(t *testing.T) {
	root := plainRepo(t)
	_, key, err := Work(root, WorkOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Restaging without a commit moves the index, not HEAD — the key must
	// notice the index half on its own.
	writeFile(t, root, "staged2.txt", "stage me later")
	gitCmd(t, root, "add", "staged2.txt")
	if WorkFresh(key, root) {
		t.Error("key survived an index change; restaged work would be served stale")
	}
}

func TestWorkExpired(t *testing.T) {
	if WorkExpired(2*time.Hour, time.Hour) != true {
		t.Error("an age past max_age must read as expired")
	}
	if WorkExpired(time.Hour, 2*time.Hour) != false {
		t.Error("a fresh age must not read as expired")
	}
	if WorkExpired(time.Hour, 0) != false {
		t.Error("max_age unset must disable the backstop, not expire everything")
	}
}
