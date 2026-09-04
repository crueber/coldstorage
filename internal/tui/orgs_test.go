// Org manager tests (GO-PORT-SPEC.md §12 org manager, §11): overlay
// rendering, the form's probe lifecycle, in-overlay validation refusals,
// the save splice + config write + §11.4 root wiring, the two-press remove,
// and the sync progress/summary path. All network-free: the probe and
// owner listing run through stubbed seams, and the sync's "update" rows
// fail fast inside a directory that is not a git repository.
package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/crueber/coldstorage/internal/config"
	"github.com/crueber/coldstorage/internal/orgsync"
)

// orgTestCfg builds a config with one root and the given registrations, and
// points the platform config dir at a throwaway so saves write somewhere
// disposable.
func orgTestCfg(t *testing.T, orgs ...config.OrgConfig) config.Config {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Roots = []string{t.TempDir()}
	cfg.Refresh.Watch = false
	cfg.Orgs = orgs
	return cfg
}

// orgTestModel builds the model over orgTestCfg without an engine.
func orgTestModel(t *testing.T, orgs ...config.OrgConfig) (model, config.Config) {
	t.Helper()
	cfg := orgTestCfg(t, orgs...)
	m := newModel(cfg, t.TempDir(), nil, nil, nil)
	m.width, m.height = 100, 30
	return m, cfg
}

// demoOrg is a registration under the config's first root.
func demoOrg(root, owner string) config.OrgConfig {
	return config.OrgConfig{
		Provider: "github",
		Host:     "github.com",
		Owner:    owner,
		Path:     filepath.Join(root, owner),
		Protocol: "ssh",
		Enabled:  true,
	}
}

// stubProbe installs a probe seam that answers per-binary and returns a
// restore func.
func stubProbe(t *testing.T, fn func(name string, args ...string) (string, error)) func() {
	t.Helper()
	old := probeRunner
	probeRunner = func(timeout time.Duration, name string, args ...string) (string, error) {
		return fn(name, args...)
	}
	return func() { probeRunner = old }
}

// runCmd executes a Bubble Tea command and hands its message back through
// Update, the way the event loop would.
func runCmd(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	if cmd == nil {
		return m
	}
	next, _ := m.Update(cmd())
	return next.(model)
}

func TestOrgsOverlayRenders(t *testing.T) {
	root := t.TempDir()
	m, _ := orgTestModel(t, demoOrg(root, "acme"), demoOrg(root, "other"))
	m.rows[demoOrg(root, "acme").Path+"/proj"] = demoRow("/r/a", "a")

	m, _ = sendKey(t, m, "A")
	if m.mode != modeOrgs {
		t.Fatalf("after A mode = %v, want modeOrgs", m.mode)
	}
	view := m.View()
	for _, want := range []string{"PROVIDER", "OWNER", "HOST", "PATH", "ON DISK", "LAST SYNC", "github", "acme", "other", "never"} {
		if !strings.Contains(view, want) {
			t.Errorf("org overlay missing %q in view:\n%s", want, view)
		}
	}
	// ON DISK counts the live rows under the org path.
	if !strings.Contains(view, "1") {
		t.Errorf("org overlay should count 1 checkout on disk:\n%s", view)
	}
	if !strings.Contains(view, "j/k move") {
		t.Errorf("org overlay should carry its keymap footer:\n%s", view)
	}

	m, _ = sendKey(t, m, "j")
	if m.orgSel != 1 {
		t.Fatalf("after j orgSel = %d, want 1", m.orgSel)
	}
	m, _ = sendKey(t, m, "esc")
	if m.mode != modeTable {
		t.Fatalf("after esc mode = %v, want modeTable", m.mode)
	}
}

