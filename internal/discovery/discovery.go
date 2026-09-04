// Package discovery walks the configured scan roots and enumerates git
// repositories. Every sweep starts from the filesystem, never from a cached
// list: repos cloned since the last sweep appear automatically and repos
// deleted from disk drop off the dashboard (spec §5).
//
// The single most important rule lives here: walking stops the moment a repo
// is found. That one rule keeps submodules, vendored trees and nested
// checkouts out of the results, and it is why the walk is hand-rolled instead
// of filepath.WalkDir — the stdlib walker offers no "do not descend" veto.
package discovery

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// DefaultPrune lists directory names that are never descended into, no matter
// what the config says. Dependency trees and build output can bury hundreds of
// stray directories per repo; descending into them wastes a scan across a
// fleet of 500–1000 repos and risks mistaking vendored checkouts for real
// ones. Exported so the watcher can reuse the same set when bounding
// per-repo watch trees.
var DefaultPrune = []string{
	"node_modules", "vendor", "target", "bower_components", "Pods",
	"Carthage", ".build", ".venv", "venv", "__pycache__", ".terraform",
	"dist", "build", ".next", ".nuxt", ".cache", ".tox", "DerivedData",
}

// Options carries everything discovery needs, deliberately free of any config
// dependency: callers translate their own configuration into this struct.
type Options struct {
	// Roots are the directories to scan. A leading ~ is expanded here.
	// Missing roots are skipped, not errors — a configured root on an
	// unmounted volume must not take the dashboard down.
	Roots []string
	// MaxDepth is how deep below a root to look: 1 examines only the root's
	// immediate children. Zero or negative means unbounded; the config layer
	// is responsible for supplying the default.
	MaxDepth int
	// FollowNestedRepos descends into a found repo looking for more repos.
	// Off by default: stopping at the first repo is the rule that keeps
	// submodules and vendored checkouts out of the list (spec §5).
	FollowNestedRepos bool
	// FollowSymlinks follows symlinked directories while scanning. Off by
	// default so a stray link to a deep tree cannot explode a sweep.
	FollowSymlinks bool
	// Exclude holds doublestar glob patterns matched against each
	// directory's path relative to the root. A pattern ending in "/**" also
	// excludes the named directory itself, so "scratch/**" removes scratch
	// and everything under it, not just its contents.
	Exclude []string
	// Prune adds directory names that are never descended into, on top of
	// DefaultPrune. Names, not globs: prune is about well-known heavy
	// directories, exclude is about arbitrary paths.
	Prune []string
}

// Repo is one discovered repository. Group is the first path segment below
// the root — each immediate subdir of a root is a "group" (spec §4) — and
// Name is the rest. A repo directly in a root has no group.
type Repo struct {
	Root  string
	Group string
	Name  string
}

// Stats reports what a sweep touched. DirsWalked and Pruned exist so the
// dashboard can show that a sweep did work even when it found nothing new.
type Stats struct {
	DirsWalked int
	ReposFound int
	Pruned     int
}

// Discover walks every root and returns the repositories it found, sorted by
// root path so output is deterministic regardless of directory read order.
// Overlapping roots do not double-list a repo; the first root to reach it
// wins. The only error this can return is a failure to resolve ~ in a root —
// unreadable directories are skipped, because one permission mistake in a
// fleet of a thousand checkouts must not abort a sweep.
func Discover(opts Options) ([]Repo, Stats, error) {
	prune := make(map[string]struct{}, len(DefaultPrune)+len(opts.Prune)+1)
	for _, name := range DefaultPrune {
		prune[name] = struct{}{}
	}
	for _, name := range opts.Prune {
		prune[name] = struct{}{}
	}
	// .git is pruned in addition to the lists above: a .git directory holds
	// HEAD and refs, which is exactly the bare-repo signature, so descending
	// into it would misreport every checkout's git dir as a bare repo.
	prune[".git"] = struct{}{}

	w := &walker{
		opts:  opts,
		prune: prune,
		seen:  map[string]struct{}{},
	}
	for _, root := range opts.Roots {
		expanded, err := expandHome(root)
		if err != nil {
			return nil, w.stats, err
		}
		abs, err := filepath.Abs(expanded)
		if err != nil {
			continue
		}
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			continue // missing roots are skipped, not errors
		}
		w.scanRoot(abs)
	}
	// Roots are walked in sorted order, so the first root to claim a
	// (group, name) is deterministic.
	sort.Slice(w.repos, func(i, j int) bool { return w.repos[i].Root < w.repos[j].Root })

	// The same checkout (same group and name) reachable through two roots
	// must list once: a mis-aimed org registration once cloned a whole
	// organization into a second tree, and the fleet doubled overnight.
	// The first root in sorted order wins — the established copy, not the
	// accident. Case-insensitive: groups are org logins, and logins differ
	// only in case between registrations.
	seenProject := make(map[string]struct{}, len(w.repos))
	unique := w.repos[:0]
	for _, r := range w.repos {
		key := strings.ToLower(r.Group) + "\x00" + strings.ToLower(r.Name)
		if _, dup := seenProject[key]; dup {
			w.stats.ReposFound--
			continue
		}
		seenProject[key] = struct{}{}
		unique = append(unique, r)
	}
	w.repos = unique
	return w.repos, w.stats, nil
}

