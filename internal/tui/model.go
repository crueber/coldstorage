// The dashboard's model (GO-PORT-SPEC.md §8, §12): one RepoState per repo —
// the single structure every view renders — plus the view state (selection,
// filters, sort, overlays) and the transient status machinery. Everything
// derived for rendering is computed by pure functions in table.go, view.go,
// and detail.go so the table, the detail pane, and future views can never
// disagree about what a repo's row means.
package tui

import (
	"fmt"
	"sort"
	"time"

	"github.com/crueber/coldstorage/internal/config"
	"github.com/crueber/coldstorage/internal/gitmode"
)

// RepoState is one repo's rendered state: the §8 data model as the TUI holds
// it. Refs and Work survive a failed probe with their last good values; Err
// carries the probe error and is checked before every other verdict.
type RepoState struct {
	Root  string
	Group string
	Name  string

	Refs        gitmode.RefsInfo
	Work        *gitmode.WorkInfo
	WorkKey     gitmode.WorkKey
	Fingerprint uint64
	Err         error

	RefsProbedAt time.Time
	WorkProbedAt time.Time
	Visibility   *gitmode.VisibilityInfo
}

// Slug is the group/name identity the JSON contract and logs use.
func (r RepoState) Slug() string {
	if r.Group == "" {
		return r.Name
	}
	return r.Group + "/" + r.Name
}

// State renders the one-word state with the §8 precedence: error first (the
// error lives outside RefsInfo/WorkInfo, so it is checked here, in the
// caller — exactly as gitmode.StateLabel's doc requires), then the rest.
func (r RepoState) State() string {
	if r.Err != nil {
		return "error"
	}
	return gitmode.StateLabel(r.Refs, r.Work)
}

// Release places the repo against its own release history (§8).
func (r RepoState) Release() gitmode.ReleaseState {
	return gitmode.ReleaseStateOf(r.Refs, r.Work)
}

// Activity is the repo's activity timestamp per §7.6: the newest commit, or
// the newest working-file mtime for a dirty repo — never the index mtime,
// which any `git status` refreshes.
func (r RepoState) Activity() time.Time {
	newest := r.Refs.NewestCommitAt()
	if r.Work != nil && r.Work.Dirty() && r.Work.NewestMtime.After(newest) {
		return r.Work.NewestMtime
	}
	return newest
}

// ActivitySource explains where Activity came from (§10).
func (r RepoState) ActivitySource() gitmode.ActivitySource {
	newest := r.Refs.NewestCommitAt()
	if r.Work != nil && r.Work.Dirty() && r.Work.NewestMtime.After(newest) {
		return gitmode.ActivityWorkingTree
	}
	if newest.IsZero() {
		return gitmode.ActivityUnknown
	}
	return gitmode.ActivityCommit
}

// mode is which full-screen surface the dashboard shows (§12 modes).
type mode int

const (
	modeTable mode = iota
	modeDetail
	modeHelp
	modeColumns
	modeOrgs
	modeOrgForm
	modeOwners
)

// agePresets are the 0-4 age presets: activity no older than the duration.
// Zero clears the preset.
var agePresets = []struct {
	label string
	dur   time.Duration
}{
	{"any", 0},
	{"1h", time.Hour},
	{"24h", 24 * time.Hour},
	{"1w", 7 * 24 * time.Hour},
	{"1mo", 30 * 24 * time.Hour},
}

// ageIdxFor maps a default_since duration to the closest preset.
func ageIdxFor(d time.Duration) int {
	for i, p := range agePresets {
		if p.dur >= d {
			return i
		}
	}
	return 0
}

// statusTTL is how long a transient notification stays on the status line
// before the tick reclaims it (§12).
const statusTTL = 6 * time.Second

