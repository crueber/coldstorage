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
)

// frameLines is the chrome the table shares the screen with: two header
// lines and two footer lines.
const frameLines = 4

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// currentFilters snapshots the model's filter state for the pure functions.
func (m model) currentFilters() filters {
	f := filters{kinds: map[string]bool{}, matchAll: m.matchAll, group: m.group, search: m.search}
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
	// Widths size to the window's rows, not the whole fleet: an off-screen
	// repo must not change column widths on screen (§12, content-sized).
	window := rows
	if len(rows) > rowsHeight {
		start := m.offset
		if start > len(rows)-rowsHeight {
			start = len(rows) - rowsHeight
		}
		if start < 0 {
			start = 0
		}
		window = rows[start : start+rowsHeight]
	}
	m.ensureOffset(len(rows), rowsHeight)

	widths := columnWidths(cols, window)

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

func (m model) headerView() string {
	var counts fleetCounts
	for _, r := range m.rows {
		counts.add(r)
	}

	var b strings.Builder
	b.WriteString(styles.title.Render("coldstorage"))
	b.WriteString(" ")
	if m.sweeping {
		b.WriteString(styles.spinner.Render(spinnerFrames[m.spinnerIdx%len(spinnerFrames)]))
		b.WriteString(" sweeping")
		if m.sweepTotal > 0 {
			b.WriteString(" " + itoa(minInt(m.swept, m.sweepTotal)) + "/" + itoa(m.sweepTotal))
		}
		b.WriteString(" ")
	}
	b.WriteString("repos " + itoa(counts.repos))
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
	return b.String()
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
	case len(parts) == 0 && m.group == "" && m.search == "" && m.ageIdx == 0:
		return m.defaultStatusLine()
	case len(parts) > 0:
		f = "filters: " + strings.Join(parts, ",") + " (" + matchMode + ")"
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
		st := stateRowBase(r, selected)
		cells = append(cells, st.Render(padTo(styled, widths[i])))
	}
	return strings.Join(cells, " ")
}

// stateRowBase dims everything about a clean row and highlights the
// selected one; individual columns then re-color their own text.
func stateRowBase(r RepoState, selected bool) lipgloss.Style {
	if selected {
		return styles.selected
	}
	if r.State() == "clean" {
		return styles.clean
	}
	return lipgloss.NewStyle()
}

// footerView is the key hints (§12 footer) plus the selection context.
func (m model) footerView(rows []RepoState, rowsHeight int) string {
	hints := "j/k move · ⏎ detail · d/filters · s sort · / search · C columns · A orgs · R rescan · ? help · q quit"
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
