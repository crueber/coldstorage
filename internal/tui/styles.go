// The §12 color grammar: yellow dirty, cyan unpushed, magenta needs-release,
// red conflicts, dim clean. These are lipgloss styles, not ASCII art — the
// visual target is the drydock dashboard, and the grammar lives in exactly
// one place so every view colors the same verdict the same way.
package tui

import "github.com/charmbracelet/lipgloss"

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
	yellow := lipgloss.Color("3")
	cyan := lipgloss.Color("6")
	magenta := lipgloss.Color("5")
	red := lipgloss.Color("1")

	styles.header = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("8"))
	styles.selected = lipgloss.NewStyle().Background(lipgloss.Color("236"))
	styles.spinner = lipgloss.NewStyle().Foreground(cyan)
	styles.status = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styles.warn = lipgloss.NewStyle().Foreground(red)
	styles.dirty = lipgloss.NewStyle().Foreground(yellow)
	styles.unpushed = lipgloss.NewStyle().Foreground(cyan)
	styles.release = lipgloss.NewStyle().Foreground(magenta)
	styles.conflict = lipgloss.NewStyle().Foreground(red)
	styles.clean = lipgloss.NewStyle().Faint(true)
	styles.bare = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styles.errorSt = lipgloss.NewStyle().Foreground(red)
	styles.dim = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styles.title = lipgloss.NewStyle().Bold(true)
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
