// The detail view (GO-PORT-SPEC.md §9 `status`, rendered per §8): everything
// about one repo — state, release, visibility, activity and its source,
// head, remote, fetched time, tags, changelog verdict, the branch table and
// the since-tag subjects — scrollable, one ⏎ away from the table.
package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/crueber/coldstorage/internal/gitmode"
)

// detailView renders the selected repo's full state.
func (m model) detailView() string {
	rows := m.visibleRows()
	if m.sel < 0 || m.sel >= len(rows) {
		return m.headerView() + "\n" + styles.status.Render("no repo selected") + "\n"
	}
	r := rows[m.sel]

	now := time.Now()
	lines := detailLines(r, now)
	lines = append(lines, m.historyLines(r, now)...)

	var b strings.Builder
	b.WriteString(m.headerView())
	b.WriteString("\n")
	b.WriteString(styles.status.Render(r.Slug() + " · esc/⏎ back to table"))
	b.WriteString("\n")

	rowsHeight := m.height - frameLines
	if rowsHeight < 1 {
		rowsHeight = 1
	}
	for i := m.detailOff; i < len(lines) && i < m.detailOff+rowsHeight; i++ {
		b.WriteString(lines[i])
		b.WriteString("\n")
	}
	for i := len(lines) - m.detailOff; i < rowsHeight; i++ {
		b.WriteString("\n")
	}
	pos := ""
	if len(lines) > 0 {
		pos = fmt.Sprintf("%d-%d of %d", m.detailOff+1, minInt(m.detailOff+rowsHeight, len(lines)), len(lines))
	}
	b.WriteString(styles.status.Render("j/k scroll · esc back · " + pos))
	return b.String()
}

// histPage is how many commit titles one background fetch pulls; histAhead
// is how close to the loaded end a scroll must come before the next page
// starts loading.
const (
	histPage  = 100
	histAhead = 15
)

// historyLines renders the COMMITS section: the repo's recent commit
// titles (§9), paged in as the owner scrolls. The section exists once the
// first page lands and keeps growing to the end of the history; a repo
// with none — or one git could not read — says so in one dim line.
func (m model) historyLines(r RepoState, now time.Time) []string {
	if r.Root != m.histRoot {
		return nil
	}
	if m.histErr != nil {
		return []string{"", styles.header.Render("  COMMITS"), styles.dim.Render("  history unavailable: " + m.histErr.Error())}
	}
	if len(m.histCommits) == 0 && !m.histLoading {
		return []string{"", styles.header.Render("  COMMITS"), styles.dim.Render("  no commits")}
	}
	lines := []string{"", styles.header.Render("  COMMITS (newest first)")}
	for _, c := range m.histCommits {
		lines = append(lines, "  "+styles.dim.Render(relAge(c.Time, now))+"  "+c.Subject)
	}
	if m.histLoading {
		lines = append(lines, styles.dim.Render("  loading…"))
	}
	return lines
}

// loadHistory fetches the next page of the selected repo's commit history
// on a command goroutine — the caller never blocks on git (§12).
func (m model) loadHistory() tea.Cmd {
	if m.histLoading || m.histDone || m.histRoot == "" {
		return nil
	}
	m.histLoading = true
	root, skip := m.histRoot, len(m.histCommits)
	return func() tea.Msg {
		commits, err := gitmode.Log(root, histPage, skip)
		return histMsg{root: root, commits: commits, err: err}
	}
}

// maybeLoadHistory issues the next page fetch when the owner has scrolled
// within histAhead lines of the loaded end.
func (m model) maybeLoadHistory() tea.Cmd {
	rows := m.visibleRows()
	if m.sel < 0 || m.sel >= len(rows) || rows[m.sel].Root != m.histRoot {
		return nil
	}
	rowsHeight := maxInt(1, m.height-frameLines)
	total := len(detailLines(rows[m.sel], time.Now())) + len(m.historyLines(rows[m.sel], time.Now()))
	if m.detailOff+rowsHeight < total-histAhead {
		return nil
	}
	return m.loadHistory()
}