func TestOrgFormProbeStateLands(t *testing.T) {
	restore := stubProbe(t, func(name string, args ...string) (string, error) {
		if name == "gh" {
			return "\n  ✓ Logged in to github.com account octocat (keyring)\n", nil
		}
		return "", os.ErrNotExist
	})
	defer restore()

	m, _ := orgTestModel(t)
	m, _ = sendKey(t, m, "A")
	m, cmd := sendKey(t, m, "a")
	if m.mode != modeOrgForm {
		t.Fatalf("after a mode = %v, want modeOrgForm", m.mode)
	}
	if view := m.View(); !strings.Contains(view, "probing tool auth…") {
		t.Fatalf("form should open in the probing state:\n%s", view)
	}
	if cmd == nil {
		t.Fatal("opening the form must return the probe command")
	}

	m = runCmd(t, m, cmd)
	f := m.orgForm
	if !f.probeDone || f.probing {
		t.Fatal("probe should have landed")
	}
	if f.provider != "github" || f.host != "github.com" {
		t.Fatalf("form preselected %s/%s, want github/github.com", f.provider, f.host)
	}
	if view := m.View(); strings.Contains(view, "probing tool auth…") {
		t.Errorf("probe landed but the form still shows the probing state:\n%s", view)
	}
}

func TestOrgFormSaveRefusedWithoutAuth(t *testing.T) {
	// §11.1: the gate — no auth, no add, and the refusal names the login
	// commands that would unlock the providers.
	restore := stubProbe(t, func(name string, args ...string) (string, error) {
		return "", os.ErrNotExist // nothing authenticated
	})
	defer restore()

	m, _ := orgTestModel(t)
	m, _ = sendKey(t, m, "A")
	m, cmd := sendKey(t, m, "a")
	m = runCmd(t, m, cmd)

	m2, _ := m.saveOrg()
	if m2.orgForm.refusal == "" {
		t.Fatal("save with no authenticated CLI must be refused")
	}
	for _, want := range []string{"gh auth login", "glab auth login", "tea login add"} {
		if !strings.Contains(m2.orgForm.refusal, want) {
			t.Errorf("refusal %q should name %q", m2.orgForm.refusal, want)
		}
	}
	if view := m2.View(); !strings.Contains(view, "refused:") {
		t.Errorf("refusal must render inside the overlay:\n%s", view)
	}
	if len(m.cfg.Orgs) != 0 {
		t.Errorf("refused save must not touch the config; got %d orgs", len(m.cfg.Orgs))
	}
}

func TestOrgFormValidationRefusalRendersInOverlay(t *testing.T) {
	restore := stubProbe(t, func(name string, args ...string) (string, error) {
		if name == "gh" {
			return "  ✓ Logged in to github.com account octocat\n", nil
		}
		return "", os.ErrNotExist
	})
	defer restore()

	root := t.TempDir()
	m, cfg := orgTestModel(t, demoOrg(root, "acme"))
	// The auto-fill default derives from the config's first root.
	root = cfg.Roots[0]
	m, _ = sendKey(t, m, "A")
	m, cmd := sendKey(t, m, "a")
	m = runCmd(t, m, cmd)

	// Cursor walks provider → host → owner; type the duplicate owner.
	m, _ = sendKey(t, m, "j")
	m, _ = sendKey(t, m, "j")
	for _, r := range "acme" {
		m, _ = sendKey(t, m, string(r))
	}
	if m.orgForm.owner != "acme" {
		t.Fatalf("owner = %q, want acme", m.orgForm.owner)
	}
	// The path auto-fills the resolved default (§11.4).
	if want := filepath.Join(root, "acme"); m.orgForm.path != want {
		t.Fatalf("path = %q, want %q", m.orgForm.path, want)
	}

	m2, cmd := m.saveOrg()
	if cmd != nil {
		t.Fatal("a duplicate registration must not reach the config write")
	}
	if !strings.Contains(m2.orgForm.refusal, "duplicate") {
		t.Fatalf("refusal = %q, want a duplicate-registration complaint", m2.orgForm.refusal)
	}
	if view := m2.View(); !strings.Contains(view, "refused:") {
		t.Errorf("refusal must render inside the overlay:\n%s", view)
	}
	if m2.mode != modeOrgForm {
		t.Errorf("refused save must stay in the form; mode = %v", m2.mode)
	}
	if len(cfg.Orgs) != 1 {
		t.Errorf("refused save must not touch the config; got %d orgs", len(cfg.Orgs))
	}
}