// model is the Bubble Tea model. It holds no locks: the engine owns all
// concurrency, and the model only ever mutates on the UI thread.
type model struct {
	cfg      config.Config
	cacheDir string
	engine   *engine

	// Fleet state: rows by repo root. Pruned by sweeps when discovery
	// drops deleted repos (§5).
	rows map[string]RepoState

	// View state.
	width, height int
	mode          mode
	sel           int // index into the visible rows
	offset        int // first visible table row
	detailOff     int // first visible detail line
	helpOff       int

	// Filters, sort, search (§12 keymap).
	filterKinds map[string]bool
	matchAll    bool
	ageIdx      int // index into agePresets, 0 = any
	group       string
	orgFilter   string // orgKey of the filtered registration; "" = all orgs
	groups      []string
	sortKey     string
	sortReverse bool
	search      string
	searching   bool
	searchBuf   string
	colCursor   int // column picker cursor
	cols        columnSet

	// Status line: transient message with TTL, sweep progress, spinner.
	status     string
	statusAt   time.Time
	sweeping   bool
	sweepTotal int
	swept      int

	// Org manager state (§12 org manager): the list cursor and remove
	// confirm, the form (with its probe results), and the sync machinery.
	// Last-sync stamps are session memory — orgsync keeps no persistent
	// journal, so a registration syncs "never" until it syncs here.
	orgSel      int
	orgConfirm  bool
	orgForm     orgForm
	orgLastSync map[string]time.Time
	syncRunning bool
	spinnerIdx  int
	warnings    []string
	lastSweep   time.Time

	// The background operation widget (§12): what the queue is doing right
	// now, rendered in the header's upper right only while it is doing it.
	// The sweep and the org sync are the two operations a fleet runs; the
	// sync's per-row chatter lands here instead of the status line, which
	// is what made a sync look like the dashboard was blinking.
	syncOrg      string
	syncProgress string

	// Detail commit history (§9): the selected repo's subjects, paged in
	// from gitmode.Log as the owner scrolls.
	histRoot    string
	histCommits []gitmode.Commit
	histLoading bool
	histDone    bool
	histErr     error
}

// newModel assembles the dashboard: cached rows paint the first frame
// before any probe has run (§15), and the config's default filters, sort,
// and since preset (§4) set the initial view.
func newModel(cfg config.Config, cacheDir string, cached map[string]RepoState, warnings []string, eng *engine) model {
	m := model{
		cfg:         cfg,
		cacheDir:    cacheDir,
		engine:      eng,
		rows:        map[string]RepoState{},
		filterKinds: map[string]bool{},
		sortKey:     "activity",
		cols:        defaultColumns(),
		warnings:    warnings,
		lastSweep:   time.Now(),
		orgLastSync: map[string]time.Time{},
	}
	for _, r := range cached {
		m.rows[r.Root] = r
	}
	m.rebuildGroups()
	for _, f := range cfg.UI.DefaultFilters {
		if knownFilter(f) {
			m.filterKinds[f] = true
		}
	}
	if validSort(cfg.UI.DefaultSort) {
		m.sortKey = cfg.UI.DefaultSort
	}
	if cfg.UI.DefaultSince != "" {
		if d, err := config.ParseDuration(cfg.UI.DefaultSince); err == nil {
			m.ageIdx = ageIdxFor(d)
		}
	}
	return m
}

// rebuildGroups recomputes the group cycle list ([ and ]) from the rows.
func (m *model) rebuildGroups() {
	seen := map[string]bool{}
	var groups []string
	for _, r := range m.rows {
		if r.Group != "" && !seen[r.Group] {
			seen[r.Group] = true
			groups = append(groups, r.Group)
		}
	}
	sort.Strings(groups)
	m.groups = groups
	if m.group != "" && !seen[m.group] {
		m.group = "" // the filtered group vanished from the fleet
	}
}

// notify sets a transient status message with the §12 TTL.
func (m *model) notify(format string, args ...any) {
	m.status = fmt.Sprintf(format, args...)
}
func (m model) busy() bool {
	return m.sweeping || m.syncRunning || (m.engine != nil && m.engine.Busy())
}
