// This file implements tier 2 of the per-repo probe (GO-PORT-SPEC.md §6):
// the `git status --porcelain=v2` scan. It is deliberately the expensive
// half — tier 1 reads .git for free, but status walks the working tree — so
// it carries a WorkKey (HEAD sha + index mtime + size) that the cache layer
// uses to skip repos whose scans are still good.
package gitmode

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// UntrackedMode selects how eager the status scan is about untracked files.
// It maps straight onto git's --untracked-files modes.
type UntrackedMode string

const (
	UntrackedNormal UntrackedMode = "normal" // untracked files and dirs, collapsed
	UntrackedAll    UntrackedMode = "all"    // every file inside untracked dirs
	UntrackedNo     UntrackedMode = "no"     // no untracked enumeration at all
)

// WorkOptions shapes a tier-2 scan.
type WorkOptions struct {
	// Untracked picks the --untracked-files mode; empty means normal.
	Untracked UntrackedMode
	// MaxFiles caps how many changed files are listed per repo (the count
	// fields are always exact). Zero or negative means no cap, which the
	// config layer (status.max_files) should never produce in practice.
	MaxFiles int
	// MaxAge is recorded for the cache layer, never consulted by Work
	// itself — see WorkExpired.
	MaxAge time.Duration
	// Timeout bounds the git child; zero means the rails' default.
	Timeout time.Duration
}

// Work runs the tier-2 scan: one `git status --porcelain=v2 --branch` and a
// handful of stat calls. The branch headers are parsed and thrown away —
// git would otherwise revwalk for ahead/behind, which is duplicated work
// when tier 1 already read the tracking refs — but still requested, so the
// porcelain shape is stable and the parse can refuse malformed output.
//
// The WorkKey it returns is computed from the filesystem, not from the
// status output, so a cached WorkInfo can be validated without running
// anything (see WorkFresh).
func Work(root string, cfg WorkOptions) (WorkInfo, WorkKey, error) {
	mode := cfg.Untracked
	if mode == "" {
		mode = UntrackedNormal
	}
	out, err := RunGit(root, cfg.Timeout,
		"status", "--porcelain=v2", "--branch",
		"--untracked-files="+string(mode),
		"--ignore-submodules=dirty",
	)
	if err != nil {
		return WorkInfo{}, WorkKey{}, err
	}
	info := parseStatusV2(out, root, cfg.MaxFiles)
	return info, workKey(root), nil
}

// WorkFresh reports whether a cached tier-2 scan is still valid. If HEAD
// and the index are both where they were, nothing can have entered or left
// the staged set, and working-tree changes can only come from the watcher —
// which bypasses this cache anyway. This is the cheap check that lets the
// probe skip `git status` across an idle fleet.
func WorkFresh(prev WorkKey, root string) bool {
	return workKey(root) == prev
}

// WorkExpired is the pure half of the max_age backstop: it says only whether
// a cached scan older than prevAge has outlived maxAge. It deliberately
// decides nothing by itself — and an expired max_age NEVER overrides the
// fingerprint gate (spec §6): the "two git processes running forever"
// incident came from the age backstop re-running status across the whole
// fleet every sweep. The gate answers first; max_age only ever applies to
// rows the fingerprint already agreed to re-scan.
func WorkExpired(prevAge, maxAge time.Duration) bool {
	if maxAge <= 0 {
		return false
	}
	return prevAge >= maxAge
}

// workKey snapshots the cache-validity inputs from the filesystem: the
// resolved HEAD sha and the index's mtime and size. Zero-value fields are
// meaningful states, not failures — an unborn HEAD hashes to the empty
// string, and a bare repo has no index at all (HasIndex false).
func workKey(root string) WorkKey {
	gd := gitDir(root)
	key := WorkKey{HeadSHA: resolveHeadSHA(gd)}
	if fi, err := os.Stat(filepath.Join(gd, "index")); err == nil {
		key.HasIndex = true
		key.IndexMtime = fi.ModTime()
		key.IndexSize = fi.Size()
	}
	return key
}

// resolveHeadSHA resolves HEAD to a commit sha without running git: read
// HEAD, and for a symref follow it through a loose ref file, then
// packed-refs. An empty result means unborn (a repo with no commits) —
// which is itself a stable, comparable state for the cache key.
func resolveHeadSHA(gd string) string {
	data, err := os.ReadFile(filepath.Join(gd, "HEAD"))
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(data))
	if ref, ok := strings.CutPrefix(line, "ref:"); ok {
		ref = strings.TrimSpace(ref)
		if sha := looseRef(gd, ref); sha != "" {
			return sha
		}
		return packedRef(gd, ref)
	}
	return line // detached HEAD: the sha itself
}