// detailLines is the pure renderer for one repo's detail text, so tests can
// pin the §8/§9 content without a terminal.
func detailLines(r RepoState, now time.Time) []string {
	var lines []string
	add := func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}

	st := r.State()
	lines = append(lines, styles.title.Render(r.Slug()))
	add("  state      %s", st)
	add("  release    %s", string(r.Release()))
	if r.Visibility != nil {
		add("  visibility %s (%s)", r.Visibility.Seen, string(r.Visibility.Status))
	}
	add("  activity   %s (%s)", relAge(r.Activity(), now), string(r.ActivitySource()))
	add("  head       %s", r.Refs.Head.Label())
	add("  root       %s", r.Root)
	add("  remote     %s", orDash(r.Refs.RemoteURL))
	if r.Refs.FetchedAt.IsZero() {
		add("  fetched    never")
	} else {
		add("  fetched    %s", relAge(r.Refs.FetchedAt, now))
	}

	if r.Refs.NewestTag != nil {
		add("  newest tag %s (%s)", r.Refs.NewestTag.Name, relAge(r.Refs.NewestTag.At, now))
	} else {
		add("  newest tag (none)")
	}
	if r.Refs.DescribedTag != nil {
		add("  at HEAD    %s", r.Refs.DescribedTag.Name)
	}
	if r.Refs.CommitsSinceTag > 0 {
		add("  since tag  %d commits", r.Refs.CommitsSinceTag)
	}
	if r.Refs.TagsOrphaned {
		add("  orphaned   newest tag sits on unreachable history")
	}
	if r.Refs.TagOffBranch() {
		add("  note       newest tag is not reachable from HEAD (git-flow)")
	}
	if r.Refs.Changelog != nil {
		if r.Refs.Changelog.Version != "" {
			add("  changelog  %s", r.Refs.Changelog.Version)
			if !r.Refs.Changelog.Tagged {
				add("             (version not tagged yet)")
			}
		}
	}
	if r.Work != nil && r.Work.Total() > 0 {
		add("  work       %s", changesCell(r))
		if r.Work.Truncated {
			add("             (file list truncated)")
		}
	}
	if r.Refs.Stashes > 0 {
		add("  stashes    %d", r.Refs.Stashes)
	}
	if r.Refs.IsBare {
		add("  bare       no working tree")
	}
	if r.Refs.IsShallow {
		add("  shallow    partial clone")
	}
	if r.Err != nil {
		lines = append(lines, styles.errorSt.Render("  error      "+r.Err.Error()))
	}

	if len(r.Refs.Branches) > 0 {
		lines = append(lines, "")
		lines = append(lines, styles.header.Render("  BRANCH              UPSTREAM             AHEAD  BEHIND  AGE  SUBJECT"))
		for _, br := range r.Refs.Branches {
			up := br.Upstream
			if br.Gone {
				up = "(gone)"
			}
			if up == "" {
				up = "—"
			}
			lines = append(lines, fmt.Sprintf("  %-19s %-20s %-6s %-6s  %-4s %s",
				padTo(br.Name, 19), padTo(up, 20),
				numberOrDot(br.Ahead), numberOrDot(br.Behind),
				relAge(br.CommittedAt, now), br.Subject))
		}
	}

	if len(r.Refs.SinceTagSubjects) > 0 {
		lines = append(lines, "")
		lines = append(lines, styles.header.Render("  SINCE-TAG COMMITS (newest first)"))
		for _, s := range r.Refs.SinceTagSubjects {
			lines = append(lines, "  · "+s)
		}
	}
	return lines
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// helpView renders the full keymap, grouped (§12 help mode).
func (m model) helpView() string {
	var b strings.Builder
	b.WriteString(styles.title.Render("coldstorage — keys"))
	b.WriteString("\n\n")

	rowsHeight := m.height - 2
	if rowsHeight < 1 {
		rowsHeight = 20
	}
	var lines []string
	for _, group := range helpGroups() {
		lines = append(lines, styles.header.Render(group.title))
		for _, k := range group.keys {
			lines = append(lines, "  "+padTo(k.key, 16)+k.desc)
		}
		lines = append(lines, "")
	}

	for i := m.helpOff; i < len(lines) && i < m.helpOff+rowsHeight; i++ {
		b.WriteString(lines[i])
		b.WriteString("\n")
	}
	b.WriteString(styles.status.Render("esc/? close"))
	return b.String()
}

type helpGroup struct {
	title string
	keys  []helpKey
}

type helpKey struct{ key, desc string }

// helpGroups is the help overlay's content: every key, grouped (§12).
func helpGroups() []helpGroup {
	var filterKeys []helpKey
	for _, f := range filterNames {
		filterKeys = append(filterKeys, helpKey{f.key1, f.desc})
	}
	return []helpGroup{
		{"moving", []helpKey{
			{"j/k, ↑/↓", "move selection"},
			{"ctrl-d/ctrl-u", "half page down/up"},
			{"pgup/pgdn", "page up/down"},
			{"home/end", "first/last row"},
			{"⏎", "repo detail"},
		}},
		{"filters & sorting", append(filterKeys, []helpKey{
			{"&", "toggle match any/all"},
			{"a", "clear all filters"},
			{"0-4", "age presets (any/1h/24h/1w/1mo)"},
			{"[ ]", "cycle group filter"},
			{"s", "cycle sort key"},
			{"S", "reverse sort"},
			{"/", "fuzzy search group/name/branch"},
		}...)},
		{"org manager", []helpKey{
			{"A", "org manager overlay"},
			{"j/k", "move selection"},
			{"a / e", "add / edit an org"},
			{"x x", "remove org (checkouts untouched)"},
			{"s / S", "sync selected / sync all enabled"},
			{"esc", "close"},
		}},
		{"overlays", []helpKey{
			{"?", "this help"},
			{"q", "quit"},
		}},
	}
}

// columnsView renders the C picker: every column with its on/off state.
func (m model) columnsView() string {
	var b strings.Builder
	b.WriteString(styles.title.Render("columns — space toggles, esc closes"))
	b.WriteString("\n\n")
	for i, col := range columnCatalog {
		box := "[ ]"
		if m.cols.visible(col) {
			box = "[x]"
		}
		cursor := " "
		if i == m.colCursor {
			cursor = ">"
		}
		b.WriteString(fmt.Sprintf("  %s %s %s", cursor, box, col.header))
		if col.optional {
			b.WriteString(styles.dim.Render("  (optional)"))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(styles.status.Render("j/k move · space/enter toggle · esc close"))
	return b.String()
}
