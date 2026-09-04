// The org manager (GO-PORT-SPEC.md §12 org manager, §11): the A-key overlay
// that lists, adds, edits, removes, and syncs the config's organization
// registrations. The §12 prime directive holds here more than anywhere: the
// auth probe, the owner fetch, the config write, and the sync engine are all
// network or filesystem work, and every one of them runs inside a Bubble Tea
// command or on the engine's goroutines — a key handler only ever flips
// state and returns a command. Validation is the one thing Update does
// itself, because cfg.OrgProblems is pure.
package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/crueber/coldstorage/internal/config"
	"github.com/crueber/coldstorage/internal/orgsync"
)

// orgListFn is the listing seam for syncs (§11.3): ListRepos in production,
// a stub in tests.
var orgListFn orgsync.ListFn = orgsync.ListRepos

// orgForm is the add/edit form's state. probing stays true — and save is
// refused — until the auth probe lands: the probe is a network call, and
// §12 forbids the UI thread from waiting on it. Rows cycle only through the
// authenticated options once it does (§11.1).
type orgForm struct {
	editing   int // index into cfg.Orgs; -1 adds a new registration
	probing   bool
	probeDone bool
	authed    []orgAuth

	provider string
	host     string
	owner    string
	path     string
	protocol string // ssh | https
	login    string // gitea only

	includeForks     bool
	includeArchived  bool
	includeSubgroups bool // gitlab only

	pathTouched bool // once the user edits the path, auto-fill stands down
	ownerFilter string
	owners      []string
	ownerCursor int
	ownersLoad  bool

	cursor  int // index into rows()
	refusal string
}

// formRow is one rendered line of the form. Rows are derived, not stored:
// the gitea login row and the gitlab subgroups row exist only when the
// selected provider uses them.
type formRow struct {
	id    string
	label string
	value string
}

func (f orgForm) rows() []formRow {
	rows := []formRow{
		{"provider", "provider", f.provider},
		{"host", "host", f.host},
		{"owner", "owner", f.owner},
		{"path", "path", f.path},
		{"protocol", "protocol", f.protocol},
	}
	if f.provider == "gitea" {
		rows = append(rows, formRow{"login", "login", f.login})
	}
	rows = append(rows,
		formRow{"forks", "include forks", toggleText(f.includeForks)},
		formRow{"archived", "include archived", toggleText(f.includeArchived)},
	)
	if f.provider == "gitlab" {
		rows = append(rows, formRow{"subgroups", "include subgroups", toggleText(f.includeSubgroups)})
	}
	return append(rows,
		formRow{"save", "save", "enter / ctrl-s"},
		formRow{"cancel", "cancel", "esc"},
	)
}

func toggleText(on bool) string {
	if on {
		return "yes"
	}
	return "no"
}

// newOrgForm opens the form: instantly, in the probing state (§12) — the
// probe has not run yet, so nothing is selectable and nothing is prefilled
// except the editing case's own values. An edit keeps the registration's
// fields; its path counts as touched so auto-fill never fights a configured
// value.
func newOrgForm(cfg config.Config, editing int) orgForm {
	f := orgForm{
		editing:     editing,
		probing:     true,
		protocol:    "ssh",
		pathTouched: editing >= 0,
	}
	if editing >= 0 && editing < len(cfg.Orgs) {
		o := cfg.Orgs[editing]
		f.provider = o.ResolvedProvider()
		f.host = o.Host
		f.owner = o.Owner
		f.path = o.Path
		f.protocol = o.Protocol
		f.login = o.Login
		f.includeForks = o.IncludeForks
		f.includeArchived = o.IncludeArchived
		f.includeSubgroups = o.IncludeSubgroups
	}
	return f
}

// orgKey is the registration identity the last-sync stamps and the remove
// confirm speak: provider/host/owner, the same triple §11.4 deems
// duplicates.
func orgKey(o config.OrgConfig) string {
	return o.ResolvedProvider() + "/" + strings.ToLower(o.Host) + "/" + o.Owner
}

// authedHas reports whether the probe found this provider/host pair — the
// gate a new (or provider/host-changed) registration must pass to save.
func authedHas(authed []orgAuth, provider, host string) bool {
	for _, a := range authed {
		if a.Provider != provider {
			continue
		}
		for _, h := range a.Hosts {
			if strings.EqualFold(h, host) {
				return true
			}
		}
	}
	return false
}

