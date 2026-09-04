// The hand-offs (§12): t / o / T release the terminal to a child process
// and re-probe the repo when it exits. The child runs are proven for real —
// fake tools on PATH write a file and exit.
package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/crueber/coldstorage/internal/config"
)

// stubExec swaps tea.ExecProcess for a runner that just runs the child and
// calls the callback — the terminal handoff itself is bubbletea's contract.
func stubExec(t *testing.T) {
	t.Helper()
	real := execProcess
	execProcess = func(c *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
		return func() tea.Msg {
			return fn(c.Run())
		}
	}
	t.Cleanup(func() { execProcess = real })
}

// putOnTestPATH prepends dir to PATH for the test.
func putOnTestPATH(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func writeFakeTool(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func handoffModel(t *testing.T, repoDir string) model {
	m := model{width: 120, height: 24, cfg: config.Default()}
	m.rows = map[string]RepoState{}
	r := repoAt(repoDir)
	r.Root = repoDir
	m.rows[repoDir] = r
	m.orgFilter = ""
	m.histRoot = ""
	return m
}

func TestResolveHandoffPrefersOverride(t *testing.T) {
	cmd := resolveHandoff([]string{"my-gitui", "--dir", "{path}"}, []string{"lazygit"}, "/repos/alpha")
	if cmd.Path != "my-gitui" || cmd.Args[1] != "--dir" || cmd.Args[2] != "/repos/alpha" {
		t.Errorf("override = %v, want {path} substituted", cmd.Args)
	}
	if cmd.Dir != "/repos/alpha" {
		t.Errorf("dir = %q, want the repo root", cmd.Dir)
	}
}

func TestResolveHandoffDetectsCandidate(t *testing.T) {
	bin := t.TempDir()
	writeFakeTool(t, bin, "gitui", "exit 0")
	t.Setenv("PATH", bin) // isolated: the host may have real candidates

	cmd := resolveHandoff(nil, []string{"lazygit", "gitui"}, "/repos/alpha")
	if cmd == nil || filepath.Base(cmd.Path) != "gitui" {
		t.Fatalf("detected %q, want gitui", cmd.Path)
	}
	if cmd.Dir != "/repos/alpha" {
		t.Errorf("dir = %q, want the repo root", cmd.Dir)
	}
}

func TestResolveHandoffNoneFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty PATH: nothing to find
	if cmd := resolveHandoff(nil, []string{"lazygit", "gitui"}, "/r"); cmd != nil {
		t.Errorf("expected nil with no candidates, got %v", cmd.Path)
	}
}

func TestDetectShellUsesEnv(t *testing.T) {
	t.Setenv("SHELL", "/bin/ksh")
	if got := detectShell(); got != "/bin/ksh" {
		t.Errorf("detectShell = %q, want $SHELL", got)
	}
	os.Unsetenv("SHELL")
	if got := detectShell(); got == "" {
		t.Error("detectShell must fall back, not return empty")
	}
}

// TestHandoffRunsForReal: t on the table hands the terminal to the child —
// the fake lazygit writes a marker file and exits; the done message
// arrives and the repo is queued for re-probe.
func TestHandoffRunsForReal(t *testing.T) {
	repo := t.TempDir()
	if err := execGit(t, "init", "-q", repo); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	writeFakeTool(t, bin, "lazygit", "touch "+marker)
	putOnTestPATH(t, bin)

	m := handoffModel(t, repo)
	stubExec(t)
	m2, cmd := m.keyTable(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if cmd == nil {
		t.Fatal("t must produce an exec command")
	}
	done, ok := cmd().(externalDoneMsg)
	if !ok || done.root != repo || done.failed {
		t.Errorf("done = %+v, %v", done, ok)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the fake git TUI never ran: %v", err)
	}
	// The returned model (m2) is untouched by the exec; the re-probe is the
	// done message's job.
	_ = m2
}

func TestHandoffFromDetailView(t *testing.T) {
	repo := t.TempDir()
	if err := execGit(t, "init", "-q", repo); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran-fm")
	writeFakeTool(t, bin, "spf", "touch "+marker)
	putOnTestPATH(t, bin)

	m := handoffModel(t, repo)
	m.mode = modeDetail
	stubExec(t)
	_, cmd := m.keyDetail(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	if cmd == nil {
		t.Fatal("o in detail must produce an exec command")
	}
	if done := cmd().(externalDoneMsg); done.root != repo || done.failed {
		t.Errorf("done = %+v", done)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the fake file manager never ran: %v", err)
	}
}

func TestHandoffShellUsesRepoDir(t *testing.T) {
	repo := t.TempDir()
	marker := filepath.Join(t.TempDir(), "cwd")

	m := handoffModel(t, repo)
	stubExec(t)
	// The override stands in for an interactive shell and proves the shell
	// ran IN the repo dir, with the same {path} wiring a real override
	// gets.
	m.cfg.UI.TerminalCommand = []string{"sh", "-c", "pwd > " + marker}
	_, cmd := m.keyTable(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'T'}})
	if cmd == nil {
		t.Fatal("T must produce an exec command")
	}
	if done := cmd().(externalDoneMsg); done.root != repo || done.failed {
		t.Errorf("done = %+v", done)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != repo {
		t.Errorf("shell ran in %q, want %q", strings.TrimSpace(string(got)), repo)
	}
}

func TestHandoffNoToolNotifies(t *testing.T) {
	repo := t.TempDir()
	m := handoffModel(t, repo)
	t.Setenv("PATH", t.TempDir())
	m2, cmd := m.keyTable(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if cmd != nil {
		t.Fatal("no tool found: nothing to exec")
	}
	if !strings.Contains(stripAnsi(m2.(model).status), "no git TUI found") {
		t.Errorf("status = %q, want the install hint", m2.(model).status)
	}
}

func TestHandoffNoSelection(t *testing.T) {
	m := model{width: 80, height: 24, cfg: config.Default()}
	m.rows = map[string]RepoState{}
	_, cmd := m.keyTable(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if cmd != nil {
		t.Fatal("no repo: nothing to exec")
	}
}

func TestOrgFilterMovedToShiftO(t *testing.T) {
	m := orgFilterModel()
	m = press(m, 'O')
	if m.orgFilter != orgKey(m.cfg.Orgs[0]) {
		t.Fatalf("O must cycle the org filter, got %q", m.orgFilter)
	}
	// Plain o is the file manager now: with no manager on PATH it
	// notifies instead of touching the filter.
	t.Setenv("PATH", t.TempDir())
	m2 := press(m, 'o')
	if m2.orgFilter != m.orgFilter {
		t.Error("o must not touch the org filter")
	}
}
