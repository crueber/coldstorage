// The event loop (GO-PORT-SPEC.md §12 preemption contract, §14). Bubble Tea
// delivers key messages from its input reader and everything else from
// commands; Update's dispatch order encodes the priority contract: keys are
// answered first, background batches are merged in memory (microseconds),
// and repaints are driven by ticks — 250ms while work is in flight, about
// 1s idle — plus an immediate repaint for user input. No key handler spawns
// processes, touches the network, or walks the filesystem synchronously:
// they only launch engine goroutines.
package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/crueber/coldstorage/internal/config"
)

// tickMsg drives the repaint cadence. The interval is chosen per tick:
// 250ms while a sweep or probes run, ~1s idle.
type tickMsg struct{ at time.Time }

// probeBatchMsg carries a bounded batch of probe results (§14).
type probeBatchMsg struct{ repos []RepoState }

// sweepPhaseMsg announces sweep progress: discovering, discovered, probing.
type sweepPhaseMsg struct {
	phase string
	total int
}

// sweepDoneMsg closes a sweep and carries the discovered roots, so the model
// can drop repos deleted from disk (§5).
type sweepDoneMsg struct {
	roots []string
}

// warnMsg surfaces a watcher or cache warning on the status line (§17).
type warnMsg struct{ err error }

// tick schedules the next repaint at the cadence the §12 contract fixes.
func (m model) tick() tea.Cmd {
	interval := time.Second
	if m.busy() {
		interval = 250 * time.Millisecond
	}
	return tea.Tick(interval, func(t time.Time) tea.Msg { return tickMsg{at: t} })
}

// startSweep launches the engine's sweep without ever blocking Update.
func (m model) startSweep(force bool) tea.Cmd {
	return func() tea.Msg {
		if m.engine != nil {
			if force {
				m.engine.Sweep(true)
			} else {
				m.engine.Start()
			}
		}
		return nil
	}
}

// Init paints from cache and kicks off the first sweep and the tick loop.
func (m model) Init() tea.Cmd {
	return tea.Batch(m.tick(), m.startSweep(false))
}

// Update is the single entry point for every message.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// User input first, every iteration (§12 preemption contract).
	if key, ok := msg.(tea.KeyMsg); ok {
		return m.handleKey(key)
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case tickMsg:
		// Expire the transient notification (§12 TTL), advance the spinner
		// only while work is in flight, and resweep on the refresh
		// interval (§4 refresh.interval).
		if m.status != "" && time.Since(m.statusAt) >= statusTTL {
			m.status = ""
		}
		if m.busy() {
			m.spinnerIdx++
		}
		interval := cfgDuration(m.cfg.Refresh.Interval, 5*time.Minute)
		if !m.busy() && time.Since(m.lastSweep) >= interval {
			m.lastSweep = time.Now()
			return m, tea.Batch(m.tick(), m.startSweep(false))
		}
		return m, m.tick()

	case probeBatchMsg:
		m.applyRows(msg.repos)
		m.swept += len(msg.repos)

	case sweepPhaseMsg:
		switch msg.phase {
		case "discovering":
			m.sweeping = true
			m.notify("sweep: discovering…")
		case "discovered":
			m.sweepTotal = msg.total
		case "probing":
			m.sweeping = true
			m.swept = 0
			m.sweepTotal = msg.total
		}

	case sweepDoneMsg:
		m.sweeping = false
		m.lastSweep = time.Now()
		m.pruneRows(msg.roots)
		m.rebuildGroups()
	case warnMsg:
		m.notify("warning: %v", msg.err)

	// Org manager messages (§12 org manager). All of them arrive from
	// command goroutines — the probe, the owner fetch, the sync engine, and
	// the config write are network or disk work the UI thread never waits on.
	case orgAuthMsg:
		f := &m.orgForm
		f.probing = false
		f.probeDone = true
		f.authed = msg.authed
		// A fresh form preselects the first authenticated provider/host, so
		// the common case is one enter away from saving (§11.1).
		if f.editing < 0 && f.provider == "" && len(msg.authed) > 0 {
			f.provider = msg.authed[0].Provider
			if len(msg.authed[0].Hosts) > 0 {
				f.host = msg.authed[0].Hosts[0]
			}
			f.autoFillPath(m.cfg)
		}

	case orgOwnersMsg:
		m.orgForm.ownersLoad = false
		m.orgForm.owners = msg.owners
		m.orgForm.ownerCursor = 0

	case orgSyncRowMsg:
		// §11.5: report rows stream into the progress line.
		if msg.action != "" {
			m.notify("org sync: %s %s %s", msg.action, msg.name, msg.detail)
		} else if msg.total > 0 {
			m.notify("org sync: %s (%d/%d)", msg.name, msg.done, msg.total)
		} else {
			m.notify("org sync: %s", msg.name)
		}

	case orgSyncDoneMsg:
		m.syncRunning = false
		now := time.Now()
		for _, k := range msg.keys {
			m.orgLastSync[k] = now
		}
		counts := map[string]int{}
		for _, o := range msg.outcomes {
			counts[o.Action]++
		}
		m.notify("org sync done: %d cloned, %d updated, %d current, %d skipped, %d orphaned, %d errors",
			counts["cloned"], counts["updated"], counts["current"], counts["skipped"], counts["orphaned"], counts["error"])
		// §11.3: a full sweep runs after sync so fresh clones appear
		// immediately; the fingerprint gate keeps it cheap.
		return m, m.startSweep(false)

	case orgSavedMsg:
		if msg.err != nil {
			m.notify("config: %v", msg.err)
			if msg.close {
				// The form stays open with the failure rendered in-overlay:
				// a lost edit is how an owner stops trusting the save key.
				m.orgForm.refusal = "config: " + msg.err.Error()
			}
			return m, nil
		}
		m.cfg = msg.cfg
		if m.engine != nil {
			m.engine.setConfig(msg.cfg)
		}
		if msg.close {
			m.mode = modeOrgs
			m.orgForm = orgForm{}
		} else {
			// A removal shrank the list; keep the cursor on the list.
			if m.orgSel >= len(m.cfg.Orgs) {
				m.orgSel = len(m.cfg.Orgs) - 1
			}
		}
		m.orgConfirm = false
		m.notify("%s", msg.note)
	}

	return m, nil
}