// probeTimeoutFor picks the probe's budget: the remote timeout (§4), because
// the auth checks are network-bound — they validate tokens against the host.
func probeTimeoutFor(cfg config.Config) time.Duration {
	return cfgDuration(cfg.Remote.Timeout, 20*time.Second)
}

// startOrgProbe runs the auth probe inside a command — never on the UI
// thread (§12).
func startOrgProbe(cfg config.Config) tea.Cmd {
	timeout := probeTimeoutFor(cfg)
	return func() tea.Msg { return orgAuthMsg{authed: probeAuth(timeout)} }
}

// orgAuthMsg lands the probe results on the form.
type orgAuthMsg struct{ authed []orgAuth }

// orgOwnersMsg lands the owner picker's fetch.
type orgOwnersMsg struct {
	owners []string
}

// orgSavedMsg reports a config write. The candidate config rides along so
// Update can replace the live config only once the disk agrees — a failed
// write must not leave a config the next save would silently overwrite.
type orgSavedMsg struct {
	cfg   config.Config
	err   error
	note  string
	close bool // close the form (a save, not a remove)
}

// orgConfigPath resolves where the config lives. The package cannot see
// main's -config override, so the platform location (§3) is the truth the
// org manager writes to.
func orgConfigPath() (string, error) {
	paths, err := config.ResolvePaths()
	if err != nil {
		return "", err
	}
	return paths.ConfigFile, nil
}

// writeOrgConfig performs the config write inside a command. The candidate
// has already passed validation; the write is the only thing left that can
// fail, and its failure comes back as a message the form re-renders as a
// refusal rather than a lost edit.
func writeOrgConfig(cand config.Config, note string, close bool) tea.Cmd {
	return func() tea.Msg {
		path, err := orgConfigPath()
		if err == nil {
			err = config.Save(path, cand)
		}
		return orgSavedMsg{cfg: cand, err: err, note: note, close: close}
	}
}

