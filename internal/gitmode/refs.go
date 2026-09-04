package gitmode

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// RefsOptions carries the release-facing slice of configuration Refs needs.
// It mirrors config's ReleaseConfig but stands alone so this package never
// depends on the config loader; the zero value asks for match-everything
// tags and the spec defaults.
type RefsOptions struct {
	TagPattern     string   // glob a tag must match to count as a release ("" = all)
	MaxSubjects    int      // commit subjects kept per repo; <=0 means the 30-subject default
	ChangelogFiles []string // filenames tried in order; empty means CHANGELOG.md
	ReadChangelog  bool     // whether to parse the changelog at all
}

// nul is the field separator inside one for-each-ref record. It is
// unrepresentable in ref names, subjects, and track annotations alike, so
// parsing cannot be confused by a commit message that got clever.
const nul = "\x00"

// Refs is tier 1: everything derivable from a checkout's .git directory
// without touching the working tree (§6). It answers with a partially
// filled RefsInfo plus an error only when the core of the probe fails —
// .git is unreadable or even for-each-ref over branches refuses. Everything
// release-shaped degrades to its empty verdict on a failed child (§17),
// because a repo whose git is mid-rewrite must still render as a row.
func Refs(root string, cfg RefsOptions) (RefsInfo, error) {
	gitDir, commonDir, err := gitDirs(root)
	if err != nil {
		return RefsInfo{}, err
	}

	branches, err := forEachBranch(root)
	if err != nil {
		return RefsInfo{}, err
	}
	head := readHead(gitDir, branches)

	tags := forEachTags(root)
	newest := newestTag(tags, cfg.TagPattern)
	described := describedTag(root, tags)
	commits, subjects := sinceTag(root, described, subjectsCap(cfg.MaxSubjects))

	info := RefsInfo{
		Head:             head,
		Branches:         branches,
		LastCommit:       lastCommit(root, head),
		NewestTag:        newest,
		DescribedTag:     described,
		CommitsSinceTag:  commits,
		SinceTagSubjects: subjects,
		TagsOrphaned:     tagsOrphaned(root, newest),
		Stashes:          stashCount(commonDir),
		Operation:        currentOperation(gitDir),
		IndexMtime:       mtimeOf(filepath.Join(gitDir, "index")),
		FetchedAt:        mtimeOf(filepath.Join(commonDir, "FETCH_HEAD")),
		RemoteURL:        remoteURL(commonDir),
		IsBare:           isBare(commonDir),
		IsShallow:        fileExists(filepath.Join(commonDir, "shallow")),
	}
	if cfg.ReadChangelog {
		info.Changelog = ReadChangelog(root, changelogFiles(cfg.ChangelogFiles), tagNames(tags))
	}
	return info, nil
}

// gitDirs locates the per-worktree git directory and the common directory.
// A .git that is a file (worktrees, submodules) points elsewhere, and that
// target names its own common root — HEAD and operation markers live in the
// worktree dir, config, FETCH_HEAD and the stash log in the common one.
// A root with no .git at all is a bare repository, whose root IS its git
// dir — bare repos still probe: branches ahead of their remote are exactly
// what tier 1 must report for them (§7.7). Reading each fact from the
// right directory is what makes worktree and bare checkouts probe
// correctly instead of showing phantom unborn branches.
func gitDirs(root string) (gd, cd string, err error) {
	dot := filepath.Join(root, ".git")
	if st, serr := os.Stat(dot); serr == nil {
		if st.IsDir() {
			return dot, dot, nil
		}
		data, rerr := os.ReadFile(dot)
		if rerr != nil {
			return "", "", rerr
		}
		line := strings.TrimSpace(string(data))
		if !strings.HasPrefix(line, "gitdir:") {
			return "", "", errNotGitDir{line}
		}
		gd = strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
		if !filepath.IsAbs(gd) {
			gd = filepath.Join(root, gd)
		}
		cd = commonOf(gd)
		return gd, cd, nil
	}
	if fileExists(filepath.Join(root, "HEAD")) {
		return root, commonOf(root), nil
	}
	return "", "", errNotGitDir{root}
}

// commonOf resolves a git directory to its common root, following the
// commondir pointer a linked worktree leaves behind.
func commonOf(gd string) string {
	if c, err := os.ReadFile(filepath.Join(gd, "commondir")); err == nil {
		p := strings.TrimSpace(string(c))
		if !filepath.IsAbs(p) {
			p = filepath.Join(gd, p)
		}
		return p
	}
	return gd
}

