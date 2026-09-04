// The group colors overlay (§12, G): every discovered group, one row each,
// with its configured background. enter/space/l cycle forward through the
// palette, h cycles back, x clears, and every change is written to the
// config on the spot — the table behind the overlay re-renders with the
// new background immediately, and the toml never drifts from the screen.
package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// groupPalette is the curated cycle: muted darks that read as banding on
// dark terminals without shouting, ordered so neighbors differ in hue.
var groupPalette = []string{
	"#1f2d3d", // navy
	"#173f2c", // pine
	"#3b2f1b", // umber
	"#2d1f3b", // plum
	"#3b1f26", // wine
	"#12383f", // teal
	"#402d18", // amber-brown
	"#1f3b3d", // petrol
	"#3b1f3b", // orchid
	"#2a3b1f", // moss
	"#33333f", // slate
	"#3b2d2d", // clay
}

// knownGroups lists the distinct groups in the fleet — lowercase, deduped
// (logins differ in case between registrations) and sorted, so the overlay
// is deterministic no matter how the discovery map iterates.
func (m model) knownGroups() []string {
	seen := map[string]struct{}{}
	for _, r := range m.rows {
		if r.Group == "" {
			continue
		}
		seen[strings.ToLower(r.Group)] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for g := range seen {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// storedKey is the config key for a group: lowercase, so a group's rows
// always resolve no matter how the login is cased.
func storedKey(group string) string { return strings.ToLower(group) }

// cycleGroupColor moves a group's color through the palette ("" = no
// background first), delta ±1, and writes the config on every change.
func (m model) cycleGroupColor(group string, delta int) (tea.Model, tea.Cmd) {
	key := storedKey(group)
	current := m.cfg.UI.GroupColors[key]
	idx := -1
	for i, c := range groupPalette {
		if c == current {
			idx = i
			break
		}
	}
	if m.cfg.UI.GroupColors == nil {
		m.cfg.UI.GroupColors = map[string]string{}
	}
	var next string
	if idx == -1 {
		if delta >= 0 {
			next = groupPalette[0]
		} else {
			next = groupPalette[len(groupPalette)-1]
		}
	} else {
		next = groupPalette[(idx+delta+len(groupPalette))%len(groupPalette)]
	}
	m.cfg.UI.GroupColors[key] = next
	return m, writeOrgConfig(m.cfg, fmt.Sprintf("group color: %s = %s", group, next), false)
}

// clearGroupColor removes a group's background and writes the config.
func (m model) clearGroupColor(group string) (tea.Model, tea.Cmd) {
	key := storedKey(group)
	if _, ok := m.cfg.UI.GroupColors[key]; !ok {
		return m, nil
	}
	delete(m.cfg.UI.GroupColors, key)
	return m, writeOrgConfig(m.cfg, fmt.Sprintf("group color: %s cleared", group), false)
}

// groupsView paints the G overlay: one row per discovered group, the
// current color swatched in place, the cursor highlighted.
func (m model) groupsView() string {
	var b strings.Builder
	b.WriteString(styles.title.Render("group colors — enter/l cycle, h back, x clear, esc close"))
	b.WriteString("\n\n")

	groups := m.knownGroups()
	if m.groupCursor >= len(groups) {
		m.groupCursor = len(groups) - 1
	}
	if m.groupCursor < 0 {
		m.groupCursor = 0
	}
	if len(groups) == 0 {
		b.WriteString(styles.dim.Render("no groups discovered yet"))
		b.WriteString("\n\n")
	}

	for i, g := range groups {
		color := m.cfg.UI.GroupColors[storedKey(g)]
		value := styles.dim.Render("default")
		if color != "" {
			value = lipgloss.NewStyle().Background(lipgloss.Color(color)).Render(" " + color + " ")
		}
		cursor := "  "
		if i == m.groupCursor {
			cursor = "> "
		}
		b.WriteString(cursor + g + "  " + value)
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(styles.status.Render("j/k move · enter/l next color · h previous · x clear · esc close"))
	return b.String()
}

// keyGroups handles the G overlay's keys. Every color change persists
// immediately: the file and the screen are the same truth.
func (m model) keyGroups(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	groups := m.knownGroups()
	switch {
	case anyKey(key, "esc", "q"):
		m.mode = modeTable
		return m, nil
	case len(groups) == 0:
		return m, nil
	case anyKey(key, "j", "down"):
		m.groupCursor = (m.groupCursor + 1) % len(groups)
	case anyKey(key, "k", "up"):
		m.groupCursor = (m.groupCursor - 1 + len(groups)) % len(groups)
	case anyKey(key, "enter", " ", "l"):
		return m.cycleGroupColor(groups[m.groupCursor], 1)
	case anyKey(key, "h", "left"):
		return m.cycleGroupColor(groups[m.groupCursor], -1)
	case keyIs(key, "x"):
		return m.clearGroupColor(groups[m.groupCursor])
	}
	return m, nil
}
