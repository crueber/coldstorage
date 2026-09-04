// The main table (GO-PORT-SPEC.md §12): the column grammar, the filter and
// sort key set, and the fuzzy search. Everything here is pure so the
// dashboard's verdicts are unit-testable without a terminal: given the same
// rows and the same filter/sort state, every view derives the same answer.
package tui

import (
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/crueber/coldstorage/internal/gitmode"
)

// filterNames is the filter key set (§12: d u r N b c i x e n), each with
// its meaning, in help-screen order.
var filterNames = []struct {
	key  string // the config's default_filters spelling
	key1 string // the key on the table
	desc string
}{
	{"dirty", "d", "dirty working tree"},
	{"unpushed", "u", "commits not pushed"},
	{"needs-release", "r", "commits or changes past the tag"},
	{"released", "N", "tagged, nothing since"},
	{"behind", "b", "behind its upstream"},
	{"conflict", "c", "merge conflicts"},
	{"in-progress", "i", "merge/rebase/cherry-pick/revert in progress"},
	{"clean", "x", "clean working tree"},
	{"errored", "e", "last probe errored"},
	{"never-fetched", "n", "never fetched"},
}

// knownFilter reports whether name is a filter the config may ask for.
func knownFilter(name string) bool {
	for _, f := range filterNames {
		if f.key == name {
			return true
		}
	}
	return false
}

// filterByKey maps a table key to its filter name.
func filterByKey(key string) string {
	for _, f := range filterNames {
		if f.key1 == key {
			return f.key
		}
	}
	return ""
}

// matchesKind is the single predicate for one filter kind. These are the
// §8-derived verdicts, word for word with the JSON contract (§10).
func matchesKind(r RepoState, kind string) bool {
	switch kind {
	case "dirty":
		return r.Work != nil && r.Work.Dirty()
	case "unpushed":
		return r.Refs.Unpushed() > 0
	case "needs-release":
		return r.Release() == gitmode.ReleaseNeedsRelease
	case "released":
		return r.Release() == gitmode.ReleaseReleased
	case "behind":
		return r.Refs.Unpulled() > 0
	case "conflict":
		return r.Work != nil && r.Work.Conflicts > 0
	case "in-progress":
		return r.Refs.Operation != ""
	case "clean":
		return r.Err == nil && r.State() == "clean"
	case "errored":
		return r.Err != nil
	case "never-fetched":
		return r.Refs.FetchedAt.IsZero()
	}
	return false
}

// filters is the complete current filter state, passed to match.
type filters struct {
	kinds    map[string]bool
	matchAll bool      // & toggles between any and all
	since    time.Time // zero = any age
	group    string    // "" = all groups
	search   string    // fuzzy, on group/name/branch
}

// match reports whether one row passes. Zero active filters, no group, no
// age preset, and no search match everything, so an unfiltered dashboard is
// the identity.
func (f filters) match(r RepoState) bool {
	if f.group != "" && r.Group != f.group {
		return false
	}
	if !f.since.IsZero() && r.Activity().Before(f.since) {
		return false
	}
	if f.search != "" && !fuzzyMatch(f.search, r.Group, r.Name, r.Refs.Head.BranchName()) {
		return false
	}
	if len(f.kinds) == 0 {
		return true
	}
	if f.matchAll {
		for kind := range f.kinds {
			if !matchesKind(r, kind) {
				return false
			}
		}
		return true
	}
	for kind := range f.kinds {
		if matchesKind(r, kind) {
			return true
		}
	}
	return false
}

// sortKeys is the s-cycle order (§12). activity is the default because it
// is what the owner asks first: what have I touched recently (§7.6).
var sortKeys = []string{"activity", "name", "group", "state", "changes", "ahead", "behind"}

// validSort reports whether s is a sort key the config may ask for.
func validSort(s string) bool {
	for _, k := range sortKeys {
		if k == s {
			return true
		}
	}
	return false
}