type errNotGitDir struct{ line string }

func (e errNotGitDir) Error() string {
	return ".git does not point at a git directory: " + e.line
}

// readHead classifies HEAD from the file on disk, not from a process. A
// symref to a branch that exists is HeadBranch; one to a branch no ref
// resolves to is the fresh-repo case, HeadUnborn — reporting "unborn"
// instead of "detached" for a repo with no commits is the difference
// between a row that reads as broken and one that reads as new (§6).
func readHead(gitDir string, branches []BranchInfo) Head {
	data, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return Head{Kind: HeadUnborn}
	}
	line := strings.TrimSpace(string(data))
	if strings.HasPrefix(line, "ref:") {
		name := strings.TrimSpace(strings.TrimPrefix(line, "ref:"))
		if !strings.HasPrefix(name, "refs/heads/") {
			return Head{Kind: HeadDetached, SHA: name}
		}
		branch := strings.TrimPrefix(name, "refs/heads/")
		for _, b := range branches {
			if b.Name == branch {
				return Head{Kind: HeadBranch, Branch: branch}
			}
		}
		return Head{Kind: HeadUnborn}
	}
	return Head{Kind: HeadDetached, SHA: line}
}

// forEachBranch gets every local branch — sha, upstream, tracking state,
// tip date, subject — from one for-each-ref call. Per-branch questions
// (rev-list, status -sb) would cost one process each; with 500–1000 repos
// the fleet probe is O(changed repos) only if each repo costs O(1)
// processes (§6, AGENTS.md).
func forEachBranch(root string) ([]BranchInfo, error) {
	format := "%(objectname)" + nulFmt + "%(refname:short)" + nulFmt +
		"%(upstream:short)" + nulFmt + "%(upstream:track)" + nulFmt +
		"%(committerdate:unix)" + nulFmt + "%(contents:subject)"
	out, err := RunGit(root, GitTimeout(), "for-each-ref", "refs/heads", "--format="+format)
	if err != nil {
		return nil, err
	}
	var branches []BranchInfo
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, nul)
		if len(f) < 6 {
			continue
		}
		b := BranchInfo{SHA: f[0], Name: f[1], Upstream: f[2], Subject: f[5]}
		b.Ahead, b.Behind, b.Gone = parseTrack(f[3])
		if u, perr := strconv.ParseInt(strings.TrimSpace(f[4]), 10, 64); perr == nil && u > 0 {
			b.CommittedAt = time.Unix(u, 0)
		}
		branches = append(branches, b)
	}
	return branches, nil
}

const nulFmt = "%00"

// parseTrack decodes upstream:track's annotation — "[ahead 1, behind 2]",
// "[gone]" — into numbers. "[gone]" is kept distinct from zero on purpose:
// a branch whose upstream was deleted on the remote is not "in sync", and
// showing 0 there is a claim nobody checked (§7.2).
func parseTrack(track string) (ahead, behind int, gone bool) {
	track = strings.TrimSpace(track)
	if track == "" {
		return 0, 0, false
	}
	if track == "[gone]" {
		return 0, 0, true
	}
	track = strings.TrimSuffix(strings.TrimPrefix(track, "["), "]")
	for _, part := range strings.Split(track, ",") {
		part = strings.TrimSpace(part)
		switch {
		case strings.HasPrefix(part, "ahead"):
			ahead, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(part, "ahead")))
		case strings.HasPrefix(part, "behind"):
			behind, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(part, "behind")))
		}
	}
	return ahead, behind, false
}

// forEachTags lists every tag with its creation date, newest first. Both
// the newest tag and the described tag are read from this single listing;
// the date sort uses creatordate because the release column asks "when was
// this version cut", and a lightweight tag's creatordate falls back to the
// commit date — the honest answer for repos that do not annotate.
func forEachTags(root string) []TagInfo {
	format := "%(refname:short)" + nulFmt + "%(creatordate:unix)"
	out, ok := RunGitOK(root, GitTimeout(), "for-each-ref", "refs/tags",
		"--sort=-creatordate", "--format="+format)
	if !ok {
		return nil
	}
	var tags []TagInfo
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, nul)
		if len(f) < 2 {
			continue
		}
		t := TagInfo{Name: f[0]}
		if u, err := strconv.ParseInt(strings.TrimSpace(f[1]), 10, 64); err == nil && u > 0 {
			t.At = time.Unix(u, 0)
		}
		tags = append(tags, t)
	}
	return tags
}

