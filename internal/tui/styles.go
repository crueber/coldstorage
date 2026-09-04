// The §12 color grammar: yellow dirty, cyan unpushed, magenta needs-release,
// red conflicts, dim clean. These are lipgloss styles, not ASCII art — the
// visual target is the drydock dashboard, and the grammar lives in exactly
// one place so every view colors the same verdict the same way. The colors
// themselves come from the detected theme (internal/theme): the shell's own
// scheme — Omarchy's active theme, the terminal's background, a [ui]
// override — grounds the same grammar in the user's colors.
package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/crueber/coldstorage/internal/theme"
)

var styles struct {
	header   lipgloss.Style // table header row
	selected lipgloss.Style // selection highlight
	spinner  lipgloss.Style
	status   lipgloss.Style
	warn     lipgloss.Style
	dirty    lipgloss.Style
	unpushed lipgloss.Style
	release  lipgloss.Style
	conflict lipgloss.Style
	clean    lipgloss.Style
	bare     lipgloss.Style
	errorSt  lipgloss.Style
	dim      lipgloss.Style
	title    lipgloss.Style
}

func init() {
	applyTheme(theme.Generic(true))
}

// applyTheme re-grounds the grammar in a resolved theme. This is the only
// place a color is chosen; every view renders the same verdict the same
// way through the shared styles.
func applyTheme(t theme.Theme) {
	styles.header = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.Header))
	styles.selected = lipgloss.NewStyle().Background(lipgloss.Color(t.SelectedBg))
	styles.spinner = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent))
	styles.status = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
	styles.warn = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Conflict))
	styles.dirty = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Dirty))
	styles.unpushed = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Unpushed))
	styles.release = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Release))
	styles.conflict = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Conflict))
	styles.clean = lipgloss.NewStyle().Faint(true)
	styles.bare = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
	styles.errorSt = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Conflict))
	styles.dim = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Dim))
	styles.title = lipgloss.NewStyle().Bold(true)
}

// ApplyTheme detects the environment's scheme and grounds the dashboard's
// colors in it. Call it once before the program starts: the terminal is
// asked about its background before the alternate screen takes over, and
// no view re-resolves colors while running.
func ApplyTheme(configured string) {
	applyTheme(theme.Detect(configured, lipgloss.HasDarkBackground()))
}

// stateStyle maps a row's state to the §12 color grammar.
func stateStyle(r RepoState) lipgloss.Style {
	switch r.State() {
	case "conflict":
		return styles.conflict
	case "error":
		return styles.errorSt
	case "dirty", "merging", "rebasing", "cherry-picking", "reverting", "bisecting":
		return styles.dirty
	case "unpushed":
		return styles.unpushed
	case "bare":
		return styles.bare
	case "clean":
		return styles.clean
	default: // "…": never scanned
		return styles.dim
	}
}
