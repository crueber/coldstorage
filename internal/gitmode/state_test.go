package gitmode

import (
	"testing"
	"time"
)

// A probe error is the zeroth precedence step (spec §8), but it lives in the
// data model's own error field — RefsInfo and WorkInfo survive a failed
// probe with their last good values — so StateLabel receives only rows whose
// error step has already been taken. These tests pin everything the function
// itself can see.

func TestStateLabelConflictBeatsDirty(t *testing.T) {
	w := &WorkInfo{Conflicts: 2, Staged: 1, Unstaged: 3}
	if got := StateLabel(RefsInfo{}, w); got != "conflict" {
		t.Errorf("StateLabel = %q, want conflict: conflicts are the one state that blocks everything", got)
	}
}

func TestStateLabelConflictBeatsInProgress(t *testing.T) {
	w := &WorkInfo{Conflicts: 1}
	r := RefsInfo{Operation: OpRebase}
	if got := StateLabel(r, w); got != "conflict" {
		t.Errorf("StateLabel = %q, want conflict even mid-rebase", got)
	}
}

func TestStateLabelInProgressLabel(t *testing.T) {
	for _, op := range []Operation{OpMerge, OpRebase, OpCherryPick, OpRevert} {
		w := &WorkInfo{Unstaged: 1}
		got := StateLabel(RefsInfo{Operation: op}, w)
		if want := op.Label(); got != want {
			t.Errorf("StateLabel(OpMerge-family) = %q, want %q", got, want)
		}
	}
}

func TestStateLabelInProgressBeatsDirty(t *testing.T) {
	w := &WorkInfo{Unstaged: 5}
	if got := StateLabel(RefsInfo{Operation: OpCherryPick}, w); got != "cherry-picking" {
		t.Errorf("StateLabel = %q, want cherry-picking: what's half-done outranks what's unfinished", got)
	}
}

func TestStateLabelDirtyBeatsUnpushed(t *testing.T) {
	w := &WorkInfo{Unstaged: 2}
	r := RefsInfo{Branches: []BranchInfo{{Name: "main", Ahead: 3}}}
	if got := StateLabel(r, w); got != "dirty" {
		t.Errorf("StateLabel = %q, want dirty", got)
	}
}

func TestStateLabelUnpushedBeatsBare(t *testing.T) {
	// Spec §8 note: a bare repo holding branches its remote hasn't seen says
	// so — bare is the quieter, less useful truth.
	r := RefsInfo{IsBare: true, Branches: []BranchInfo{{Name: "main", Ahead: 4}}}
	if got := StateLabel(r, nil); got != "unpushed" {
		t.Errorf("StateLabel = %q, want unpushed", got)
	}
}

func TestStateLabelUnpushedBeatsNeverScanned(t *testing.T) {
	// Tier 1 knows about unpushed work; tier 2 may not have run yet. The
	// unpushed verdict is real information and outranks the ellipsis.
	r := RefsInfo{Branches: []BranchInfo{{Name: "main", Ahead: 1}}}
	if got := StateLabel(r, nil); got != "unpushed" {
		t.Errorf("StateLabel = %q, want unpushed", got)
	}
}

func TestStateLabelBare(t *testing.T) {
	if got := StateLabel(RefsInfo{IsBare: true}, nil); got != "bare" {
		t.Errorf("StateLabel = %q, want bare", got)
	}
}

func TestStateLabelNeverScanned(t *testing.T) {
	// nil WorkInfo is "tier 2 hasn't run", not "clean" — the ellipsis is
	// honest about not knowing, which is why bare is checked first: a bare
	// repo never runs tier 2 and would be stuck on the ellipsis forever.
	if got := StateLabel(RefsInfo{}, nil); got != "…" {
		t.Errorf("StateLabel = %q, want the never-scanned ellipsis", got)
	}
}

func TestStateLabelClean(t *testing.T) {
	w := &WorkInfo{}
	if got := StateLabel(RefsInfo{}, w); got != "clean" {
		t.Errorf("StateLabel = %q, want clean", got)
	}
}

func TestStateLabelEmptyScanIsCleanNotNeverScanned(t *testing.T) {
	// A scanned-and-empty repo differs from a never-scanned one only by the
	// pointer being non-nil — the distinction the ellipsis exists to draw.
	w := &WorkInfo{Files: []ChangedFile{}}
	if got := StateLabel(RefsInfo{}, w); got != "clean" {
		t.Errorf("StateLabel = %q, want clean for a completed empty scan", got)
	}
}

func TestStateLabelDetachedIsNotAState(t *testing.T) {
	// Detached HEAD shows in its own column; the state column has no word
	// for it, and a clean detached checkout reads as clean.
	r := RefsInfo{Head: Head{Kind: HeadDetached, SHA: "abc123"}}
	w := &WorkInfo{}
	if got := StateLabel(r, w); got != "clean" {
		t.Errorf("StateLabel = %q, want clean", got)
	}
}

func TestReleaseStateOf(t *testing.T) {
	tag := &TagInfo{Name: "v1.2.3", At: time.Unix(1700000000, 0)}

	// No tags at all: unreleased, whatever else is true.
	if got := ReleaseStateOf(RefsInfo{}, &WorkInfo{}); got != ReleaseUnreleased {
		t.Errorf("ReleaseStateOf = %q, want unreleased", got)
	}
	if got := ReleaseStateOf(RefsInfo{Branches: []BranchInfo{{Ahead: 2}}}, nil); got != ReleaseUnreleased {
		t.Errorf("ReleaseStateOf = %q, want unreleased even with unpushed work", got)
	}

	// Tagged, nothing since: released.
	released := RefsInfo{NewestTag: tag, DescribedTag: tag}
	if got := ReleaseStateOf(released, &WorkInfo{}); got != ReleaseReleased {
		t.Errorf("ReleaseStateOf = %q, want released", got)
	}

	// Tagged, commits since: needs-release.
	since := RefsInfo{NewestTag: tag, DescribedTag: tag, CommitsSinceTag: 2}
	if got := ReleaseStateOf(since, &WorkInfo{}); got != ReleaseNeedsRelease {
		t.Errorf("ReleaseStateOf = %q, want needs-release from commits since the tag", got)
	}

	// Tagged, no commits since, but uncommitted work: needs-release — the
	// tree has moved on from the tag whether or not it has been committed.
	dirty := &WorkInfo{Untracked: 1}
	if got := ReleaseStateOf(released, dirty); got != ReleaseNeedsRelease {
		t.Errorf("ReleaseStateOf = %q, want needs-release from dirty work", got)
	}

	// Dirty work before any tag exists is still just unreleased.
	if got := ReleaseStateOf(RefsInfo{}, dirty); got != ReleaseUnreleased {
		t.Errorf("ReleaseStateOf = %q, want unreleased", got)
	}
}
