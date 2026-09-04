// The org filter (§12): cycling registered orgs with `o`, matching by
// checkout path — the same resolution the org manager's ON DISK count uses.
package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/crueber/coldstorage/internal/config"
	"github.com/crueber/coldstorage/internal/gitmode"
)

func orgFilterModel() model {
	m := model{width: 120, height: 24}
	m.rows = map[string]RepoState{}
	m.cfg = config.Default()
	m.cfg.Roots = []string{"~/dev/github.com", "~/Projects"}
	m.cfg.Orgs = []config.OrgConfig{
		{Provider: "github", Host: "github.com", Owner: "crueber", Path: "~/dev/github.com/crueber", Enabled: true},
		{Provider: "github", Host: "github.com", Owner: "decisiv", Path: "~/dev/github.com/decisiv", Enabled: true},
	}
	// Rows live under the registrations' real resolved paths (ResolvedPath
	// expands ~ against the running user's home), plus one repo outside
	// every org.
	for _, root := range []string{
		m.cfg.Orgs[0].ResolvedPath(m.cfg) + "/alpha",
		m.cfg.Orgs[0].ResolvedPath(m.cfg) + "/beta",
		m.cfg.Orgs[1].ResolvedPath(m.cfg) + "/gamma",
		m.cfg.Orgs[1].ResolvedPath(m.cfg) + "/../elsewhere/delta",
	} {
		r := repoAt(filepath.Clean(root))
		m.rows[r.Root] = r
	}
	return m
}

func repoAt(root string) RepoState {
	r := row(root[strings.LastIndex(root, "/")+1:], nil)
	r.Root = root
	return r
}

func press(m model, r rune) model {
	nm, _ := m.keyTable(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return nm.(model)
}

// visibleSlugs lists the visible rows in display order.
func visibleSlugs(m model) []string {
	var out []string
	for _, r := range m.visibleRows() {
		out = append(out, r.Slug())
	}
	return out
}

func TestOrgFilterMatchByPath(t *testing.T) {
	f := filters{orgPath: "/home/u/dev/github.com/crueber"}
	if !f.match(repoAt("/home/u/dev/github.com/crueber/alpha")) {
		t.Error("a repo inside the org path must match")
	}
	if !f.match(repoAt("/home/u/dev/github.com/crueber")) {
		t.Error("the org path itself must match")
	}
	if f.match(repoAt("/home/u/dev/github.com/crueberish/x")) {
		t.Error("a sibling prefix must not match")
	}
	if f.match(repoAt("/home/u/dev/github.com/decisiv/gamma")) {
		t.Error("another org's repo must not match")
	}
	if !(filters{}).match(repoAt("/anywhere")) {
		t.Error("no org filter matches everything")
	}
}

func TestOrgFilterCycle(t *testing.T) {
	m := orgFilterModel()

	m = press(m, 'O')
	if m.orgFilter != orgKey(m.cfg.Orgs[0]) {
		t.Fatalf("first press = %q, want the first registration", m.orgFilter)
	}
	got := visibleSlugs(m)
	if len(got) != 2 || !strings.Contains(got[0], "alpha") {
		t.Errorf("crueber filter shows %v", got)
	}

	m = press(m, 'O')
	if m.orgFilter != orgKey(m.cfg.Orgs[1]) {
		t.Fatalf("second press = %q, want the second registration", m.orgFilter)
	}
	got = visibleSlugs(m)
	if len(got) != 1 || !strings.Contains(got[0], "gamma") {
		t.Errorf("decisiv filter shows %v", got)
	}

	m = press(m, 'O')
	if m.orgFilter != "" {
		t.Fatalf("third press = %q, want all (wrapped)", m.orgFilter)
	}
	if len(visibleSlugs(m)) != 4 {
		t.Error("all filter must show the whole fleet")
	}
}

func TestOrgFilterClearsAndSurvivesEdits(t *testing.T) {
	m := orgFilterModel()
	m = press(m, 'O')
	m = press(m, 'a')
	if m.orgFilter != "" {
		t.Fatal("a (clear all) must clear the org filter")
	}

	// A filter whose org is removed from the config is dropped when the
	// new config lands (orgSavedMsg normalizes it) — the matcher itself
	// treats an unresolvable path as "no org filter", never as "match one
	// broken path".
	m = orgFilterModel()
	m.orgFilter = "github/github.com/gone"
	if p := m.orgFilterPath(); p != "" {
		t.Errorf("removed org resolved to %q", p)
	}
	if len(visibleSlugs(m)) != 4 {
		t.Errorf("unresolvable filter must fall open, got %d rows", len(visibleSlugs(m)))
	}
}

func TestOrgFilterResolvesEditedPath(t *testing.T) {
	// The filter resolves the path at match time: an edited registration
	// changes what the filter matches without touching the filter itself.
	m := orgFilterModel()
	m = press(m, 'O')
	m.cfg.Orgs[0].Path = "~/dev/github.com/decisiv"
	if got := m.orgFilterPath(); !strings.HasSuffix(got, "decisiv") {
		t.Fatalf("edited org resolves to %q", got)
	}
	got := visibleSlugs(m)
	if len(got) != 1 || !strings.Contains(got[0], "gamma") {
		t.Errorf("after the edit the filter shows %v", got)
	}
}

func TestOrgFilterSummary(t *testing.T) {
	lipgloss.SetColorProfile(termenv.Ascii)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	m := orgFilterModel()
	m = press(m, 'O')
	s := stripAnsi(m.filterSummary())
	if !strings.Contains(s, "org: crueber on github.com") {
		t.Errorf("summary = %q, want the org named", s)
	}
}

func TestOrgFilterWithNoOrgs(t *testing.T) {
	m := orgFilterModel()
	m.cfg.Orgs = nil
	m = press(m, 'O')
	if m.orgFilter != "" {
		t.Error("no registrations: the filter must stay clear")
	}
}

func TestHeaderStatsSurviveRealProfile(t *testing.T) {
	// Belt and braces over TestHeaderLineStatsSurviveAnsi: with a color
	// profile active and the widget on the line, every count must land.
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	m := orgFilterModel()
	m.width = 60
	for root, r := range m.rows {
		if strings.HasSuffix(root, "alpha") {
			r.Work = &gitmode.WorkInfo{Unstaged: 2}
			m.rows[root] = r
		}
	}
	m.sweeping = true
	m.sweepTotal = 4
	m.swept = 4
	line := m.headerLine(m.headerView())
	plain := stripAnsi(line)
	if !strings.Contains(plain, "repos 4") || !strings.Contains(plain, "dirty 1") {
		t.Errorf("stats lost: %q", plain)
	}
	if lipgloss.Width(line) > 60 {
		t.Errorf("line is %d wide, terminal is 60", lipgloss.Width(line))
	}
}
