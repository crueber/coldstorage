// Update-level tests: synthetic key messages against the §12 keymap, plus a
// full Bubble Tea program smoke test over buffered input/output (network-
// free, terminal-free).
package tui

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/crueber/coldstorage/internal/config"
	"github.com/crueber/coldstorage/internal/gitmode"
)

// key constructs a key message the way the input reader would.
func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEscape}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "pgup":
		return tea.KeyMsg{Type: tea.KeyPgUp}
	case "pgdn":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// testModel builds a model with a small fleet and a fixed screen size.
func testModel(t *testing.T, rows ...RepoState) model {
	t.Helper()
	cfg := config.Default()
	cfg.Roots = []string{t.TempDir()} // discovery over an empty root is safe
	cfg.Refresh.Watch = false
	m := newModel(cfg, t.TempDir(), nil, nil, nil)
	m.width, m.height = 100, 30
	for _, r := range rows {
		m.rows[r.Root] = r
	}
	m.rebuildGroups()
	return m
}

func demoRow(root, name string) RepoState {
	r := RepoState{Root: root, Group: "grp", Name: name, Refs: gitmode.RefsInfo{}}
	r.Refs.Head = gitmode.Head{Kind: gitmode.HeadBranch, Branch: "main"}
	r.Refs.Branches = []gitmode.BranchInfo{{Name: "main", CommittedAt: time.Now().Add(-2 * time.Hour)}}
	r.Refs.FetchedAt = time.Now().Add(-time.Hour)
	return r
}

func sendKey(t *testing.T, m model, s string) (model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(key(s))
	return next.(model), cmd
}

func TestUpdateSelectionMovement(t *testing.T) {
	m := testModel(t,
		demoRow("/r/a", "a"),
		demoRow("/r/b", "b"),
		demoRow("/r/c", "c"),
	)

	if m.sel != 0 {
		t.Fatalf("initial sel = %d, want 0", m.sel)
	}
	m, _ = sendKey(t, m, "j")
	if m.sel != 1 {
		t.Fatalf("after j sel = %d, want 1", m.sel)
	}
	m, _ = sendKey(t, m, "down")
	if m.sel != 2 {
		t.Fatalf("after down sel = %d, want 2", m.sel)
	}
	// Clamped at the end.
	m, _ = sendKey(t, m, "down")
	if m.sel != 2 {
		t.Fatalf("clamped-end sel = %d, want 2", m.sel)
	}
	m, _ = sendKey(t, m, "k")
	m, _ = sendKey(t, m, "up")
	if m.sel != 0 {
		t.Fatalf("after k+up sel = %d, want 0", m.sel)
	}
	// end jumps to the last row; home back to the first.
	m, _ = sendKey(t, m, "end")
	if m.sel != 2 {
		t.Fatalf("end sel = %d, want 2", m.sel)
	}
	m, _ = sendKey(t, m, "home")
	if m.sel != 0 {
		t.Fatalf("home sel = %d, want 0", m.sel)
	}
}

func TestUpdateFilterToggles(t *testing.T) {
	m := testModel(t,
		demoRow("/r/a", "a"),
	)
	dirty := demoRow("/r/dirty", "dirty")
	dirty.Work = &gitmode.WorkInfo{Unstaged: 1}
	m.rows["/r/dirty"] = dirty

	m, _ = sendKey(t, m, "d")
	if !m.filterKinds["dirty"] {
		t.Fatal("d did not toggle the dirty filter")
	}
	rows := m.visibleRows()
	if len(rows) != 1 || rows[0].Name != "dirty" {
		t.Fatalf("dirty filter rows = %v", rows)
	}
	m, _ = sendKey(t, m, "&")
	if !m.matchAll {
		t.Fatal("& did not switch to all-matching")
	}
	m, _ = sendKey(t, m, "a")
	if len(m.filterKinds) != 0 || m.matchAll {
		t.Fatal("a did not clear the filters")
	}
	if len(m.visibleRows()) != 2 {
		t.Fatal("clearing filters should show the whole fleet")
	}
}

func TestUpdateAgePresetsAndGroupCycle(t *testing.T) {
	m := testModel(t, demoRow("/r/a", "a"))

	m, _ = sendKey(t, m, "2")
	if m.ageIdx != 2 {
		t.Fatalf("preset 2 -> ageIdx %d, want 2 (24h)", m.ageIdx)
	}
	if len(m.visibleRows()) != 1 {
		t.Fatal("1h-old repo should pass the 24h preset")
	}
	m, _ = sendKey(t, m, "1")
	if len(m.visibleRows()) != 0 {
		t.Fatal("2h-old repo should fail the 1h preset")
	}
	m, _ = sendKey(t, m, "0")
	if m.ageIdx != 0 || len(m.visibleRows()) != 1 {
		t.Fatal("preset 0 must clear the age filter")
	}

	m, _ = sendKey(t, m, "]")
	if m.group != "grp" {
		t.Fatalf("] group = %q, want grp", m.group)
	}
	m, _ = sendKey(t, m, "]")
	if m.group != "" {
		t.Fatalf("] past the last group = %q, want empty", m.group)
	}
}