type walker struct {
	opts  Options
	prune map[string]struct{}
	repos []Repo
	stats Stats
	seen  map[string]struct{}
}

// scanRoot checks whether the root itself is a repo (no group — the repo's
// name is the root's base name) and then walks below it.
func (w *walker) scanRoot(abs string) {
	if isRepo(abs) {
		w.record(abs, "", filepath.Base(abs))
		if !w.opts.FollowNestedRepos {
			return
		}
	}
	w.walk(abs, "", 0)
}

// walk visits the directory at abs, which sits depth levels below its root
// and at rel (slash-separated) relative to it. Only directories matter here:
// files are invisible to discovery, they merely ride along in the entries.
func (w *walker) walk(abs, rel string, depth int) {
	entries, err := os.ReadDir(abs)
	if err != nil {
		return // unreadable directory: skip it, keep the sweep alive
	}
	w.stats.DirsWalked++

	for _, entry := range entries {
		name := entry.Name()
		if _, ok := w.prune[name]; ok {
			w.stats.Pruned++
			continue
		}
		childRel := name
		if rel != "" {
			childRel = rel + "/" + name
		}

		isLink := entry.Type()&os.ModeSymlink != 0
		if isLink && !w.opts.FollowSymlinks {
			continue
		}
		var info os.FileInfo
		if isLink {
			info, err = os.Stat(filepath.Join(abs, name))
		} else {
			info, err = entry.Info()
		}
		if err != nil || !info.IsDir() {
			continue // broken link or plain file
		}
		if w.excluded(childRel) {
			continue
		}
		if w.opts.MaxDepth > 0 && depth+1 > w.opts.MaxDepth {
			continue
		}

		child := filepath.Join(abs, name)
		if isRepo(child) {
			group, repoName := splitGroup(childRel)
			w.record(child, group, repoName)
			if !w.opts.FollowNestedRepos {
				continue
			}
		}
		w.walk(child, childRel, depth+1)
	}
}

// record adds a repo unless an earlier root already claimed it.
func (w *walker) record(abs, group, name string) {
	if _, dup := w.seen[abs]; dup {
		return
	}
	w.seen[abs] = struct{}{}
	w.repos = append(w.repos, Repo{Root: abs, Group: group, Name: name})
	w.stats.ReposFound++
}

// excluded reports whether a directory's root-relative path matches any
// exclude glob. Pattern errors are treated as non-matches: a malformed glob
// must not abort a sweep, and config validation is the config package's job.
func (w *walker) excluded(rel string) bool {
	for _, pattern := range w.opts.Exclude {
		if match, _ := doublestar.Match(pattern, rel); match {
			return true
		}
		// "sub/**" matches everything under sub but (in doublestar) not sub
		// itself; the spec wants the named directory gone too.
		if strings.HasSuffix(pattern, "/**") {
			if base := strings.TrimSuffix(pattern, "/**"); base != "" && rel == base {
				return true
			}
		}
	}
	return false
}

// isRepo reports whether dir is a checkout or a bare repository. A checkout
// is any directory containing .git — directory or file, since worktrees and
// submodules carry a .git file pointing at their git dir. A bare repo is a
// directory with a HEAD file and a refs directory and no .git: it is the
// container of a bare-plus-worktrees layout and must appear on the dashboard.
func isRepo(dir string) bool {
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
		return true
	}
	head, err := os.Stat(filepath.Join(dir, "HEAD"))
	if err != nil || head.IsDir() {
		return false
	}
	refs, err := os.Stat(filepath.Join(dir, "refs"))
	return err == nil && refs.IsDir()
}

// splitGroup splits a root-relative repo path into group and name: the first
// segment is the group, the rest is the name. A single segment means the repo
// sits directly in the root — no group, the segment is the name.
func splitGroup(rel string) (group, name string) {
	segs := strings.Split(rel, "/")
	if len(segs) == 1 {
		return "", segs[0]
	}
	return segs[0], strings.Join(segs[1:], "/")
}

// expandHome resolves a leading ~ to the user's home directory, so configs
// written as "~/Projects" survive any process environment.
func expandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
}
