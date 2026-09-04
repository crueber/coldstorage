// Pure-function tests for the table's verdicts (spec §12): filters, any/all
// matching, age presets, fuzzy search, sorting, and the §10 cell grammar.
package tui

import (
	"errors"
	"testing"
	"time"

	"github.com/crueber/coldstorage/internal/gitmode"
)

func row(root string, mutate func(*RepoState)) RepoState {
	r := RepoState{Root: root, Group: "grp", Name: root, Refs: gitmode.RefsInfo{}}
	r.Refs.FetchedAt = time.Now().Add(-time.Hour) // not "never fetched" by default
	if mutate != nil {
		mutate(&r)
	}
	return r
}

func TestMatchesKind(t *testing.T) {
	dirty := row("dirty", func(r *RepoState) {
		r.Work = &gitmode.WorkInfo{Unstaged: 2}
	})
	unpushed := row("unpushed", func(r *RepoState) {
		r.Refs.Branches = []gitmode.BranchInfo{{Name: "main", Ahead: 3}}
	})
	clean := row("clean", func(r *RepoState) { r.Work = &gitmode.WorkInfo{} })
	conflict := row("conflict", func(r *RepoState) {
		r.Work = &gitmode.WorkInfo{Conflicts: 1}
	})
	errored := row("errored", func(r *RepoState) { r.Err = errors.New("boom") })
	neverFetched := row("nf", nil)
	neverFetched.Refs.FetchedAt = time.Time{}
	inProgress := row("ip", func(r *RepoState) { r.Refs.Operation = gitmode.OpMerge })
	needsRelease := row("nr", func(r *RepoState) {
		r.Refs.NewestTag = &gitmode.TagInfo{Name: "v1.0.0"}
		r.Refs.CommitsSinceTag = 2
	})

	cases := []struct {
		r    RepoState
		kind string
		want bool
	}{
		{dirty, "dirty", true},
		{dirty, "clean", false},
		{unpushed, "unpushed", true},
		{conflict, "conflict", true},
		{conflict, "dirty", true}, // conflicts are also dirty (Work.Dirty)
		{clean, "clean", true},
		{errored, "errored", true},
		{errored, "clean", false},
		{neverFetched, "never-fetched", true},
		{inProgress, "in-progress", true},
		{needsRelease, "needs-release", true},
		{needsRelease, "released", false},
	}
	for _, tc := range cases {
		if got := matchesKind(tc.r, tc.kind); got != tc.want {
			t.Errorf("matchesKind(%s, %s) = %v, want %v", tc.r.Name, tc.kind, got, tc.want)
		}
	}
}

func TestFiltersAnyVsAll(t *testing.T) {
	dirtyUnpushed := row("x", func(r *RepoState) {
		r.Work = &gitmode.WorkInfo{Unstaged: 1}
		r.Refs.Branches = []gitmode.BranchInfo{{Name: "main", Ahead: 1}}
	})
	dirtyOnly := row("y", func(r *RepoState) { r.Work = &gitmode.WorkInfo{Staged: 1} })

	any := filters{kinds: map[string]bool{"dirty": true, "behind": true}}
	if !any.match(dirtyUnpushed) || !any.match(dirtyOnly) {
		t.Error("any-match should admit both rows")
	}
	all := filters{kinds: map[string]bool{"dirty": true, "behind": true}, matchAll: true}
	if all.match(dirtyUnpushed) || all.match(dirtyOnly) {
		t.Error("all-match should admit neither row (neither is behind)")
	}
}

func TestFiltersGroupSearchAge(t *testing.T) {
	r := row("demo", nil)

	if (filters{group: "other"}).match(r) {
		t.Error("group filter should exclude other groups")
	}
	if !(filters{group: "grp"}).match(r) {
		t.Error("group filter should admit the row's group")
	}
	if (filters{search: "zzz"}).match(r) {
		t.Error("search zzz should not match demo")
	}
	if !(filters{search: "dm"}).match(r) {
		t.Error("fuzzy dm should match demo")
	}

	old := row("old", func(r *RepoState) {
		r.Refs.Branches = []gitmode.BranchInfo{{Name: "main", CommittedAt: time.Now().Add(-48 * time.Hour)}}
	})
	recent := row("new", func(r *RepoState) {
		r.Refs.Branches = []gitmode.BranchInfo{{Name: "main", CommittedAt: time.Now().Add(-time.Hour)}}
	})
	since := time.Now().Add(-24 * time.Hour)
	f := filters{since: since}
	if f.match(old) {
		t.Error("age filter should exclude the 48h-old repo")
	}
	if !f.match(recent) {
		t.Error("age filter should admit the 1h-old repo")
	}
}