// pathUnderRoots reports whether p is a root or sits inside one.
func pathUnderRoots(p string, roots []string) bool {
	for _, r := range roots {
		r = config.Expand(r)
		if p == r || strings.HasPrefix(p, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// autoRoot implements the §11.4 wiring rule: an org whose checkout path sits
// under no root gets its path's parent added to roots, so clones land on the
// dashboard as a group with no manual config edit. The root is stored
// contracted (~) the way every other root is.
func autoRoot(cand *config.Config, org config.OrgConfig) {
	p := org.ResolvedPath(*cand)
	if p == "" || pathUnderRoots(p, cand.Roots) {
		return
	}
	parent := filepath.Dir(p)
	if !pathUnderRoots(parent, cand.Roots) {
		cand.Roots = append(cand.Roots, config.Contract(parent))
	}
}

// orgValue materializes the form's fields as a registration.
func (f orgForm) orgValue() config.OrgConfig {
	return config.OrgConfig{
		Provider:         f.provider,
		Host:             f.host,
		Owner:            f.owner,
		Path:             f.path,
		Login:            f.login,
		Protocol:         f.protocol,
		IncludeForks:     f.includeForks,
		IncludeArchived:  f.includeArchived,
		IncludeSubgroups: f.includeSubgroups,
		Enabled:          true,
	}
}

// autoFillPath applies the resolved default (<first root>/<owner>, §11.4)
// until the user edits the path by hand.
func (f *orgForm) autoFillPath(cfg config.Config) {
	if f.pathTouched || f.owner == "" || len(cfg.Roots) == 0 {
		return
	}
	f.path = config.Contract(filepath.Join(config.Expand(cfg.Roots[0]), f.owner))
}

// saveOrg validates and, when everything passes, launches the config write.
// Every refusal is rendered inside the overlay (§12: the status line is
// under it), and the gates run in the order a user can act on them: probe,
// auth, then cfg.OrgProblems.
func (m model) saveOrg() (model, tea.Cmd) {
	f := &m.orgForm
	switch {
	case f.probing || !f.probeDone:
		f.refusal = "probing tool auth… — saving unlocks when the probe lands"
		return m, nil
	case f.provider == "" || !authedHas(f.authed, f.provider, f.host):
		// §11.1: no auth, no add. Editing an existing registration's other
		// fields stays possible; a new (or re-hosted) one needs a live CLI.
		orig := f.editing >= 0 && f.editing < len(m.cfg.Orgs)
		unchanged := orig && m.cfg.Orgs[f.editing].ResolvedProvider() == f.provider &&
			strings.EqualFold(m.cfg.Orgs[f.editing].Host, f.host)
		if f.provider == "" || !unchanged {
			if len(f.authed) == 0 {
				f.refusal = noAuthRefusal()
			} else if f.provider == "" {
				f.refusal = "no provider selected — cycle the provider row to an authenticated one"
			} else {
				f.refusal = f.provider + " is not authenticated on " + f.host + " — run " + loginCommands[f.provider]
			}
			return m, nil
		}
	}

	org := f.orgValue()
	cand := m.cfg
	if f.editing >= 0 && f.editing < len(cand.Orgs) {
		cand.Orgs[f.editing] = org
	} else {
		cand.Orgs = append(cand.Orgs, org)
	}
	autoRoot(&cand, org)

	// The gate: OrgProblems refuses the save, the overlay renders why. This
	// org's problems lead, so one typo is not buried under pre-existing
	// complaints from elsewhere in the file.
	probs := cand.OrgProblems()
	if len(probs) > 0 {
		mine := "orgs[" + itoa(f.editing) + "]"
		var relevant, rest []string
		for _, p := range probs {
			if strings.Contains(p, "orgs[") && !strings.Contains(p, mine) {
				rest = append(rest, p)
			} else {
				relevant = append(relevant, p)
			}
		}
		f.refusal = strings.Join(append(relevant, rest...), "; ")
		return m, nil
	}

	note := "org saved: " + org.Owner + " on " + org.Host
	if f.editing >= 0 {
		note = "org updated: " + org.Owner + " on " + org.Host
	}
	f.refusal = ""
	return m, writeOrgConfig(cand, note, true)
}

// removeOrg arms and fires the two-press remove (§12): the first x arms the
// confirm, the second removes the registration. Checkouts are never touched
// — §11.3: nothing is ever deleted; the repos simply fall out of sync and
// report as orphans should the org come back.
func (m model) removeOrg() (model, tea.Cmd) {
	if !m.orgConfirm {
		m.orgConfirm = true
		return m, nil
	}
	m.orgConfirm = false
	i := m.orgSel
	if i < 0 || i >= len(m.cfg.Orgs) {
		return m, nil
	}
	org := m.cfg.Orgs[i]
	cand := m.cfg
	cand.Orgs = append(cand.Orgs[:i:i], cand.Orgs[i+1:]...)
	return m, writeOrgConfig(cand, "org removed: "+org.Owner+" on "+org.Host+" (checkouts left untouched)", false)
}

// syncSelected syncs the org under the cursor (§12 s). Disabled
// registrations are skipped: enabled is the switch §11.3 syncs honor.
func (m model) syncSelected() (model, tea.Cmd) {
	if m.syncRunning {
		m.notify("org sync already running")
		return m, nil
	}
	i := m.orgSel
	if i < 0 || i >= len(m.cfg.Orgs) {
		return m, nil
	}
	org := m.cfg.Orgs[i]
	if !org.Enabled {
		m.notify("org %s is disabled — e to edit", org.Owner)
		return m, nil
	}
	return m.startOrgSync([]int{i})
}

// syncAllEnabled syncs every live registration in one serial pass (§12 S).
func (m model) syncAllEnabled() (model, tea.Cmd) {
	if m.syncRunning {
		m.notify("org sync already running")
		return m, nil
	}
	var idxs []int
	for i, o := range m.cfg.Orgs {
		if o.Enabled {
			idxs = append(idxs, i)
		}
	}
	if len(idxs) == 0 {
		m.notify("no enabled orgs to sync")
		return m, nil
	}
	return m.startOrgSync(idxs)
}

// orgSyncRowMsg streams one report row into the progress line (§11.5).
type orgSyncRowMsg struct {
	key    string
	name   string
	action string
	detail string
	done   int
	total  int
}

// orgSyncDoneMsg closes the sync: the summary lands on the status line, the
// synced registrations earn their last-sync stamp, and the final sweep makes
// fresh clones appear immediately (§11.3).
type orgSyncDoneMsg struct {
	keys     []string
	outcomes []orgsync.Outcome
}

// startOrgSync runs listing → plan → Execute for each org, serially, on a
// command goroutine. Progress reaches the UI through the engine's message
// sink because a command returns exactly one message and a sync streams
// many; the engine drops them silently when no program is wired (tests).
func (m model) startOrgSync(idxs []int) (model, tea.Cmd) {
	cfg := m.cfg
	eng := m.engine
	timeout := cfgDuration(cfg.Remote.Timeout, 20*time.Second)

	keys := make([]string, 0, len(idxs))
	for _, i := range idxs {
		keys = append(keys, orgKey(cfg.Orgs[i]))
	}

	m.syncRunning = true
	if len(idxs) == 1 {
		m.syncOrg = cfg.Orgs[idxs[0]].Owner + " on " + cfg.Orgs[idxs[0]].Host
	} else {
		m.syncOrg = itoa(len(idxs)) + " orgs"
	}
	m.syncProgress = "starting…"
	m.notify("org sync: starting…")
	cmd := func() tea.Msg {
		send := func(msg any) {
			if eng != nil {
				eng.sendMsg(msg)
			}
		}
		if eng != nil {
			// §13: while a sync runs, watcher events are dropped wholesale —
			// the sync's final sweep re-covers whatever moved.
			eng.SetSyncActive(true)
			defer eng.SetSyncActive(false)
		}
		var all []orgsync.Outcome
		for _, i := range idxs {
			org := cfg.Orgs[i]
			key := orgKey(org)
			src := orgSource(cfg, org)
			opts := orgsync.Opts{Path: src.Path, Timeout: timeout}

			plan := orgsync.ListSync(src, opts, orgDiskRepos, orgListFn)
			for _, row := range plan.Rows {
				send(orgSyncRowMsg{key: key, name: row.Name, action: row.Action, detail: row.Detail})
			}
			done := len(plan.Rows)
			total := done + len(plan.Plan.ToClone) + len(plan.Plan.ToUpdate)
			outcomes := orgsync.Execute(src, plan.Plan, opts, func(d, t int, label string) {
				send(orgSyncRowMsg{key: key, name: label, done: d, total: total})
			})
			for _, o := range outcomes {
				send(orgSyncRowMsg{key: key, name: o.Name, action: o.Action, detail: o.Detail})
			}
			all = append(all, outcomes...)
		}
		send(orgSyncDoneMsg{keys: keys, outcomes: all})
		return nil
	}
	return m, cmd
}

// orgSource builds the sync-facing slice of a registration.
func orgSource(cfg config.Config, o config.OrgConfig) orgsync.Source {
	protocol := o.Protocol
	if protocol == "" {
		protocol = "ssh" // the primed default (§4): entries never lose it, but belt and braces
	}
	return orgsync.Source{
		Provider:         o.ResolvedProvider(),
		Host:             o.Host,
		Owner:            o.Owner,
		Path:             o.ResolvedPath(cfg),
		Login:            o.Login,
		Protocol:         protocol,
		IncludeForks:     o.IncludeForks,
		IncludeArchived:  o.IncludeArchived,
		IncludeSubgroups: o.IncludeSubgroups,
		Exclude:          o.Exclude,
	}
}

// orgDiskRepos lists the checkout names under an org path, the disk half of
// the plan (§11.3). A checkout is a directory holding .git (file or
// directory — worktrees and submodules included) or a bare repo's HEAD+refs;
// anything else is somebody's plain directory and is none of sync's business.
func orgDiskRepos(path string) []string {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(path, e.Name())
		if isCheckoutDir(dir) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

func isCheckoutDir(dir string) bool {
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
		return true
	}
	// Bare: HEAD plus refs (§5 — bare repos count as repos).
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		return false
	}
	fi, err := os.Stat(filepath.Join(dir, "refs"))
	return err == nil && fi.IsDir()
}

// setConfig hands a validated, written config to the background pipeline so
// the next sweep walks the org's new root (§11.4). The write itself happens
// off-thread; the UI thread only swaps the snapshot in after the disk
// agreed.
func (e *engine) setConfig(cfg config.Config) {
	e.mu.Lock()
	e.cfg = cfg
	e.mu.Unlock()
}

// cfgSnapshot reads the engine's config under the lock. runSweep takes its
// snapshot at the top of a sweep; anything reading engine config mid-flight
// should do the same.
func (e *engine) cfgSnapshot() config.Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg
}

// keyOrgs is the org manager list keymap (§12): j/k move, a add, e edit,
// x x remove, s sync selected, S sync all enabled, ? help, esc close. Any
// other key disarms a pending remove confirm — a confirm that survives
// unrelated typing is how the wrong org disappears.
func (m model) keyOrgs(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case anyKey(key, "esc", "q", "A"):
		m.mode = modeTable
		m.orgConfirm = false
	case anyKey(key, "j", "down"):
		m.orgMove(1)
	case anyKey(key, "k", "up"):
		m.orgMove(-1)
	case keyIs(key, "a"):
		m.mode = modeOrgForm
		m.orgForm = newOrgForm(m.cfg, -1)
		return m, startOrgProbe(m.cfg)
	case keyIs(key, "e"):
		if m.orgSel >= 0 && m.orgSel < len(m.cfg.Orgs) {
			m.mode = modeOrgForm
			m.orgForm = newOrgForm(m.cfg, m.orgSel)
			return m, startOrgProbe(m.cfg)
		}
	case keyIs(key, "x"):
		return m.removeOrg()
	case keyIs(key, "s"):
		return m.syncSelected()
	case keyIs(key, "S"):
		return m.syncAllEnabled()
	case keyIs(key, "?"):
		m.mode = modeHelp
		m.helpOff = 0
	default:
		m.orgConfirm = false
	}
	return m, nil
}