func TestOrgSaveSplicesWritesAndAutoRoots(t *testing.T) {
	restore := stubProbe(t, func(name string, args ...string) (string, error) {
		if name == "gh" {
			return "  ✓ Logged in to github.com account octocat\n", nil
		}
		return "", os.ErrNotExist
	})
	defer restore()

	m, _ := orgTestModel(t)
	m, _ = sendKey(t, m, "A")
	m, cmd := sendKey(t, m, "a")
	m = runCmd(t, m, cmd)
	m, _ = sendKey(t, m, "j")
	m, _ = sendKey(t, m, "j")
	for _, r := range "acme" {
		m, _ = sendKey(t, m, string(r))
	}

	m, cmd = m.saveOrg()
	if cmd == nil {
		t.Fatal("a valid save must return the config-write command")
	}
	m = runCmd(t, m, cmd)

	if m.mode != modeOrgs {
		t.Fatalf("after save mode = %v, want modeOrgs", m.mode)
	}
	if len(m.cfg.Orgs) != 1 || m.cfg.Orgs[0].Owner != "acme" {
		t.Fatalf("save must splice the org into the live config; got %+v", m.cfg.Orgs)
	}

	// The write is real: the platform config file round-trips the org.
	path, err := orgConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("saved config must reload: %v", err)
	}
	if len(loaded.Orgs) != 1 || loaded.Orgs[0].Owner != "acme" {
		t.Fatalf("saved config orgs = %+v, want one acme org", loaded.Orgs)
	}
}

func TestAutoRootWiresOrgPathParent(t *testing.T) {
	// §11.4: an org path under no root contributes its parent to roots.
	cfg := config.Default()
	cfg.Roots = []string{t.TempDir()}
	org := config.OrgConfig{Provider: "github", Host: "github.com", Owner: "acme"}
	org.Path = filepath.Join(t.TempDir(), "acme")
	cfg.Orgs = []config.OrgConfig{org}

	autoRoot(&cfg, org)
	if len(cfg.Roots) != 2 {
		t.Fatalf("roots after autoRoot = %v, want the org's parent added", cfg.Roots)
	}
	if cfg.Roots[1] != config.Contract(filepath.Dir(org.Path)) {
		t.Fatalf("new root = %q, want %q", cfg.Roots[1], config.Contract(filepath.Dir(org.Path)))
	}
	// Idempotent: an org already under a root adds nothing.
	cfg2 := config.Default()
	cfg2.Roots = []string{t.TempDir()}
	org2 := demoOrg(cfg2.Roots[0], "acme")
	cfg2.Orgs = []config.OrgConfig{org2}
	autoRoot(&cfg2, org2)
	if len(cfg2.Roots) != 1 {
		t.Fatalf("roots = %v; an org under a root must not add a root", cfg2.Roots)
	}
}

func TestOrgRemoveRequiresTwoPresses(t *testing.T) {
	root := t.TempDir()
	m, _ := orgTestModel(t, demoOrg(root, "acme"))

	// Somebody's checkout lives under the org path; removal must leave it.
	checkout := filepath.Join(root, "acme", "proj")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}

	m, _ = sendKey(t, m, "A")
	m, cmd := sendKey(t, m, "x")
	if cmd != nil {
		t.Fatal("the first x must only arm the confirm")
	}
	if len(m.cfg.Orgs) != 1 {
		t.Fatal("the first x must not remove the registration")
	}
	if !strings.Contains(m.View(), "press x again") {
		t.Errorf("the armed confirm must render in the overlay:\n%s", m.View())
	}

	m, cmd = sendKey(t, m, "x")
	if cmd == nil {
		t.Fatal("the second x must fire the remove")
	}
	m = runCmd(t, m, cmd)
	if len(m.cfg.Orgs) != 0 {
		t.Fatalf("remove must drop the registration; got %+v", m.cfg.Orgs)
	}
	if _, err := os.Stat(checkout); err != nil {
		// §11.3: nothing is ever deleted — checkouts are untouched.
		t.Fatalf("removal touched the checkout on disk: %v", err)
	}
	if strings.Contains(m.View(), "press x again") {
		t.Error("confirm must disarm after the remove fires")
	}
}