// applyRows merges one batch of probe results. Map-upsert only: rows the
// batch doesn't mention are untouched, so a filtered dashboard keeps its
// state between batches.
func (m *model) applyRows(repos []RepoState) {
	for _, r := range repos {
		m.rows[r.Root] = r
	}
	m.rebuildGroups()
}

// pruneRows drops repos the latest discovery no longer sees (§5: repos
// deleted from disk leave the dashboard).
func (m *model) pruneRows(roots []string) {
	keep := make(map[string]bool, len(roots))
	for _, r := range roots {
		keep[r] = true
	}
	for root := range m.rows {
		if !keep[root] {
			delete(m.rows, root)
		}
	}
}

func cfgDuration(s string, def time.Duration) time.Duration {
	if d, err := config.ParseDuration(s); err == nil {
		return d
	}
	return def
}

// handleKey routes a key by mode. The table keeps its full §12 keymap; the
// overlays take the few keys they need and hand the rest back.
func (m model) handleKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeHelp:
		return m.keyHelp(key)
	case modeColumns:
		return m.keyColumns(key)
	case modeDetail:
		return m.keyDetail(key)
	case modeOrgs:
		return m.keyOrgs(key)
	case modeOrgForm:
		return m.keyOrgForm(key)
	case modeOwners:
		return m.keyOwners(key)
	default:
		return m.keyTable(key)
	}
}

// keyString renders a KeyMsg as the key name the keymap speaks.
func keyString(k tea.KeyMsg) string {
	if k.Type == tea.KeyRunes {
		return string(k.Runes)
	}
	return k.String()
}

func keyIs(k tea.KeyMsg, name string) bool { return keyString(k) == name }

// anyKey reports whether the key matches any of the names.
func anyKey(key tea.KeyMsg, names ...string) bool {
	for _, n := range names {
		if keyIs(key, n) {
			return true
		}
	}
	return false
}

func (m model) keyHelp(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if anyKey(key, "esc", "?", "q") {
		m.mode = modeTable
	}
	return m, nil
}

func (m model) keyColumns(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case anyKey(key, "esc", "C", "q"):
		m.mode = modeTable
	case anyKey(key, "j", "down"):
		m.colCursor = (m.colCursor + 1) % len(columnCatalog)
	case anyKey(key, "k", "up"):
		m.colCursor = (m.colCursor - 1 + len(columnCatalog)) % len(columnCatalog)
	case anyKey(key, " ", "enter"):
		col := columnCatalog[m.colCursor]
		if col.optional {
			if m.cols.on[col.id] {
				delete(m.cols.on, col.id)
			} else {
				m.cols.on[col.id] = true
			}
		}
	}
	return m, nil
}

func (m model) keyDetail(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	rowsHeight := m.height - frameLines
	switch {
	case anyKey(key, "esc", "enter", "q"):
		m.mode = modeTable
		m.detailOff = 0
	case anyKey(key, "j", "down"):
		m.detailOff++
	case anyKey(key, "k", "up"):
		m.detailOff -= 2
		if m.detailOff < 0 {
			m.detailOff = 0
		}
	case key.Type == tea.KeyPgDown || keyIs(key, "ctrl-d"):
		m.detailOff += maxInt(1, rowsHeight)
	case key.Type == tea.KeyPgUp || keyIs(key, "ctrl-u"):
		m.detailOff -= maxInt(1, rowsHeight)
		if m.detailOff < 0 {
			m.detailOff = 0
		}
	}
	return m, nil
}