// orgMove shifts the org list cursor, clamped to the registrations.
func (m *model) orgMove(delta int) {
	m.orgConfirm = false
	m.orgSel += delta
	if m.orgSel < 0 {
		m.orgSel = 0
	}
	if m.orgSel > len(m.cfg.Orgs)-1 {
		m.orgSel = len(m.cfg.Orgs) - 1
	}
}

// keyOrgForm edits the add/edit form. Until the probe lands the form is a
// single esc away from backing out and nothing else — §12: rows cycle only
// through authenticated options, and the probe decides what is authenticated.
func (m model) keyOrgForm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := &m.orgForm
	if keyIs(key, "esc") {
		m.mode = modeOrgs
		return m, nil
	}
	if f.probing || !f.probeDone {
		return m, nil
	}
	rows := f.rows()
	if f.cursor >= len(rows) {
		f.cursor = len(rows) - 1
	}
	row := rows[f.cursor]

	switch {
	case keyIs(key, "ctrl-s"):
		return m.saveOrg()
	case anyKey(key, "j", "down"):
		f.cursor = (f.cursor + 1) % len(rows)
	case anyKey(key, "k", "up"):
		f.cursor = (f.cursor - 1 + len(rows)) % len(rows)
	case key.Type == tea.KeyEnter, keyIs(key, " "), keyIs(key, "l"), keyIs(key, "right"):
		return m.formActivate(row)
	case anyKey(key, "h", "left"):
		return m.formCycle(row, -1)
	case key.Type == tea.KeyBackspace:
		m.formTypeInto(row, "")
	default:
		if key.Type == tea.KeyRunes || key.Type == tea.KeySpace {
			m.formTypeInto(row, keyString(key))
		}
	}
	return m, nil
}