func TestSortRows(t *testing.T) {
	now := time.Now()
	a := row("a", nil)
	a.Refs.Branches = []gitmode.BranchInfo{{Name: "main", CommittedAt: now.Add(-time.Hour)}}
	b := row("b", nil)
	b.Refs.Branches = []gitmode.BranchInfo{{Name: "main", CommittedAt: now.Add(-48 * time.Hour)}}
	c := row("c", nil)
	c.Work = &gitmode.WorkInfo{Unstaged: 5}

	rows := []RepoState{b, c, a}
	sortRows(rows, "activity", false)
	if rows[0].Name != "a" || rows[2].Name != "c" {
		t.Errorf("activity sort = [%s %s %s], want a first", rows[0].Name, rows[1].Name, rows[2].Name)
	}
	sortRows(rows, "activity", true)
	if rows[0].Name != "c" {
		t.Errorf("reverse activity sort = [%s %s %s], want c first", rows[0].Name, rows[1].Name, rows[2].Name)
	}
	sortRows(rows, "changes", false)
	if rows[0].Name != "c" {
		t.Errorf("changes sort = [%s %s %s], want c first", rows[0].Name, rows[1].Name, rows[2].Name)
	}
	sortRows(rows, "name", false)
	if rows[0].Name != "a" || rows[1].Name != "b" || rows[2].Name != "c" {
		t.Errorf("name sort = [%s %s %s], want a b c", rows[0].Name, rows[1].Name, rows[2].Name)
	}
}

func TestFuzzyMatch(t *testing.T) {
	if !fuzzyMatch("colds", "coldstorage") {
		t.Error("prefix subsequence should match")
	}
	if !fuzzyMatch("cs", "coldstorage") {
		t.Error("subsequence should match")
	}
	if fuzzyMatch("zz", "coldstorage") {
		t.Error("zz should not match")
	}
	if !fuzzyMatch("AB", "xayb") {
		t.Error("case-insensitive subsequence should match")
	}
}

func TestRelAge(t *testing.T) {
	now := time.Now()
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "now"},
		{30 * time.Second, "now"},
		{4 * time.Minute, "4m"},
		{3 * time.Hour, "3h"},
		{6 * 24 * time.Hour, "6d"},
		{5 * 7 * 24 * time.Hour, "5w"},
		{2 * 30 * 24 * time.Hour, "2mo"},
		{2 * 365 * 24 * time.Hour, "2y"},
	}
	for _, tc := range cases {
		if got := relAge(now.Add(-tc.d), now); got != tc.want {
			t.Errorf("relAge(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
	if relAge(time.Time{}, now) != "?" {
		t.Error("zero time must render as ? (never, §7.2)")
	}
}

func TestChangesCell(t *testing.T) {
	if got := changesCell(row("c", nil)); got != "·" {
		t.Errorf("clean changes = %q, want ·", got)
	}
	r := row("c", func(r *RepoState) {
		r.Work = &gitmode.WorkInfo{Conflicts: 1, Staged: 2, Unstaged: 3, Untracked: 4}
	})
	if got := changesCell(r); got != "!1 +2 ~3 ?4" {
		t.Errorf("changes = %q, want !1 +2 ~3 ?4", got)
	}
}

func TestColumnVisibilityAndWidths(t *testing.T) {
	cols := visibleColumns(defaultColumns())
	if len(cols) != 11 {
		t.Fatalf("default columns = %d, want 11 (three optional start hidden)", len(cols))
	}
	m := defaultColumns()
	m.on["stashes"] = true
	cols = visibleColumns(m)
	if len(cols) != 12 || cols[len(cols)-1].header != "STASHES" {
		t.Fatalf("stashes toggle produced %d columns", len(cols))
	}
	widths := columnWidths(cols, []RepoState{row("demo", nil)})
	for i, col := range cols {
		if widths[i] < len(col.header) {
			t.Errorf("column %s width %d < header", col.header, widths[i])
		}
	}
}

func TestFilterKeymap(t *testing.T) {
	if got := filterByKey("d"); got != "dirty" {
		t.Errorf("filterByKey(d) = %q", got)
	}
	if got := filterByKey("n"); got != "never-fetched" {
		t.Errorf("filterByKey(n) = %q", got)
	}
	if !knownFilter("dirty") || knownFilter("bogus") {
		t.Error("knownFilter is wrong")
	}
	if !validSort("activity") || validSort("bogus") {
		t.Error("validSort is wrong")
	}
}
