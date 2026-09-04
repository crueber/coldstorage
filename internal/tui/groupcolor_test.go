package tui

// Group background colors (§12): configured groups paint their rows, the
// selection outranks them, unconfigured groups are untouched.

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/crueber/coldstorage/internal/config"
	"github.com/crueber/coldstorage/internal/gitmode"
)

func colorModel() model {
	m := model{width: 120, height: 24, cfg: config.Default()}
	m.cfg.UI.GroupColors = map[string]string{"decisiv": "#1f2d3d", "crueber": "blue"}
	return m
}

func TestParseColorValue(t *testing.T) {
	cases := map[string]string{
		"#1F2D3D": "#1f2d3d", " blue ": "4", "Bright-Cyan": "14",
		"gray": "8", "9": "9", "": "", "sparkle": "", "#zz": "", "#12345": "",
	}
	for in, want := range cases {
		if got := parseColorValue(in); got != want {
			t.Errorf("parseColorValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGroupColorCaseInsensitive(t *testing.T) {
	m := colorModel()
	for _, g := range []string{"decisiv", "Decisiv", "DECISIV"} {
		if string(m.groupColor(g)) != "#1f2d3d" {
			t.Errorf("groupColor(%q) missed — group keys must match case-insensitively", g)
		}
	}
	if m.groupColor("acme") != "" {
		t.Error("an unconfigured group must have no background")
	}
}

func TestGroupBackgroundRenders(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	m := colorModel()
	cols := visibleColumns(defaultColumns())
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = len(c.header)
	}

	trow := row("terra-boxes", nil)
	trow.Group = "decisiv"
	line := m.tableRow(cols, widths, trow, false)
	// The background renders across the whole padded row, not just one cell.
	if !strings.Contains(line, "\x1b[48;5;") && !strings.Contains(line, "\x1b[48;2;") {
		t.Errorf("no background escape on a colored group row: %q", line[:60])
	}
	if !strings.Contains(line, "1f2d3d") && !strings.Contains(line, "48;5;") {
		t.Errorf("background color lost: %q", line[:60])
	}

	// The selection outranks the group color.
	selected := m.tableRow(cols, widths, trow, true)
	if !strings.Contains(stripAnsi(selected), "decisiv") {
		t.Fatal("selected row lost its content")
	}

	// An unconfigured group keeps the default style: no background escape.
	plain := row("alpha", nil)
	plain.Group = "acme"
	if line := m.tableRow(cols, widths, plain, false); strings.Contains(line, "\x1b[48;") {
		t.Errorf("unconfigured group got a background: %q", line[:60])
	}
}

func TestGroupBackgroundDirtyRow(t *testing.T) {
	// The verdict grammar is foreground-only: a dirty row in a colored
	// group keeps its yellow verdict on the group background.
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	m := colorModel()
	cols := visibleColumns(defaultColumns())
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = len(c.header)
	}
	r := row("beta", nil)
	r.Group = "decisiv"
	r.Work = &gitmode.WorkInfo{Unstaged: 2}
	if st := m.stateRowBase(r, false).GetBackground(); st == nil {
		t.Error("dirty rows in a colored group must carry the background")
	}
	line := m.tableRow(cols, widths, r, false)
	if !strings.Contains(stripAnsi(line), "dirty") {
		t.Error("dirty verdict text lost on a colored row")
	}
}