// formActivate answers enter/space/l on the cursor row: cycles through the
// authenticated options, opens the owner picker, flips a toggle, or saves.
func (m model) formActivate(row formRow) (model, tea.Cmd) {
	f := &m.orgForm
	switch row.id {
	case "provider":
		m.formCycleProvider(1)
	case "host":
		m.formCycleHost(1)
	case "owner":
		m.mode = modeOwners
		f.ownerFilter = ""
		f.ownerCursor = 0
		f.owners = nil
		f.ownersLoad = true
		return m, m.fetchOwners()
	case "protocol":
		f.flipProtocol()
	case "forks":
		f.includeForks = !f.includeForks
	case "archived":
		f.includeArchived = !f.includeArchived
	case "subgroups":
		f.includeSubgroups = !f.includeSubgroups
	case "save":
		return m.saveOrg()
	case "cancel":
		m.mode = modeOrgs
	}
	return m, nil
}

// formCycle answers h/left — the backward direction of every cycle.
func (m model) formCycle(row formRow, dir int) (model, tea.Cmd) {
	f := &m.orgForm
	switch row.id {
	case "provider":
		m.formCycleProvider(dir)
	case "host":
		m.formCycleHost(dir)
	case "protocol":
		f.flipProtocol()
	}
	return m, nil
}

