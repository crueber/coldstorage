// The org manager's paint (§12 org manager): the registration list, the
// add/edit form with its probe state and in-overlay refusal, and the owner
// picker. Like every view here, rendering is a pure projection of the model
// — it never mutates and never blocks, so an org file of any size draws the
// same way the table does.
package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// relTime renders a timestamp the way the AGE column does, compactly and
// only as precisely as a human reads it.
func relTime(t time.Time, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dmo", int(d.Hours()/(24*30)))
	}
}

// orgRowView is one line of the registration list: the §12 columns.
type orgRowView struct {
	provider, owner, host, path, onDisk, lastSync string
}

// orgRowsView projects the config's registrations into the list's rows. ON
// DISK counts the live model's repos under the org's resolved path — the
// same rows the table shows, so the two views can never disagree about what
// is checked out. LAST SYNC reads the session's sync stamps; orgsync keeps
// no persistent journal, so an org this session never synced shows "never".
func (m model) orgRowsView() []orgRowView {
	now := time.Now()
	rows := make([]orgRowView, 0, len(m.cfg.Orgs))
	for _, o := range m.cfg.Orgs {
		row := orgRowView{
			provider: o.ResolvedProvider(),
			owner:    o.Owner,
			host:     o.Host,
			path:     o.ResolvedPath(m.cfg),
		}
		if !o.Enabled {
			row.provider += " (off)"
		}
		if row.path == "" {
			row.path = "—"
		} else {
			n := 0
			for root := range m.rows {
				if root == row.path || strings.HasPrefix(root, row.path+string(filepath.Separator)) {
					n++
				}
			}
			row.onDisk = fmt.Sprintf("%d", n)
		}
		if at, ok := m.orgLastSync[orgKey(o)]; ok {
			row.lastSync = relTime(at, now)
		} else {
			row.lastSync = "never"
		}
		rows = append(rows, row)
	}
	return rows
}