// sortRows orders rows in place. It is stable so equal keys keep discovery
// order, and the base order is slug, which makes rendering deterministic.
func sortRows(rows []RepoState, key string, reverse bool) {
	base := func(a, b RepoState) int {
		switch strings.Compare(a.Slug(), b.Slug()) {
		case -1:
			return -1
		case 1:
			return 1
		}
		return 0
	}
	less := func(a, b RepoState) bool {
		switch key {
		case "name":
			if a.Name != b.Name {
				return a.Name < b.Name
			}
		case "group":
			if a.Group != b.Group {
				return a.Group < b.Group
			}
		case "state":
			if a.State() != b.State() {
				return a.State() < b.State()
			}
		case "changes":
			ac, bc := workTotal(a), workTotal(b)
			if ac != bc {
				return ac > bc // most-changed first
			}
		case "ahead":
			if a.Refs.Unpushed() != b.Refs.Unpushed() {
				return a.Refs.Unpushed() > b.Refs.Unpushed()
			}
		case "behind":
			if a.Refs.Unpulled() != b.Refs.Unpulled() {
				return a.Refs.Unpulled() > b.Refs.Unpulled()
			}
		default: // activity: newest first
			if !a.Activity().Equal(b.Activity()) {
				return a.Activity().After(b.Activity())
			}
		}
		return base(a, b) < 0
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if reverse {
			return less(rows[j], rows[i])
		}
		return less(rows[i], rows[j])
	})
}

func workTotal(r RepoState) int {
	if r.Work == nil {
		return 0
	}
	return r.Work.Total()
}

// fuzzyMatch is the / search: a case-insensitive in-order subsequence match
// against any of the fields. It asks "does this repo plausibly match what I
// typed", not "does it score best" — a dashboard over 1,000 repos needs the
// cheap question.
func fuzzyMatch(needle string, fields ...string) bool {
	n := strings.ToLower(needle)
	for _, f := range fields {
		if n == "" {
			return true
		}
		h := strings.ToLower(f)
		i := 0
		for j := 0; j < len(h) && i < len(n); j++ {
			if h[j] == n[i] {
				i++
			}
		}
		if i == len(n) {
			return true
		}
	}
	return false
}

// relAge renders the compact relative form of §10: now, 4m, 3h, 6d, 5w,
// 2mo, 2y. A zero time (never) renders as "?" — zero there is a claim
// nobody checked (§7.2).
func relAge(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "m")
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "h")
	case d < 7*24*time.Hour:
		return plural(int(d.Hours()/24), "d")
	case d < 60*24*time.Hour:
		return plural(int(d.Hours()/24/7), "w")
	case d < 730*24*time.Hour:
		return plural(int(d.Hours()/24/30), "mo")
	default:
		return plural(int(d.Hours()/24/365), "y")
	}
}

