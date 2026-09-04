// The dashboard's paint (GO-PORT-SPEC.md §12): a header with the fleet
// counts, the spinner and the status message; the table; a footer of key
// hints; and a status line. Rendering is a pure projection of the model —
// View never mutates, never blocks, and only ever touches rows the filters
// already selected, so a 1,000-repo fleet paints in the time it takes to
// draw one screen of rows.
package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// frameLines is the chrome the table shares the screen with: three header
// lines (title, status, column caption) and two footer lines. The count
// MUST equal every non-data line the table view writes — it once omitted
// the column caption, making the frame one line taller than the terminal,
// and the renderer (which keeps the LAST height lines) dropped the header
// and with it every fleet statistic.
const frameLines = 5

// detailFrameLines is the same accounting for the detail view: two header
// lines (title, slug) and one footer line.
const detailFrameLines = 3

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// currentFilters snapshots the model's filter state for the pure functions.
func (m model) currentFilters() filters {
	f := filters{kinds: map[string]bool{}, matchAll: m.matchAll, group: m.group,
		orgPath: m.orgFilterPath(), search: m.search}
	for k := range m.filterKinds {
		f.kinds[k] = true
	}
	if m.ageIdx > 0 && m.ageIdx < len(agePresets) {
		if d := agePresets[m.ageIdx].dur; d > 0 {
			f.since = time.Now().Add(-d)
		}
	}
	return f
}

// orgFilterPath resolves the org filter's registration to its checkout
// path at match time — the same resolution the org manager's ON DISK count
// uses, so the two views can never disagree about what belongs to an org.
// A registration removed from the config simply stops matching ("").
func (m model) orgFilterPath() string {
	if m.orgFilter == "" {
		return ""
	}
	for _, o := range m.cfg.Orgs {
		if orgKey(o) == m.orgFilter {
			return o.ResolvedPath(m.cfg)
		}
	}
	return ""
}

// orgFilterOwner is the display name for the filter summary.
func (m model) orgFilterOwner() string {
	for _, o := range m.cfg.Orgs {
		if orgKey(o) == m.orgFilter {
			return o.Owner + " on " + o.Host
		}
	}
	return ""
}

// visibleRows is the derived table: filter, search, sort. The base order is
// discovery's (sorted by root), so the same fleet always renders the same
// table regardless of map iteration.
func (m model) visibleRows() []RepoState {
	f := m.currentFilters()
	base := make([]RepoState, 0, len(m.rows))
	for _, r := range m.rows {
		if f.match(r) {
			base = append(base, r)
		}
	}
	sortByRoot(base)
	sortRows(base, m.sortKey, m.sortReverse)
	return base
}

func sortByRoot(rows []RepoState) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].Root < rows[j-1].Root; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

// View renders the whole frame.
func (m model) View() string {
	switch m.mode {
	case modeHelp:
		return m.helpView()
	case modeColumns:
		return m.columnsView()
	case modeDetail:
		return m.detailView()
	case modeOrgs:
		return m.orgsView()
	case modeOrgForm:
		return m.orgFormView()
	case modeOwners:
		return m.ownersView()
	default:
		return m.tableView()
	}
}

func (m model) tableView() string {
	rows := m.visibleRows()

	// Clamp the selection into the current row set; a filter change or a
	// deleted repo can shrink the table under the cursor.
	if m.sel >= len(rows) {
		m.sel = len(rows) - 1
	}
	if m.sel < 0 {
		m.sel = 0
	}

	var b strings.Builder
	b.WriteString(m.headerView())
	b.WriteString("\n")
	b.WriteString(m.statusView())
	b.WriteString("\n")

	rowsHeight := m.height - frameLines
	if rowsHeight < 1 {
		rowsHeight = 1
	}

	cols := visibleColumns(m.cols)
	// Widths derive from the whole filtered fleet and the terminal width,
	// never the scroll window: an off-screen repo and a moved cursor must
	// not reflow the grid (§12 full-width, stable columns).
	m.ensureOffset(len(rows), rowsHeight)
	widths := columnWidths(cols, rows, m.width)

	b.WriteString(m.tableHeader(cols, widths))
	b.WriteString("\n")
	for i := m.offset; i < len(rows) && i < m.offset+rowsHeight; i++ {
		b.WriteString(m.tableRow(cols, widths, rows[i], i == m.sel))
		b.WriteString("\n")
	}
	// Pad the frame so the footer stays pinned to the bottom of the
	// terminal whatever the fleet's size.
	for i := len(rows) - m.offset; i < rowsHeight; i++ {
		b.WriteString(strings.Repeat(" ", maxInt(1, m.width)))
		b.WriteString("\n")
	}

	b.WriteString(m.footerView(rows, rowsHeight))
	return b.String()
}