// flipProtocol cycles the clone protocol (§4: ssh | https).
func (f *orgForm) flipProtocol() {
	if f.protocol == "ssh" {
		f.protocol = "https"
	} else {
		f.protocol = "ssh"
	}
}

// formCycleProvider steps the provider row through the authenticated
// providers only (§11.1) and re-pins the host to the new provider's first
// authenticated host, because a host belongs to exactly one provider.
func (m *model) formCycleProvider(dir int) {
	f := &m.orgForm
	if len(f.authed) == 0 {
		return
	}
	idx := 0
	for i, a := range f.authed {
		if a.Provider == f.provider {
			idx = i
			break
		}
	}
	idx = (idx + dir + len(f.authed)) % len(f.authed)
	f.provider = f.authed[idx].Provider
	if len(f.authed[idx].Hosts) > 0 {
		f.host = f.authed[idx].Hosts[0]
	}
}

// formCycleHost steps the host row through the current provider's
// authenticated hosts.
func (m *model) formCycleHost(dir int) {
	f := &m.orgForm
	for _, a := range f.authed {
		if a.Provider != f.provider || len(a.Hosts) == 0 {
			continue
		}
		idx := 0
		for i, h := range a.Hosts {
			if strings.EqualFold(h, f.host) {
				idx = i
				break
			}
		}
		f.host = a.Hosts[(idx+dir+len(a.Hosts))%len(a.Hosts)]
		return
	}
}

// formTypeInto edits the cursor row's text. Owner edits re-apply the
// resolved default path until the user touches the path row (§11.4); a
// path edit retires the auto-fill for good. Backspace arrives here as an
// empty edit.
func (m *model) formTypeInto(row formRow, text string) {
	f := &m.orgForm
	switch row.id {
	case "owner":
		if text == "" {
			r := []rune(f.owner)
			if len(r) > 0 {
				f.owner = string(r[:len(r)-1])
			}
		} else {
			f.owner += text
		}
		f.autoFillPath(m.cfg)
	case "path":
		f.pathTouched = true
		if text == "" {
			r := []rune(f.path)
			if len(r) > 0 {
				f.path = string(r[:len(r)-1])
			}
		} else {
			f.path += text
		}
	case "login":
		if text == "" {
			r := []rune(f.login)
			if len(r) > 0 {
				f.login = string(r[:len(r)-1])
			}
		} else {
			f.login += text
		}
	}
}

// fetchOwners launches the owner listing inside a command (§11.1: the
// owner is a pick list fetched from the provider's API).
func (m model) fetchOwners() tea.Cmd {
	f := m.orgForm
	timeout := probeTimeoutFor(m.cfg)
	provider, host, login := f.provider, f.host, f.login
	return func() tea.Msg {
		return orgOwnersMsg{owners: listOwners(timeout, provider, host, login)}
	}
}

// keyOwners is the owner picker keymap: j/k move, enter picks the narrowed
// list's cursor — or the typed text itself, which is the free-typing
// fallback §11.1 keeps open for memberships the API doesn't expose.
func (m model) keyOwners(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := &m.orgForm
	switch {
	case keyIs(key, "esc"):
		m.mode = modeOrgForm
	case key.Type == tea.KeyEnter:
		filtered := f.filteredOwners()
		switch {
		case len(filtered) > 0 && f.ownerCursor < len(filtered):
			f.owner = filtered[f.ownerCursor]
		case strings.TrimSpace(f.ownerFilter) != "":
			f.owner = strings.TrimSpace(f.ownerFilter)
		}
		if f.owner != "" {
			f.autoFillPath(m.cfg)
			m.mode = modeOrgForm
		}
	case anyKey(key, "j", "down"):
		if n := len(f.filteredOwners()); n > 0 {
			f.ownerCursor = (f.ownerCursor + 1) % n
		}
	case anyKey(key, "k", "up"):
		if n := len(f.filteredOwners()); n > 0 {
			f.ownerCursor = (f.ownerCursor - 1 + n) % n
		}
	case key.Type == tea.KeyBackspace:
		r := []rune(f.ownerFilter)
		if len(r) > 0 {
			f.ownerFilter = string(r[:len(r)-1])
		}
		f.ownerCursor = 0
	case key.Type == tea.KeyRunes || key.Type == tea.KeySpace:
		f.ownerFilter += keyString(key)
		f.ownerCursor = 0
	}
	return m, nil
}