// looseRef reads a ref file under the git dir. Refs come from git's own
// HEAD file, but the ref name is still user-shaped data one symlink away
// from the filesystem, so anything containing ".." is refused rather than
// resolved.
func looseRef(gd, ref string) string {
	if strings.Contains(ref, "..") {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(gd, filepath.FromSlash(ref)))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// packedRef looks a ref up in packed-refs, the fallback for refs git hasn't
// bothered to write loose. An unresolvable ref is an unborn branch: the
// empty string, not an error.
func packedRef(gd, ref string) string {
	data, err := os.ReadFile(filepath.Join(gd, "packed-refs"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		if sha, name, ok := strings.Cut(line, " "); ok && strings.TrimSpace(name) == ref {
			return sha
		}
	}
	return ""
}

// gitDir locates the git directory for a checkout root: the .git directory
// itself, the gitdir pointer for a linked worktree or submodule, or — for a
// bare repo — the root, because a bare repo IS its git dir.
func gitDir(root string) string {
	dot := filepath.Join(root, ".git")
	if fi, err := os.Stat(dot); err == nil && fi.IsDir() {
		return dot
	}
	if data, err := os.ReadFile(dot); err == nil {
		if body, ok := strings.CutPrefix(string(data), "gitdir:"); ok {
			p := strings.TrimSpace(body)
			if filepath.IsAbs(p) {
				return p
			}
			return filepath.Join(root, p)
		}
	}
	return root
}

// commonDir resolves the shared git directory behind a linked worktree
// (refs, packed-refs, config, FETCH_HEAD live there; HEAD and index do
// not). For an ordinary checkout it is the git dir itself.
func commonDir(gd string) string {
	if data, err := os.ReadFile(filepath.Join(gd, "commondir")); err == nil {
		p := strings.TrimSpace(string(data))
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(gd, p)
	}
	return gd
}

// parseStatusV2 turns raw porcelain v2 output into a WorkInfo. Kept as a
// pure function over its input (plus stat calls against root) so the
// fixtures can pin git's exact output shapes without a child process.
//
// The path handling mirrors git's contract: the path is the last
// space-separated field of the record (paths can contain spaces), and for a
// rename record a tab separates the new path from the original — the new
// path is the one a user sees in the working tree.
func parseStatusV2(raw string, root string, maxFiles int) WorkInfo {
	info := WorkInfo{Files: []ChangedFile{}}

	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		tag, rest, ok := strings.Cut(line, " ")
		if !ok {
			continue // bare "unknown" line or truncated record: skip, don't guess
		}

		var path, code string
		var kind ChangeKind
		switch tag {
		// 1 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <path>
		// 2 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <X><score> <path><tab><origPath>
		case "1", "2":
			leading := 8
			if tag == "2" {
				leading = 9
			}
			path = fieldTail(rest, leading)
			code = xyCode(rest, "..")
			x, y := code[0], code[1]
			if x != '.' {
				info.Staged++
			}
			if y != '.' {
				info.Unstaged++
			}
			// A record can be both (MM): the kind follows the worktree
			// side, because that is the half the file's mtime speaks for.
			kind = ChangeUnstaged
			if y == '.' {
				kind = ChangeStaged
			}
		// u <XY> <sub> <m1> <m2> <m3> <mW> <h1> <h2> <h3> <path>
		case "u":
			info.Conflicts++
			path = fieldTail(rest, 10)
			code = xyCode(rest, "UU")
			kind = ChangeConflicted
		case "?":
			info.Untracked++
			path = rest
			code = "??"
			kind = ChangeUntracked
		default:
			// # headers (branch.oid, branch.head, …) and ! ignored entries.
			continue
		}

		if maxFiles > 0 && len(info.Files) >= maxFiles {
			info.Truncated = true
			continue
		}
		info.Files = append(info.Files, ChangedFile{Path: path, Code: code, Kind: kind})
	}

	statChangedFiles(root, &info)
	return info
}

// xyCode extracts the two-letter status code that starts every ordinary and
// unmerged record, falling back to the caller's default when a record is
// shorter than git promised — a malformed line is still a change someone
// should see.
func xyCode(rest, fallback string) string {
	if len(rest) >= 2 {
		return rest[:2]
	}
	return fallback
}

// fieldTail takes the final field of a porcelain v2 record, given how many
// space-separated fields the record splits into. Paths can contain spaces,
// so the split is bounded, not greedy — and for rename records a tab
// separates the new path from the original, of which the new path is the
// one a user sees in the working tree.
func fieldTail(rest string, fieldCount int) string {
	parts := strings.SplitN(rest, " ", fieldCount)
	last := parts[len(parts)-1]
	if newPath, _, ok := strings.Cut(last, "\t"); ok {
		return newPath
	}
	return last
}

// statChangedFiles timestamps the listed files. This is what makes
// "modified in the last hour" work at all (spec §7.6): a working-tree edit
// touches nothing under .git, so a dirty repo's activity can only come from
// the files themselves. Only listed files are statted — on a capped scan
// the overflow is countable but invisible, and statting paths git named but
// the view will never show is wasted syscalls on exactly the repos that
// have the most changes.
func statChangedFiles(root string, info *WorkInfo) {
	var newest time.Time
	for i := range info.Files {
		f := &info.Files[i]
		fi, err := os.Stat(filepath.Join(root, strings.TrimSuffix(f.Path, "/")))
		if err != nil {
			continue // deleted or renamed-away: no mtime to speak of
		}
		f.Mtime = fi.ModTime()
		if f.Mtime.After(newest) {
			newest = f.Mtime
		}
	}
	info.NewestMtime = newest
}