// ensureOffset keeps the selected row inside the viewport (§12 scrolling).
func (m *model) ensureOffset(total, rowsHeight int) {
	if rowsHeight < 1 {
		return
	}
	if m.offset > total-rowsHeight {
		m.offset = total - rowsHeight
	}
	if m.offset < 0 {
		m.offset = 0
	}
	if m.sel < m.offset {
		m.offset = m.sel
	}
	if m.sel >= m.offset+rowsHeight {
		m.offset = m.sel - rowsHeight + 1
	}
}

// headerView is the first line's left side: the title and the fleet counts.
// The sweep's spinner and progress moved to the operation widget on the
// line's right edge (§12), so the left half only ever changes when the
// fleet's verdicts do.
func (m model) headerView() string {
	var counts fleetCounts
	for _, r := range m.rows {
		counts.add(r)
	}

	var b strings.Builder
	b.WriteString(styles.title.Render("coldstorage"))
	b.WriteString(" repos " + itoa(counts.repos))
	if counts.dirty > 0 {
		b.WriteString(" " + styles.dirty.Render("dirty "+itoa(counts.dirty)))
	}
	if counts.unpushed > 0 {
		b.WriteString(" " + styles.unpushed.Render("unpushed "+itoa(counts.unpushed)))
	}
	if counts.needsRelease > 0 {
		b.WriteString(" " + styles.release.Render("needs-release "+itoa(counts.needsRelease)))
	}
	if counts.neverFetched > 0 {
		b.WriteString(" " + styles.dim.Render("unfetched "+itoa(counts.neverFetched)))
	}
	return m.headerLine(b.String())
}

// opWidget renders the background operation the queue is running right
// now — the sync's current repo, the sweep's progress — or "" when the
// queue is idle, so the header stays quiet until there is something worth
// saying (§12).
func (m model) opWidget() string {
	var label, detail string
	switch {
	case m.syncRunning:
		label, detail = "sync "+m.syncOrg, m.syncProgress
	case m.pullRunning:
		label = "sync"
		if m.pullTotal > 0 {
			detail = itoa(minInt(m.pullDone, m.pullTotal)) + "/" + itoa(m.pullTotal)
		}
		if m.pullName != "" {
			detail += " · " + m.pullName
		}
	case m.sweeping:
		label = "sweep"
		if m.sweepTotal > 0 {
			detail = itoa(minInt(m.swept, m.sweepTotal)) + "/" + itoa(m.sweepTotal)
		}
	default:
		return ""
	}
	s := styles.spinner.Render(spinnerFrames[m.spinnerIdx%len(spinnerFrames)]) + " " + label
	if detail != "" {
		s += " " + styles.dim.Render(detail)
	}
	return s
}

// headerLine lays one chrome line out across the terminal: the left
// content, the widget right-aligned on the edge, the empty space between.
// Truncation — when the halves cannot share the line — MUST measure and cut
// by visual width, not runes: the left half is styled, and its escape
// sequences once pushed the rune count past the terminal edge while the
// line had acres of room, cutting off the fleet counts (the incident behind
// this comment).
func (m model) headerLine(left string) string {
	w := maxInt(1, m.width)
	right := m.opWidget()
	rw := lipgloss.Width(right)
	if rw == 0 {
		if lipgloss.Width(left) <= w {
			return left
		}
		return ansi.Truncate(left, w, "")
	}
	pad := w - lipgloss.Width(left) - rw
	if pad < 1 {
		// The two halves cannot share the line; the left half is the
		// load-bearing content and the widget is dropped.
		return ansi.Truncate(left, w, "")
	}
	return left + strings.Repeat(" ", pad) + right
}

// fleetCounts is the §12 header tally.
type fleetCounts struct {
	repos, dirty, unpushed, needsRelease, neverFetched int
}

func (c *fleetCounts) add(r RepoState) {
	c.repos++
	if r.Work != nil && r.Work.Dirty() {
		c.dirty++
	}
	if r.Refs.Unpushed() > 0 {
		c.unpushed++
	}
	if r.Release() == "needs-release" {
		c.needsRelease++
	}
	if r.Refs.FetchedAt.IsZero() {
		c.neverFetched++
	}
}

