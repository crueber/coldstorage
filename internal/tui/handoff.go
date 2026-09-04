// The hand-offs (§12): t opens a git TUI on the selected repo, o opens a
// TUI file manager there, and T hands the whole terminal to a shell in the
// repo's directory until it exits. Every one goes through tea.ExecProcess,
// which releases the terminal to the child and hands it back on exit —
// coldstorage is suspended, not competing. Detection is a PATH lookup; the
// config's [ui] command overrides win over detection, with {path} standing
// in for the repo root.
package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// externalDoneMsg arrives when a handed-off child exits: the repo's state
// may have changed under the other tool, so the dashboard re-probes it.
type externalDoneMsg struct {
	root   string
	tool   string
	failed bool
}

// execProcess is tea.ExecProcess; a package var so tests can run the child
// directly instead of through the terminal handoff.
var execProcess = tea.ExecProcess

// detectTool returns the first name from candidates present in PATH, "" if
// none.
func detectTool(candidates ...string) string {
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil && p != "" {
			return c
		}
	}
	return ""
}

// detectShell is the login shell a T hand-off uses. The override is
// [ui] terminal_command; this is the auto-detect path.
func detectShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "sh"
}

// overrideCmd builds the command for a configured hand-off: {path} is
// replaced by the repo root wherever it appears. exec.Command, not a
// hand-built Cmd: a bare name like "lazygit" must resolve against PATH
// before Dir applies, or every override that isn't a full path would fail
// to start.
func overrideCmd(override []string, root string) *exec.Cmd {
	argv := make([]string, len(override))
	for i, a := range override {
		argv[i] = strings.ReplaceAll(a, "{path}", root)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = root
	return cmd
}

// resolveHandoff builds the command for a git-UI or file-manager hand-off:
// the [ui] override wins, else the first detected candidate. nil means
// nothing to run — the caller notifies.
func resolveHandoff(override []string, candidates []string, root string) *exec.Cmd {
	if len(override) > 0 && strings.TrimSpace(override[0]) != "" {
		return overrideCmd(override, root)
	}
	if tool := detectTool(candidates...); tool != "" {
		cmd := exec.Command(tool)
		cmd.Dir = root
		return cmd
	}
	return nil
}

// selectedRepo is the repo the current view is about: the cursor's row on
// the table, the same row in the detail view.
func (m model) selectedRepo() (RepoState, bool) {
	rows := m.visibleRows()
	if m.sel < 0 || m.sel >= len(rows) {
		return RepoState{}, false
	}
	return rows[m.sel], true
}

// handoffCmd wraps one child run: ExecProcess frees the terminal for the
// child, and the done message makes the dashboard re-probe the repo — a
// commit made in lazygit must reach the table.
func handoffCmd(cmd *exec.Cmd, root, tool string) tea.Cmd {
	return execProcess(cmd, func(err error) tea.Msg {
		return externalDoneMsg{root: root, tool: tool, failed: err != nil}
	})
}

// openGitUI hands the selected repo to the configured or detected git TUI.
func (m model) openGitUI() (tea.Model, tea.Cmd) {
	r, ok := m.selectedRepo()
	if !ok {
		m.notify("no repo selected")
		return m, nil
	}
	cmd := resolveHandoff(m.cfg.UI.GitClientCommand, []string{"lazygit", "gitui", "lg", "tig"}, r.Root)
	if cmd == nil {
		m.notify("no git TUI found — install lazygit or gitui, or set [ui] git_client_command")
		return m, nil
	}
	return m, handoffCmd(cmd, r.Root, filepath.Base(cmd.Path))
}

// openFileManager hands the selected repo's directory to the configured or
// detected TUI file manager.
func (m model) openFileManager() (tea.Model, tea.Cmd) {
	r, ok := m.selectedRepo()
	if !ok {
		m.notify("no repo selected")
		return m, nil
	}
	cmd := resolveHandoff(m.cfg.UI.FileManagerCommand, []string{"spf", "superfile", "yazi", "ranger", "nnn"}, r.Root)
	if cmd == nil {
		m.notify("no file manager found — install superfile, yazi, ranger or nnn, or set [ui] file_manager_command")
		return m, nil
	}
	return m, handoffCmd(cmd, r.Root, filepath.Base(cmd.Path))
}

// openShell hands the whole terminal to the user's shell in the selected
// repo's directory; when the shell exits, coldstorage resumes and
// re-probes whatever the shell left behind.
func (m model) openShell() (tea.Model, tea.Cmd) {
	r, ok := m.selectedRepo()
	if !ok {
		m.notify("no repo selected")
		return m, nil
	}
	override := m.cfg.UI.TerminalCommand
	var cmd *exec.Cmd
	if len(override) > 0 && strings.TrimSpace(override[0]) != "" {
		cmd = overrideCmd(override, r.Root)
	} else {
		cmd = exec.Command(detectShell())
		cmd.Dir = r.Root
	}
	return m, handoffCmd(cmd, r.Root, "shell")
}