// keyTable is the §12 table keymap.
func (m model) keyTable(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	// The search prompt swallows everything except commit/cancel.
	if m.searching {
		return m.keySearch(key)
	}

	switch {
	case key.Type == tea.KeyCtrlC || keyIs(key, "q"):
		return m, tea.Quit

	case keyIs(key, "?"):
		m.mode = modeHelp
		m.helpOff = 0

	case keyIs(key, "C"):
		m.mode = modeColumns
		m.colCursor = 0

	case key.Type == tea.KeyEnter:
		m.mode = modeDetail
		m.detailOff = 0
	case keyIs(key, "A"):
		// §12: the org manager overlay. The cursor keeps its position
		// between visits so a sync of the same org is one key away.
		if m.orgSel >= len(m.cfg.Orgs) {
			m.orgSel = len(m.cfg.Orgs) - 1
		}
		m.mode = modeOrgs
		m.orgConfirm = false

	case anyKey(key, "j", "down"):
		m.move(1)
	case anyKey(key, "k", "up"):
		m.move(-1)
	case keyIs(key, "ctrl-d"):
		m.move(m.page() / 2)
	case keyIs(key, "ctrl-u"):
		m.move(-m.page() / 2)
	case key.Type == tea.KeyPgDown:
		m.move(m.page())
	case key.Type == tea.KeyPgUp:
		m.move(-m.page())
	case anyKey(key, "end") || key.Type == tea.KeyEnd:
		m.move(1 << 30)
	case anyKey(key, "home") || key.Type == tea.KeyHome:
		m.move(-(1 << 30))

	// Filters.
	case keyIs(key, "a"):
		m.filterKinds = map[string]bool{}
		m.matchAll = false
		m.ageIdx = 0
		m.group = ""
		m.search = ""
		m.sel, m.offset = 0, 0
	case keyIs(key, "&"):
		m.matchAll = !m.matchAll
	case keyIs(key, "["), keyIs(key, "]"):
		m.cycleGroup(keyIs(key, "]"))
	case filterByKey(keyString(key)) != "":
		// The §12 filter toggles: d u r N b c i x e n.
		kind := filterByKey(keyString(key))
		if m.filterKinds[kind] {
			delete(m.filterKinds, kind)
		} else {
			m.filterKinds[kind] = true
		}
		m.sel, m.offset = 0, 0
	case anyKey(key, "0", "1", "2", "3", "4"):
		m.ageIdx = int(key.Runes[0] - '0')

	// Sort and search.
	case keyIs(key, "s"):
		idx := 0
		for i, k := range sortKeys {
			if k == m.sortKey {
				idx = (i + 1) % len(sortKeys)
				break
			}
		}
		m.sortKey = sortKeys[idx]
	case keyIs(key, "S"):
		m.sortReverse = !m.sortReverse
	case keyIs(key, "/"):
		m.searching = true
		m.searchBuf = m.search

	// Fleet.
	case keyIs(key, "R"), keyIs(key, "ctrl-r"):
		m.lastSweep = time.Now()
		m.notify("rescanning…")
		return m, m.startSweep(true)
	}
	return m, nil
}

// keySearch edits the search buffer live; the table re-filters as it types.
func (m model) keySearch(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case keyIs(key, "esc"):
		m.searching = false
		m.searchBuf = m.search // cancel restores the committed query
	case key.Type == tea.KeyEnter:
		m.searching = false
		m.search = m.searchBuf
		m.sel, m.offset = 0, 0
	case key.Type == tea.KeyBackspace:
		r := []rune(m.searchBuf)
		if len(r) > 0 {
			m.searchBuf = string(r[:len(r)-1])
		}
	case key.Type == tea.KeyRunes || key.Type == tea.KeySpace:
		m.searchBuf += keyString(key)
	}
	return m, nil
}

// page is the visible row count, for paging.
func (m model) page() int {
	h := m.height - frameLines
	if h < 1 {
		return 1
	}
	return h
}

// move shifts the selection by delta, clamped to the visible rows.
func (m *model) move(delta int) {
	n := len(m.visibleRows())
	if n == 0 {
		m.sel, m.offset = 0, 0
		return
	}
	m.sel += delta
	if m.sel < 0 {
		m.sel = 0
	}
	if m.sel > n-1 {
		m.sel = n - 1
	}
	m.ensureOffset(n, m.page())
}

// cycleGroup steps through the fleet's groups (§12 [ ]). Past the last
// group the filter clears to "all".
func (m *model) cycleGroup(forward bool) {
	if len(m.groups) == 0 {
		m.group = ""
		return
	}
	idx := -1
	for i, g := range m.groups {
		if g == m.group {
			idx = i
			break
		}
	}
	if forward {
		idx++
	} else {
		idx--
	}
	if idx < 0 {
		idx = len(m.groups)
	}
	if idx > len(m.groups) {
		idx = 0
	}
	if idx == len(m.groups) {
		m.group = ""
	} else {
		m.group = m.groups[idx]
	}
	m.sel, m.offset = 0, 0
}