func TestOrgSyncProgressAndSummary(t *testing.T) {
	restore := func() {}
	defer restore()
	// The fake listing offers exactly what's on disk, so the plan is
	// update-only and the executor's git pull fails fast inside a plain
	// directory — no network, deterministic error rows (§11.3).
	orgListOld := orgListFn
	orgListFn = func(src orgsync.Source, timeout time.Duration) ([]orgsync.Repo, error) {
		return []orgsync.Repo{{Name: "plain", OwnerLogin: src.Owner, SSHURL: "git@" + src.Host + ":" + src.Owner + "/plain.git"}}, nil
	}
	defer func() { orgListFn = orgListOld }()

	root := t.TempDir()
	org := demoOrg(root, "acme")
	if err := os.MkdirAll(filepath.Join(org.Path, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := orgTestCfg(t, org)
	eng := newEngine(cfg, t.TempDir(), nil)
	msgs := make(chan tea.Msg, 64)
	eng.send = func(m any) { msgs <- m }
	m := newModel(cfg, t.TempDir(), nil, nil, eng)
	m.width, m.height = 100, 30

	m, _ = sendKey(t, m, "A")
	m, cmd := sendKey(t, m, "s")
	if cmd == nil {
		t.Fatal("s must start the sync")
	}
	if !m.syncRunning {
		t.Fatal("the model must know a sync is running")
	}
	// §12: while a sync runs, s/S say so.
	m2, _ := sendKey(t, m, "S")
	if !strings.Contains(m2.status, "already running") {
		t.Errorf("second sync request status = %q, want the already-running note", m2.status)
	}

	go cmd()
	var done orgSyncDoneMsg
	deadline := time.After(10 * time.Second)
	rows := 0
collect:
	for {
		select {
		case msg := <-msgs:
			switch msg := msg.(type) {
			case orgSyncRowMsg:
				rows++
			case orgSyncDoneMsg:
				done = msg
				break collect
			}
		case <-deadline:
			t.Fatal("timed out waiting for the sync to finish")
		}
	}
	if rows == 0 {
		t.Error("the sync should stream report rows (§11.5)")
	}
	if len(done.keys) != 1 || done.keys[0] != orgKey(org) {
		t.Fatalf("done keys = %v, want [%s]", done.keys, orgKey(org))
	}
	actions := map[string]int{}
	for _, o := range done.outcomes {
		actions[o.Action]++
	}
	if actions["error"] != 1 {
		t.Fatalf("outcome actions = %v, want one error row (git pull in a non-repo)", actions)
	}

	// The done message closes the sync, summarizes on the status line,
	// stamps the last-sync time, and triggers a sweep (§11.3).
	nm, cmd := m.Update(done)
	m3 := nm.(model)
	if m3.syncRunning {
		t.Error("sync must be closed by the done message")
	}
	if !strings.Contains(m3.status, "org sync done") || !strings.Contains(m3.status, "1 errors") {
		t.Errorf("status = %q, want the summary with 1 error", m3.status)
	}
	if _, ok := m3.orgLastSync[orgKey(org)]; !ok {
		t.Error("the synced org must earn its last-sync stamp")
	}
	if cmd == nil {
		t.Error("the done message must trigger the fresh-clone sweep")
	}
}

func TestOrgProbeParsers(t *testing.T) {
	gh := "github.com\n  ✓ Logged in to github.com account octocat (keyring)\n  - Active account: true\n\ngitlab.example.com\n  ✓ Logged in to gitlab.example.com account bot\n"
	hosts := parseGhAuthHosts(gh)
	if len(hosts) != 2 || hosts[0] != "github.com" || hosts[1] != "gitlab.example.com" {
		t.Fatalf("gh hosts = %v, want github.com + gitlab.example.com", hosts)
	}
	if got := parseGhAuthHosts("✗ Token absent"); len(got) != 0 {
		t.Fatalf("gh no-login parse = %v, want empty", got)
	}

	glab := "gitlab.com\n  ✓ Logged in as myuser\ngitlab.corp\n  ✓ Logged in as other\n"
	hosts = parseGlabAuthHosts(glab)
	if len(hosts) != 2 || hosts[0] != "gitlab.com" || hosts[1] != "gitlab.corp" {
		t.Fatalf("glab hosts = %v, want gitlab.com + gitlab.corp", hosts)
	}
	hosts = parseGlabAuthHosts("gitlab.com: Logged in as myuser")
	if len(hosts) != 1 || hosts[0] != "gitlab.com" {
		t.Fatalf("glab inline hosts = %v, want gitlab.com", hosts)
	}

	tea := " NAME      URL                        USER \n-----    ---------------------     -----\n def      https://gitea.com          me   \n corp     https://git.corp.io        me   \n"
	hosts = parseTeaLoginHosts(tea)
	if len(hosts) != 2 || hosts[0] != "git.corp.io" || hosts[1] != "gitea.com" {
		t.Fatalf("tea hosts = %v, want git.corp.io + gitea.com", hosts)
	}
}

func TestOrgOwnerPickerFetchesAndFallsBackToTyping(t *testing.T) {
	restore := stubProbe(t, func(name string, args ...string) (string, error) {
		if name == "gh" {
			return "  ✓ Logged in to github.com account octocat\n", nil
		}
		return "", os.ErrNotExist
	})
	defer restore()
	ownerOld := ownerListRunner
	ownerListRunner = func(timeout time.Duration, name string, args ...string) (string, error) {
		if name != "gh" {
			return "", os.ErrNotExist
		}
		if len(args) >= 2 && args[1] == "user/orgs" {
			return "acme\nbuildworks\n", nil
		}
		return "octocat\n", nil
	}
	defer func() { ownerListRunner = ownerOld }()

	m, _ := orgTestModel(t)
	m, _ = sendKey(t, m, "A")
	m, cmd := sendKey(t, m, "a")
	m = runCmd(t, m, cmd) // probe lands: github/github.com preselected

	// Cursor walks provider → host → owner; enter opens the picker.
	m, _ = sendKey(t, m, "j")
	m, _ = sendKey(t, m, "j")
	m, cmd = sendKey(t, m, "enter")
	if m.mode != modeOwners {
		t.Fatalf("enter on the owner row must open the picker; mode = %v", m.mode)
	}
	if cmd == nil {
		t.Fatal("opening the picker must fetch the owner list")
	}
	m = runCmd(t, m, cmd)
	if view := m.View(); !strings.Contains(view, "octocat") || !strings.Contains(view, "acme") {
		t.Errorf("picker should list the fetched owners:\n%s", view)
	}

	// j moves into the fetched list; enter picks.
	m, _ = sendKey(t, m, "j")
	m, _ = sendKey(t, m, "enter")
	if m.mode != modeOrgForm || m.orgForm.owner != "acme" {
		t.Fatalf("owner = %q mode = %v, want acme in the form", m.orgForm.owner, m.mode)
	}

	// Free-typing fallback: a typed owner that matches nothing still picks.
	m, cmd = sendKey(t, m, "enter")
	m = runCmd(t, m, cmd)
	for _, r := range "myown" {
		m, _ = sendKey(t, m, string(r))
	}
	m, _ = sendKey(t, m, "enter")
	if m.orgForm.owner != "myown" {
		t.Fatalf("owner = %q, want the typed fallback myown", m.orgForm.owner)
	}
	if m.mode != modeOrgForm {
		t.Fatalf("mode = %v, want back in the form", m.mode)
	}
}