// tagMatches applies the configured release glob to a tag name. A broken
// pattern degrades to match-everything: a typo in config must not make the
// release column silently report every repo as unreleased.
func tagMatches(pattern, tag string) bool {
	if pattern == "" {
		return true
	}
	ok, err := filepath.Match(pattern, tag)
	if err != nil {
		return true
	}
	return ok
}

// newestTag picks the newest tag by creation date among those matching the
// release glob. It is tracked separately from the described tag because in
// git-flow they legitimately differ — the newest tag lands on master while
// HEAD sits on develop (§7.3).
func newestTag(tags []TagInfo, pattern string) *TagInfo {
	for i := range tags {
		if tagMatches(pattern, tags[i].Name) {
			t := tags[i]
			return &t
		}
	}
	return nil
}

// describedTag asks describe for the nearest tag reachable from HEAD. A
// repo with no tags in reach — every fresh checkout — answers false here,
// which is the expected outcome, not an error.
func describedTag(root string, tags []TagInfo) *TagInfo {
	name, ok := RunGitOK(root, GitTimeout(), "describe", "--tags", "--abbrev=0")
	if !ok {
		return nil
	}
	name = strings.TrimSpace(name)
	for i := range tags {
		if tags[i].Name == name {
			t := tags[i]
			return &t
		}
	}
	return &TagInfo{Name: name}
}

// sinceTag counts commits on HEAD since the described tag and collects
// their subjects. The git-flow rule (§7.3): merge commits are excluded from
// the count — a back-merge is history, not work — except when a merge is
// the sole commit since the tag AND changes the tree, which is a release's
// worth of change pretending to be a no-op. Subjects come from the
// non-merge log, capped: subjects are display data, not archives.
func sinceTag(root string, tag *TagInfo, maxSubjects int) (int, []string) {
	if tag == nil {
		return 0, nil
	}
	totalStr, okT := RunGitOK(root, GitTimeout(), "rev-list", "--count", tag.Name+"..HEAD")
	nonStr, okN := RunGitOK(root, GitTimeout(), "rev-list", "--count", "--no-merges", tag.Name+"..HEAD")
	if !okT || !okN {
		return 0, nil
	}
	total, _ := strconv.Atoi(strings.TrimSpace(totalStr))
	non, _ := strconv.Atoi(strings.TrimSpace(nonStr))
	n := non
	if total == 1 && non == 0 {
		// diff --quiet exits non-zero exactly when the tree moved; a
		// non-zero exit is the expected answer here, not a failure.
		if _, same := RunGitOK(root, GitTimeout(), "diff", "--quiet", tag.Name, "HEAD"); !same {
			n = 1
		}
	}
	var subjects []string
	if out, ok := RunGitOK(root, GitTimeout(), "log", tag.Name+"..HEAD", "--no-merges", "--format=%s"); ok {
		for _, s := range strings.Split(out, "\n") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if maxSubjects > 0 && len(subjects) >= maxSubjects {
				break
			}
			subjects = append(subjects, s)
		}
	}
	return n, subjects
}

// subjectsCap fills in the display cap the config omitted.
func subjectsCap(max int) int {
	if max <= 0 {
		return 30
	}
	return max
}

// tagsOrphaned reports whether the newest tag sits on history no branch
// reaches — the residue of history rewrites. Such a tag is reported, not
// silently treated as a release (§7.4); the verdict is only claimed when
// the question could actually be asked.
func tagsOrphaned(root string, newest *TagInfo) bool {
	if newest == nil {
		return false
	}
	out, err := RunGit(root, GitTimeout(), "branch", "--contains", newest.Name)
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == ""
}

// stashCount counts stashes as the line count of logs/refs/stash — no
// process (§7.8). Bare repos get a real zero from the same code path
// rather than a "not scanned" placeholder.
func stashCount(commonDir string) int {
	data, err := os.ReadFile(filepath.Join(commonDir, "logs", "refs", "stash"))
	if err != nil || len(data) == 0 {
		return 0
	}
	n := 0
	for _, b := range data {
		if b == '\n' {
			n++
		}
	}
	if data[len(data)-1] != '\n' {
		n++
	}
	return n
}