// orgsView paints the A overlay (§12).
func (m model) orgsView() string {
	var b strings.Builder
	b.WriteString(m.headerLine(styles.title.Render("org manager")))
	b.WriteString("\n\n")

	rows := m.orgRowsView()
	if m.orgSel >= len(rows) {
		m.orgSel = len(rows) - 1
	}
	if m.orgSel < 0 {
		m.orgSel = 0
	}

	if len(rows) == 0 {
		b.WriteString(styles.dim.Render("no orgs registered — a to add one"))
		b.WriteString("\n\n")
	} else {
		widths := make([]int, 6)
		headers := []string{"PROVIDER", "OWNER", "HOST", "PATH", "ON DISK", "LAST SYNC"}
		cells := make([][]string, len(rows))
		for i, r := range rows {
			cells[i] = []string{r.provider, r.owner, r.host, r.path, r.onDisk, r.lastSync}
			for c, v := range cells[i] {
				if len(v) > widths[c] {
					widths[c] = len(v)
				}
				if widths[c] > 32 {
					widths[c] = 32
				}
			}
		}
		b.WriteString(styles.header.Render(formatOrgCells(headers, widths)))
		b.WriteString("\n")
		for i := range rows {
			line := formatOrgCells(cells[i], widths)
			if i == m.orgSel {
				line = styles.selected.Render(padTo(line, maxInt(1, m.width)))
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")

	if m.orgConfirm && m.orgSel >= 0 && m.orgSel < len(m.cfg.Orgs) {
		o := m.cfg.Orgs[m.orgSel]
		b.WriteString(styles.warn.Render(fmt.Sprintf(
			"press x again to remove %s/%s/%s — checkouts are left untouched",
			o.ResolvedProvider(), o.Host, o.Owner)))
		b.WriteString("\n")
	}
	b.WriteString(styles.status.Render("j/k move · a add · e edit · x x remove · s sync selected · S sync all enabled · esc close"))
	return b.String()
}

// formatOrgCells joins cells at the given widths with a two-space gutter.
func formatOrgCells(cells []string, widths []int) string {
	parts := make([]string, len(cells))
	for i, v := range cells {
		parts[i] = padTo(v, widths[i])
	}
	return strings.Join(parts, "  ")
}

// orgFormView paints the add/edit form (§12): it opens instantly in the
// probing state, rows cycle only through authenticated options once the
// probe lands, and a refusal renders inside the overlay — the status line
// is under it and would never be seen.
func (m model) orgFormView() string {
	f := m.orgForm
	var b strings.Builder
	if f.editing >= 0 && f.editing < len(m.cfg.Orgs) {
		o := m.cfg.Orgs[f.editing]
		b.WriteString(m.headerLine(styles.title.Render("edit org — " + o.Owner + " on " + o.Host)))
	} else {
		b.WriteString(m.headerLine(styles.title.Render("add org")))
	}
	b.WriteString("\n\n")

	if f.probing || !f.probeDone {
		b.WriteString(styles.dim.Render("probing tool auth…"))
		b.WriteString("\n\n")
	} else if len(f.authed) == 0 {
		b.WriteString(styles.warn.Render("no authenticated provider CLI found"))
		b.WriteString("\n\n")
	} else {
		var names []string
		for _, a := range f.authed {
			names = append(names, a.Provider+" ("+strings.Join(a.Hosts, ", ")+")")
		}
		sort.Strings(names)
		b.WriteString(styles.dim.Render("authenticated: " + strings.Join(names, " · ")))
		b.WriteString("\n\n")
	}

	rows := f.rows()
	if f.cursor >= len(rows) {
		f.cursor = len(rows) - 1
	}
	if f.cursor < 0 {
		f.cursor = 0
	}
	for i, row := range rows {
		cursor := " "
		if i == f.cursor {
			cursor = ">"
		}
		b.WriteString(cursor + " " + padTo(row.label, 18) + row.value)
		b.WriteString("\n")
	}
	b.WriteString("\n")

	if f.refusal != "" {
		b.WriteString(styles.warn.Render("refused: " + f.refusal))
		b.WriteString("\n\n")
	}
	hint := "j/k move · enter/space select · type to edit text rows · ctrl-s save · esc cancel"
	if f.probing || !f.probeDone {
		hint = "probing tool auth… — esc cancels"
	}
	b.WriteString(styles.status.Render(hint))
	return b.String()
}

// ownersView paints the fetched owner picker (§11.1): the provider's user
// and org memberships, narrowed by typing — and free-typing stays possible
// for memberships the API doesn't expose.
func (m model) ownersView() string {
	f := m.orgForm
	var b strings.Builder
	b.WriteString(m.headerLine(styles.title.Render("pick owner — " + f.provider + " · " + f.host)))
	b.WriteString("\n\n")

	if f.ownersLoad {
		b.WriteString(styles.dim.Render("fetching owners…"))
		b.WriteString("\n\n")
	}
	if f.ownerFilter != "" {
		b.WriteString("filter: " + f.ownerFilter)
		b.WriteString("\n\n")
	}

	filtered := f.filteredOwners()
	if !f.ownersLoad && len(filtered) == 0 {
		b.WriteString(styles.dim.Render("no owners fetched — type the owner and press enter"))
		b.WriteString("\n\n")
	}
	for i, o := range filtered {
		cursor := " "
		if i == f.ownerCursor {
			cursor = ">"
		}
		b.WriteString(cursor + " " + o)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(styles.status.Render("j/k move · enter pick · type to narrow · esc back to form"))
	return b.String()
}

// filteredOwners applies the picker's narrowing filter.
func (f orgForm) filteredOwners() []string {
	q := strings.ToLower(f.ownerFilter)
	if q == "" {
		return f.owners
	}
	var out []string
	for _, o := range f.owners {
		if strings.Contains(strings.ToLower(o), q) {
			out = append(out, o)
		}
	}
	return out
}
