// The §12 chrome tests: the operation widget in the header's upper right,
// the full-width header line it sits on, and the detail view's commit
// history stream. Everything here is pure model → string, no terminal.
package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/crueber/coldstorage/internal/gitmode"
)

func widgetModel() model {
	return model{width: 80, height: 24}
}

func TestOpWidgetIdle(t *testing.T) {
	if got := widgetModel().opWidget(); got != "" {
		t.Errorf("an idle queue must render nothing, got %q", got)
	}
}

func TestOpWidgetSweep(t *testing.T) {
	m := widgetModel()
	m.sweeping = true
	m.sweepTotal = 556
	m.swept = 123
	got := m.opWidget()
	if !strings.Contains(got, "sweep") || !strings.Contains(got, "123/556") {
		t.Errorf("sweep widget = %q, want label and progress", got)
	}
}

func TestOpWidgetSync(t *testing.T) {
	m := widgetModel()
	m.syncRunning = true
	m.syncOrg = "crueber on github.com"
	m.syncProgress = "update walgit"
	got := m.opWidget()
	if !strings.Contains(got, "sync crueber on github.com") || !strings.Contains(got, "update walgit") {
		t.Errorf("sync widget = %q, want what the queue is working on", got)
	}
	// The sync outranks the sweep in the widget: the user asked for it.
	m.sweeping = true
	if !strings.Contains(m.opWidget(), "sync") {
		t.Error("a running sync must win the widget")
	}
}

func TestHeaderLineRightAlignsWidget(t *testing.T) {
	m := widgetModel()
	m.syncRunning = true
	m.syncOrg = "crueber"
	m.syncProgress = "3/10"
	line := m.headerLine("coldstorage repos 556")
	plain := strings.ReplaceAll(line, "\x1b", "")
	_ = plain
	if !strings.Contains(line, "sync crueber") {
		t.Error("widget content lost")
	}
	if !strings.HasPrefix(line, "coldstorage repos 556") {
		t.Errorf("left content must lead: %q", line)
	}
	if !strings.HasSuffix(line, "3/10") {
		t.Errorf("widget must end on the right edge: %q", line)
	}
}

func TestHeaderLineIdle(t *testing.T) {
	m := widgetModel()
	line := m.headerLine("coldstorage repos 3")
	if w := lipgloss.Width(line); w != 19 {
		t.Errorf("idle header = %d wide, want left content only (19)", w)
	}
}

func TestHeaderLineDropsWidgetWhenTooNarrow(t *testing.T) {
	m := model{width: 12}
	m.syncRunning = true
	m.syncOrg = "a-very-long-org-name"
	line := m.headerLine("coldstorage repos 556")
	if w := lipgloss.Width(line); w > 12 {
		t.Errorf("narrow terminal: line is %d wide, must fit 12", w)
	}
	if strings.Contains(line, "sync") {
		t.Error("a widget that cannot fit must be dropped, not wrapped")
	}
}

func TestHeaderLineSweepProgress(t *testing.T) {
	m := widgetModel()
	m.sweeping = true
	m.sweepTotal = 556
	m.swept = 556
	line := m.headerLine(m.headerView())
	if !strings.Contains(line, "556/556") {
		t.Errorf("completed sweep progress lost: %q", line)
	}
}

func histModel() model {
	r := row("demo", nil)
	r.Root = "/tmp/demo"
	m := model{width: 80, height: 24}
	m.rows = map[string]RepoState{r.Root: r}
	m.histRoot = r.Root
	return m
}

func TestHistoryLinesHiddenForOtherRepo(t *testing.T) {
	m := histModel()
	m.histCommits = []gitmode.Commit{{Time: time.Now(), Subject: "elsewhere"}}
	other := row("demo", nil)
	other.Root = "/tmp/other"
	if got := m.historyLines(other, time.Now()); got != nil {
		t.Errorf("history must not leak across repos: %v", got)
	}
}

func TestHistoryLinesEmptyRepo(t *testing.T) {
	m := histModel()
	r := row("demo", nil)
	r.Root = "/tmp/demo"
	got := strings.Join(m.historyLines(r, time.Now()), "\n")
	if !strings.Contains(got, "COMMITS") || !strings.Contains(got, "no commits") {
		t.Errorf("empty history must say so: %q", got)
	}
}

func TestHistoryLinesError(t *testing.T) {
	m := histModel()
	m.histErr = errTest
	m.histDone = true
	r := row("demo", nil)
	r.Root = "/tmp/demo"
	got := strings.Join(m.historyLines(r, time.Now()), "\n")
	if !strings.Contains(got, "history unavailable") {
		t.Errorf("a failed history must be admitted: %q", got)
	}
}

func TestHistoryLinesRendersTitles(t *testing.T) {
	m := histModel()
	m.histCommits = []gitmode.Commit{
		{Time: time.Now().Add(-time.Hour), Subject: "fix the thing"},
		{Time: time.Now().Add(-48 * time.Hour), Subject: "start the thing"},
	}
	r := row("demo", nil)
	r.Root = "/tmp/demo"
	got := strings.Join(m.historyLines(r, time.Now()), "\n")
	if !strings.Contains(got, "fix the thing") || !strings.Contains(got, "start the thing") {
		t.Errorf("subjects missing: %q", got)
	}
	if strings.Contains(got, "loading") {
		t.Error("not loading: no loader line")
	}
	m.histLoading = true
	if got := strings.Join(m.historyLines(r, time.Now()), "\n"); !strings.Contains(got, "loading…") {
		t.Errorf("loading state must show: %q", got)
	}
}

func TestMaybeLoadHistoryTriggersNearEnd(t *testing.T) {
	m := histModel()
	r := row("demo", nil)
	r.Root = "/tmp/demo"

	// A fresh detail view has its first page already in flight (the enter
	// handler started it): no second fetch until it lands.
	m.histLoading = true
	m.detailOff = 1 << 20
	if cmd := m.maybeLoadHistory(); cmd != nil {
		t.Error("an in-flight page must not double-fetch")
	}
	m.histLoading = false

	// Parked past the loaded end: fetch.
	if cmd := m.maybeLoadHistory(); cmd == nil {
		t.Error("scrolling past the loaded end must fetch the next page")
	}

	// A loaded fleet of titles, viewport at the top: far from the end.
	m.histLoading = false
	m.histCommits = make([]gitmode.Commit, histPage)
	m.detailOff = 0
	if cmd := m.maybeLoadHistory(); cmd != nil {
		t.Error("the top of a loaded history must not fetch")
	}
}
func TestMaybeLoadHistoryStopsWhenDone(t *testing.T) {
	m := histModel()
	m.histDone = true
	m.detailOff = 1 << 20
	if cmd := m.maybeLoadHistory(); cmd != nil {
		t.Error("a finished history must not fetch again")
	}
}

func TestMaybeLoadHistoryGuardsRepo(t *testing.T) {
	m := histModel()
	m.histRoot = "/tmp/other"
	m.detailOff = 1 << 20
	if cmd := m.maybeLoadHistory(); cmd != nil {
		t.Error("no fetch for a repo that is not the selected one")
	}
}

var errTest = &testError{}

type testError struct{}

func (*testError) Error() string { return "boom" }