func TestUpdateSortCycleAndSearch(t *testing.T) {
	m := testModel(t, demoRow("/r/a", "a"))

	m, _ = sendKey(t, m, "s")
	if m.sortKey != "name" {
		t.Fatalf("s from activity -> %q, want name", m.sortKey)
	}
	m, _ = sendKey(t, m, "S")
	if !m.sortReverse {
		t.Fatal("S did not reverse the sort")
	}

	m, _ = sendKey(t, m, "/")
	if !m.searching {
		t.Fatal("/ did not open the search prompt")
	}
	m, _ = sendKey(t, m, "d")
	m, _ = sendKey(t, m, "e")
	if m.searchBuf != "de" {
		t.Fatalf("search buffer = %q, want de", m.searchBuf)
	}
	m, _ = sendKey(t, m, "esc")
	if m.searching || m.search != "" {
		t.Fatal("esc must cancel the search without committing")
	}
	m, _ = sendKey(t, m, "/")
	m, _ = sendKey(t, m, "e")
	m, _ = sendKey(t, m, "enter")
	if m.searching || m.search != "e" {
		t.Fatalf("enter must commit the search, got %q", m.search)
	}
	if len(m.visibleRows()) != 0 {
		t.Fatal("fuzzy e must not match repo a")
	}
}

func TestUpdateOverlaysAndQuit(t *testing.T) {
	m := testModel(t, demoRow("/r/a", "a"))

	m, _ = sendKey(t, m, "?")
	if m.mode != modeHelp {
		t.Fatal("? did not open help")
	}
	m, _ = sendKey(t, m, "esc")
	if m.mode != modeTable {
		t.Fatal("esc did not close help")
	}

	m, _ = sendKey(t, m, "C")
	if m.mode != modeColumns {
		t.Fatal("C did not open the column picker")
	}
	m, _ = sendKey(t, m, "j")
	m, _ = sendKey(t, m, " ")
	// The cursor walked into an optional column; it should now be on.
	m, _ = sendKey(t, m, "esc")
	if m.mode != modeTable {
		t.Fatal("esc did not close the column picker")
	}

	m, _ = sendKey(t, m, "enter")
	if m.mode != modeDetail {
		t.Fatal("enter did not open the detail view")
	}
	m, _ = sendKey(t, m, "esc")
	if m.mode != modeTable {
		t.Fatal("esc did not return from the detail view")
	}

	var cmd tea.Cmd
	m, cmd = sendKey(t, m, "q")
	if cmd == nil {
		t.Fatal("q must quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q command is not Quit")
	}
}

func TestUpdateProbeBatchMerges(t *testing.T) {
	m := testModel(t, demoRow("/r/a", "a"))

	batch := probeBatchMsg{repos: []RepoState{demoRow("/r/b", "b")}}
	next, _ := m.Update(batch)
	m2 := next.(model)
	if len(m2.rows) != 2 {
		t.Fatalf("rows after batch = %d, want 2", len(m2.rows))
	}

	// §5: a sweep that no longer sees a repo drops it.
	next, _ = m2.Update(sweepDoneMsg{roots: []string{"/r/a"}})
	m3 := next.(model)
	if len(m3.rows) != 1 || m3.rows["/r/a"].Root != "/r/a" {
		t.Fatalf("rows after sweep = %d, want only /r/a", len(m3.rows))
	}
}

func TestUpdateWarnSurfaces(t *testing.T) {
	m := testModel(t, demoRow("/r/a", "a"))
	next, _ := m.Update(warnMsg{err: errors.New("watch: out of watches")})
	m2 := next.(model)
	if !strings.Contains(m2.status, "watch: out of watches") {
		t.Fatalf("status = %q, want the warning", m2.status)
	}
}

// The frame test: the initial model renders the header with fleet counts
// and the table's column frame with its rows.
func TestInitialFrame(t *testing.T) {
	cfg := config.Default()
	cfg.Roots = []string{t.TempDir()}
	cfg.Refresh.Watch = false

	m := newModel(cfg, t.TempDir(), map[string]RepoState{
		"/r/a": demoRow("/r/a", "alpha"),
		"/r/b": demoRow("/r/b", "beta"),
	}, nil, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	frame := next.(model).View()

	for _, want := range []string{"coldstorage", "repos 2", "REPO", "BRANCH", "STATE", "alpha", "beta", "sorted by activity"} {
		if !strings.Contains(frame, want) {
			t.Errorf("initial frame missing %q", want)
		}
	}
}

// The smoke test: a real Bubble Tea program runs the event loop end to end
// over buffered input/output — Init's sweep command, ticks, and a clean
// quit — without a terminal.
func TestProgramSmoke(t *testing.T) {
	cfg := config.Default()
	cfg.Roots = []string{t.TempDir()}
	cfg.Refresh.Watch = false

	var in, out bytes.Buffer
	m := newModel(cfg, t.TempDir(), nil, nil, nil)

	prog := tea.NewProgram(m,
		tea.WithInput(&in), tea.WithOutput(&out),
		tea.WithoutSignalHandler(), tea.WithoutRenderer(),
	)
	go func() {
		prog.Send(tea.WindowSizeMsg{Width: 120, Height: 40})
		time.Sleep(300 * time.Millisecond)
		prog.Quit()
	}()
	if _, err := prog.Run(); err != nil {
		t.Fatalf("program: %v", err)
	}
}
