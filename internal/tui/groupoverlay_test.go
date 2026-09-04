package tui

// The G overlay: group colors set in the TUI persist to the config file on
// every change, and the table re-renders with them immediately.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/coldstorage/internal/config"
)

// overlayModel is a model with two groups on screen and a config directory
// redirected into the test's tempdir, so writes never touch the real file.
func overlayModel(t *testing.T) model {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := model{width: 120, height: 24, cfg: config.Default()}
	m.rows = map[string]RepoState{}
	for _, g := range []string{"crueber", "decisiv", "DECISIV"} {
		r := row("repo", nil)
		r.Root = "/r/" + strings.ToLower(g)
		r.Group = g
		m.rows[r.Root] = r
	}
	return m
}

func TestKnownGroupsDedupesCase(t *testing.T) {
	m := overlayModel(t)
	got := m.knownGroups()
	if len(got) != 2 {
		t.Fatalf("groups = %v, want crueber + decisiv (case-deduped)", got)
	}
	if got[0] != "crueber" || got[1] != "decisiv" {
		t.Errorf("order = %v, want crueber then decisiv (case-insensitive)", got)
	}
}

func TestGroupOverlayCyclesAndPersists(t *testing.T) {
	m := overlayModel(t)
	m, _ = sendKey(t, m, "G")
	if m.mode != modeGroups {
		t.Fatal("G must open the group colors overlay")
	}

	// Cycle forward onto the first palette color (cursor starts on
	// crueber, the first group). The returned command performs the config
	// write; the event loop runs it through Update.
	m, wcmd := sendKey(t, m, "enter")
	m = runCmd(t, m, wcmd)
	key := "crueber"
	want := groupPalette[0]
	if got := m.cfg.UI.GroupColors[key]; got != want {
		t.Fatalf("color = %q, want %q", got, want)
	}

	// The change persisted to the config file.
	data, err := os.ReadFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "coldstorage", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `crueber = "`+want+`"`) {
		t.Errorf("config file missing the color:\n%s", data)
	}

	// Cycle back through the palette and past it wraps.
	m, wcmd = sendKey(t, m, "h")
	m = runCmd(t, m, wcmd)
	if got := m.cfg.UI.GroupColors[key]; got != groupPalette[len(groupPalette)-1] {
		t.Errorf("h wrap = %q, want the palette tail", got)
	}

	// x clears.
	m, wcmd = sendKey(t, m, "x")
	m = runCmd(t, m, wcmd)
	if _, ok := m.cfg.UI.GroupColors[key]; ok {
		t.Error("x must clear the group color")
	}
	if m.groupColor("crueber") != "" {
		t.Error("cleared group still colored")
	}
}

func TestGroupOverlayTableRenders(t *testing.T) {
	m := overlayModel(t)
	m, _ = sendKey(t, m, "G")
	m, wcmd := sendKey(t, m, "enter")
	m = runCmd(t, m, wcmd)
	if v := m.View(); !strings.Contains(v, "group colors") {
		t.Error("overlay title lost")
	}
	// esc closes and the table keeps the color.
	m, _ = sendKey(t, m, "esc")
	if m.mode != modeTable {
		t.Fatalf("esc mode = %v, want modeTable", m.mode)
	}
	if m.groupColor("crueber") == "" {
		t.Error("closing the overlay must keep the color")
	}
}