// currentOperation detects a half-finished operation from its marker files
// under .git — the same signal git itself uses to refuse and to resume.
// These markers are what the state column renders ahead of dirty/unpushed
// in the precedence (§8): "rebasing" is a more urgent fact than "dirty".
func currentOperation(gitDir string) Operation {
	if fileExists(filepath.Join(gitDir, "MERGE_HEAD")) {
		return OpMerge
	}
	if fileExists(filepath.Join(gitDir, "rebase-merge")) ||
		fileExists(filepath.Join(gitDir, "rebase-apply")) {
		return OpRebase
	}
	if fileExists(filepath.Join(gitDir, "CHERRY_PICK_HEAD")) {
		return OpCherryPick
	}
	if fileExists(filepath.Join(gitDir, "REVERT_HEAD")) {
		return OpRevert
	}
	if fileExists(filepath.Join(gitDir, "BISECT_LOG")) {
		return OpBisect
	}
	return ""
}

// lastCommit takes HEAD's tip commit for the activity column. It is
// deliberately derived from the commit, not the index: any tool running
// git status refreshes the index mtime, and an editor sitting open on a
// repo would otherwise read as recently active (§7.6).
func lastCommit(root string, head Head) *CommitInfo {
	if head.Kind == HeadUnborn {
		return nil
	}
	out, ok := RunGitOK(root, GitTimeout(), "log", "-1", "--format=%H%x00%at%x00%s%x00%an")
	if !ok {
		return nil
	}
	f := strings.Split(strings.TrimRight(out, "\n"), nul)
	if len(f) < 4 {
		return nil
	}
	c := &CommitInfo{SHA: f[0], Subject: f[2], Author: f[3]}
	if u, err := strconv.ParseInt(strings.TrimSpace(f[1]), 10, 64); err == nil && u > 0 {
		c.At = time.Unix(u, 0)
	}
	return c
}

// mtimeOf returns a file's modification time, or the zero time when it
// does not exist. For FETCH_HEAD the zero time is a fact, not a failure:
// it means the repo never fetched, and "never" must not render as the
// false confidence of "in sync" (§7.2).
func mtimeOf(path string) time.Time {
	if st, err := os.Stat(path); err == nil {
		return st.ModTime()
	}
	return time.Time{}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func tagNames(tags []TagInfo) []string {
	names := make([]string, len(tags))
	for i := range tags {
		names[i] = tags[i].Name
	}
	return names
}

func changelogFiles(files []string) []string {
	if len(files) > 0 {
		return files
	}
	return []string{"CHANGELOG.md", "CHANGELOG", "changelog.md"}
}

// iniEntry is one `key = value` line under one `[section]` header — the
// minimum .git/config model needed here. A hand-rolled reader, because the
// remote URL and the bare flag are two scalars, and a probe that shells
// out to `git config` for scalars it can read off disk has already spent
// its process budget badly (§6).
type iniEntry struct{ section, key, value string }

func parseINI(path string) ([]iniEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []iniEntry
	section := ""
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out = append(out, iniEntry{section: section, key: strings.TrimSpace(k), value: strings.TrimSpace(v)})
	}
	return out, nil
}

// remoteURL picks the checkout's remote straight out of .git/config: the
// origin URL when there is one, else the first remote defined. No process,
// and a repo with no remote answers empty — which the sync engine reads as
// its loud error, not as a probe failure.
func remoteURL(commonDir string) string {
	entries, err := parseINI(filepath.Join(commonDir, "config"))
	if err != nil {
		return ""
	}
	origin := ""
	first := ""
	for _, e := range entries {
		name, isRemote := strings.CutPrefix(e.section, `remote "`)
		if !isRemote || !strings.HasSuffix(name, `"`) || e.key != "url" {
			continue
		}
		switch strings.TrimSuffix(name, `"`) {
		case "origin":
			origin = e.value
		case "":
			if first == "" {
				first = e.value
			}
		default:
			if first == "" {
				first = e.value
			}
		}
	}
	if origin != "" {
		return origin
	}
	return first
}

// isBare reads core.bare from .git/config. A bare repo still probes fine —
// branches, tags, and stashes all resolve — but there is no working tree
// to scan, and the rest of the dashboard keys off this flag to say so
// (§7.7).
func isBare(commonDir string) bool {
	entries, err := parseINI(filepath.Join(commonDir, "config"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.section == "core" && e.key == "bare" {
			return e.value == "true"
		}
	}
	return false
}