// statusView is the second header line: the transient notification, the
// search prompt while typing, else the active filter summary.
func (m model) statusView() string {
	if m.searching {
		return styles.unpushed.Render("/" + m.searchBuf + "▌")
	}
	if m.status != "" && time.Since(m.statusAt) < statusTTL {
		style := styles.status
		if strings.HasPrefix(m.status, "warning:") {
			style = styles.warn
		}
		return style.Render(truncate(m.status, maxInt(1, m.width)))
	}
	return m.filterSummary()
}

func (m model) filterSummary() string {
	var parts []string
	for _, f := range filterNames {
		if m.filterKinds[f.key] {
			parts = append(parts, f.key)
		}
	}
	matchMode := "any"
	if m.matchAll {
		matchMode = "all"
	}
	var f string
	switch {
	case len(parts) == 0 && m.group == "" && m.search == "" && m.ageIdx == 0 && m.orgFilter == "":
		return m.defaultStatusLine()
	case len(parts) > 0:
		f = "filters: " + strings.Join(parts, ",") + " (" + matchMode + ")"
	}
	if m.orgFilter != "" {
		if owner := m.orgFilterOwner(); owner != "" {
			f = joinFilterPart(f, "org: "+owner)
		}
	}
	if m.group != "" {
		f = joinFilterPart(f, "group: "+m.group)
	}
	if m.search != "" {
		f = joinFilterPart(f, "/"+m.search)
	}
	if m.ageIdx > 0 {
		f = joinFilterPart(f, "since "+agePresets[m.ageIdx].label)
	}
	return styles.status.Render("[" + f + "] " + m.defaultStatusLine())
}

func joinFilterPart(acc, part string) string {
	if acc == "" {
		return part
	}
	return acc + " · " + part
}

func (m model) defaultStatusLine() string {
	sortDesc := m.sortKey
	if m.sortReverse {
		sortDesc += " (reversed)"
	}
	return "sorted by " + sortDesc + " · " + itoa(len(m.rows)) + " repos"
}

// tableHeader renders the column caption row.
func (m model) tableHeader(cols []column, widths []int) string {
	var cells []string
	for i, col := range cols {
		cells = append(cells, styles.header.Render(padTo(col.header, widths[i])))
	}
	return strings.Join(cells, " ")
}

// tableRow renders one data row with the selection highlight and the §12
// color grammar applied per column.
func (m model) tableRow(cols []column, widths []int, r RepoState, selected bool) string {
	var cells []string
	for i, col := range cols {
		plain := col.cell(r)
		styled := plain
		if col.styled != nil {
			styled = col.styled(r, plain, selected)
		}
		st := m.stateRowBase(r, selected)
		cells = append(cells, st.Render(padTo(styled, widths[i])))
	}
	return strings.Join(cells, " ")
}

// stateRowBase dims everything about a clean row and highlights the
// selected one; a group with a configured background carries it on every
// non-selected row — the selection always outranks the group color, and
// the §12 verdict grammar stays foreground-only so it reads on any
// background. Individual columns then re-color their own text.
func (m model) stateRowBase(r RepoState, selected bool) lipgloss.Style {
	if selected {
		return styles.selected
	}
	if bg := m.groupColor(r.Group); bg != "" {
		return lipgloss.NewStyle().Background(bg)
	}
	if r.State() == "clean" {
		return styles.clean
	}
	return lipgloss.NewStyle()
}

// footerView is the key hints (§12 footer) plus the selection context.
func (m model) footerView(rows []RepoState, rowsHeight int) string {
	hints := "j/k move · ⏎ detail · d/filters · p sync · P sync all · s sort · / search · t gitui · o files · T shell · C columns · A orgs · R rescan · ? help · q quit"
	position := ""
	if len(rows) > 0 {
		position = itoa(m.sel+1) + "/" + itoa(len(rows))
	} else {
		position = "0 repos"
	}
	width := maxInt(1, m.width)
	pad := width - lipgloss.Width(hints) - lipgloss.Width(position)
	if pad < 1 {
		pad = 1
	}
	line := hints + strings.Repeat(" ", pad) + position

	context := ""
	if m.sel < len(rows) {
		context = rows[m.sel].Slug() + " · " + rows[m.sel].Root
	}

	return styles.status.Render(truncate(line, width)) + "\n" + styles.status.Render(truncate(context, width))
}

func padTo(s string, w int) string {
	if diff := w - lipgloss.Width(s); diff > 0 {
		return s + strings.Repeat(" ", diff)
	}
	return s
}

func truncate(s string, w int) string {
	if w <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return ""
	}
	return string(r[:w-1]) + "…"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