func plural(n int, unit string) string {
	if n <= 1 {
		return "1" + unit
	}
	return itoa(n) + unit
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// changesCell renders the CHANGES grammar of §10/§12: !n conflicts, +n
// staged, ~n unstaged, ?n untracked, joined with spaces, · when empty.
func changesCell(r RepoState) string {
	if r.Work == nil || r.Work.Total() == 0 {
		return "·"
	}
	var parts []string
	if r.Work.Conflicts > 0 {
		parts = append(parts, "!"+itoa(r.Work.Conflicts))
	}
	if r.Work.Staged > 0 {
		parts = append(parts, "+"+itoa(r.Work.Staged))
	}
	if r.Work.Unstaged > 0 {
		parts = append(parts, "~"+itoa(r.Work.Unstaged))
	}
	if r.Work.Untracked > 0 {
		parts = append(parts, "?"+itoa(r.Work.Untracked))
	}
	return strings.Join(parts, " ")
}

// column is one table column. cell renders the plain text; style picks the
// §12 color grammar. optional columns are toggled from the C picker.
type column struct {
	id       string
	header   string
	optional bool
	cell     func(RepoState) string
	styled   func(RepoState, string, bool) string
}

// defaultColumns is the §12 column set, left to right. The optional three
// start hidden; the C picker turns them on for the session (§12).
func defaultColumns() columnSet {
	return columnSet{on: map[string]bool{}}
}

// columnSet tracks which optional columns the session has enabled.
type columnSet struct {
	on map[string]bool
}

// visible reports whether the column shows. Core columns are always on.
func (c columnSet) visible(col column) bool {
	if !col.optional {
		return true
	}
	return c.on[col.id]
}

// columnCatalog is every column, in §12 order.
var columnCatalog = []column{
	{"group", "GROUP", false,
		func(r RepoState) string { return r.Group },
		func(r RepoState, s string, sel bool) string { return s }},
	{"repo", "REPO", false,
		func(r RepoState) string { return r.Name },
		func(r RepoState, s string, sel bool) string { return s }},
	{"branch", "BRANCH", false,
		func(r RepoState) string { return r.Refs.Head.Label() },
		func(r RepoState, s string, sel bool) string { return s }},
	{"state", "STATE", false,
		func(r RepoState) string { return r.State() },
		func(r RepoState, s string, sel bool) string {
			if sel {
				return s
			}
			return stateStyle(r).Render(s)
		}},
	{"release", "RELEASE", false,
		func(r RepoState) string { return string(r.Release()) },
		func(r RepoState, s string, sel bool) string {
			if sel {
				return s
			}
			if r.Release() == gitmode.ReleaseNeedsRelease {
				return styles.release.Render(s)
			}
			return s
		}},
	{"changes", "CHANGES", false,
		changesCell,
		func(r RepoState, s string, sel bool) string { return s }},
	{"ahead", "AHEAD", false,
		func(r RepoState) string { return numberOrDot(r.Refs.Unpushed()) },
		func(r RepoState, s string, sel bool) string { return s }},
	{"behind", "BEHIND", false,
		func(r RepoState) string {
			if r.Refs.FetchedAt.IsZero() {
				return "?" // never fetched: "?" is a claim nobody checked (§7.2)
			}
			return numberOrDot(r.Refs.Unpulled())
		},
		func(r RepoState, s string, sel bool) string { return s }},
	{"tag", "TAG", false,
		func(r RepoState) string {
			if r.Refs.NewestTag == nil {
				return "·"
			}
			return r.Refs.NewestTag.Name
		},
		func(r RepoState, s string, sel bool) string { return s }},
	{"tagage", "+TAG", false,
		func(r RepoState) string {
			if r.Refs.NewestTag == nil {
				return "·"
			}
			return relAge(r.Refs.NewestTag.At, time.Now())
		},
		func(r RepoState, s string, sel bool) string { return s }},
	{"age", "AGE", false,
		func(r RepoState) string { return relAge(r.Activity(), time.Now()) },
		func(r RepoState, s string, sel bool) string { return s }},
	{"visibility", "VISIBILITY", true,
		func(r RepoState) string {
			if r.Visibility == nil || r.Visibility.Status != gitmode.VisKnown {
				return "·"
			}
			return string(r.Visibility.Seen)
		},
		func(r RepoState, s string, sel bool) string { return s }},
	{"stashes", "STASHES", true,
		func(r RepoState) string { return numberOrDot(r.Refs.Stashes) },
		func(r RepoState, s string, sel bool) string { return s }},
	{"fetched", "FETCHED", true,
		func(r RepoState) string { return relAge(r.Refs.FetchedAt, time.Now()) },
		func(r RepoState, s string, sel bool) string { return s }},
}

func numberOrDot(n int) string {
	if n == 0 {
		return "·"
	}
	return itoa(n)
}

// visibleColumns returns the columns currently shown, in order.
func visibleColumns(c columnSet) []column {
	var out []column
	for _, col := range columnCatalog {
		if c.visible(col) {
			out = append(out, col)
		}
	}
	return out
}

// columnWidths sizes each visible column to its content (§12: BRANCH sized
// to content), including the header, over the rows that will render.
func columnWidths(cols []column, rows []RepoState) []int {
	widths := make([]int, len(cols))
	for i, col := range cols {
		widths[i] = lipgloss.Width(col.header)
		for _, r := range rows {
			if w := lipgloss.Width(col.cell(r)); w > widths[i] {
				widths[i] = w
			}
		}
	}
	return widths
}
