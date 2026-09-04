package watcher

// Watch-set composition (spec §13). A repo's watch set is a short list of
// logical targets; each Recursive target is expanded into one fsnotify watch
// per directory at registration time, because fsnotify offers no recursive
// mode — and that is deliberate, not a workaround. The inotify-exhaustion
// incident happened because a whole scan root was watched recursively and
// .git/objects growth ate the kernel budget within a thousand repos. Here
// nothing under .git/objects is ever watched, working trees are pruned and
// capped, and the ~10–20 watches a repo actually needs are immune to object
// growth.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// WatchTarget is one entry of a repo's logical watch set. A non-recursive
// target registers a single watch on Path; a recursive target is expanded
// into one watch per directory beneath Path before registration.
type WatchTarget struct {
	Path      string
	Recursive bool
}

// maxWorkingDirsPerRepo bounds the working-tree walk per repo. A repo whose
// tree exceeds the cap simply loses watches for the surplus directories and
// degrades to sweep-driven updates for them — the alternative, unbounded
// registration, is exactly the failure the design exists to prevent. Git
// trees (refs/logs/worktrees) are small by nature and bounded by the same
// per-walk limit, so a hostile repository cannot exhaust the budget either.
const maxWorkingDirsPerRepo = 512

// gitRecursiveSubs are the directories inside a git dir worth a recursive
// watch set. Ref updates, reflog writes and worktree bookkeeping are the
// git-side signals that change an answer; everything else under .git either
// sits in the non-recursive allowlist (HEAD, index, FETCH_HEAD, …) or is
// noise (objects above all).
var gitRecursiveSubs = []string{"refs", "logs", "worktrees"}

// repoWatch is the composed watch set for one repository, plus the location
// of its git dir. GitDir is needed later to route events through the §13
// allowlist: for a checkout it is the repo's .git, for a worktree or
// submodule it is the directory the .git file points at, and for a bare repo
// it is the repo root itself. An empty Targets means the repo is not
// watchable and gets no watches at all.
type repoWatch struct {
	Targets []WatchTarget
	GitDir  string
}

// errCapReached aborts a directory walk once the per-repo budget is spent.
// It never escapes the package: a capped walk is a degraded watch set, not a
// failure.
var errCapReached = errors.New("watcher: per-repo watch cap reached")

// watchSetFor composes the logical watch set for one repository (spec §13):
// repo root non-recursive, working directories non-recursive (pruned and
// capped), .git non-recursive, and refs/logs/worktrees recursive when
// present. Bare repos — HEAD plus refs, no .git, the bare-plus-worktrees
// container layout — get the root and the recursive trio. Missing or
// unreadable repos produce an empty set: a repo that vanished mid-sweep must
// not fail a reconcile.
func watchSetFor(repo string, prune map[string]bool) repoWatch {
	st, err := os.Stat(repo)
	if err != nil || !st.IsDir() {
		return repoWatch{}
	}

	gitPath := filepath.Join(repo, ".git")
	gitSt, gitErr := os.Stat(gitPath)
	switch {
	case gitErr == nil && gitSt.IsDir():
		return repoWatch{
			Targets: appendGitTargets(
				append([]WatchTarget{{Path: repo}}, workingTargets(repo, prune)...),
				gitPath,
			),
			GitDir: gitPath,
		}
	case gitErr == nil:
		// .git is a file: a linked worktree or a submodule, whose real git
		// dir lives where the file says. The working tree still needs the
		// full treatment; the git-side signals only exist if the pointer
		// resolves to a readable directory.
		gd, ok := gitDirFromFile(gitPath, repo)
		if !ok {
			return repoWatch{
				Targets: append([]WatchTarget{{Path: repo}}, workingTargets(repo, prune)...),
			}
		}
		return repoWatch{
			Targets: appendGitTargets(
				append([]WatchTarget{{Path: repo}}, workingTargets(repo, prune)...),
				gd,
			),
			GitDir: gd,
		}
	default:
		if !bareShape(repo) {
			// No .git and no bare shape: not a repository anymore, or never
			// was one. Empty set — the next sweep decides what it is.
			return repoWatch{}
		}
		targets := []WatchTarget{{Path: repo}}
		for _, sub := range gitRecursiveSubs {
			p := filepath.Join(repo, sub)
			if fi, err := os.Stat(p); err == nil && fi.IsDir() {
				targets = append(targets, WatchTarget{Path: p, Recursive: true})
			}
		}
		return repoWatch{Targets: targets, GitDir: repo}
	}
}

// appendGitTargets adds the git-dir portion of a watch set: the git dir
// itself non-recursive (HEAD, index, packed-refs and FETCH_HEAD all live
// directly in it) plus a recursive target for each of the signal
// directories that exists. It assumes targets already holds the root.
func appendGitTargets(targets []WatchTarget, gitDir string) []WatchTarget {
	targets = append(targets, WatchTarget{Path: gitDir})
	for _, sub := range gitRecursiveSubs {
		p := filepath.Join(gitDir, sub)
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			targets = append(targets, WatchTarget{Path: p, Recursive: true})
		}
	}
	return targets
}

// bareShape reports whether dir looks like a bare repository — a HEAD file
// and a refs directory with no .git — mirroring discovery's repo test so
// both layers agree on what a bare-plus-worktrees container is.
func bareShape(dir string) bool {
	if fi, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil || fi.IsDir() {
		return false
	}
	fi, err := os.Stat(filepath.Join(dir, "refs"))
	return err == nil && fi.IsDir()
}

// gitDirFromFile resolves a .git file's "gitdir: <path>" pointer, relative
// paths being read against the worktree. A pointer that cannot be read is
// not an error — the repo simply gets no git-side watches, and the next
// reconcile tries again.
func gitDirFromFile(gitFile, repo string) (string, bool) {
	raw, err := os.ReadFile(gitFile)
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(string(raw))
	rest, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return "", false
	}
	p := strings.TrimSpace(rest)
	if p == "" {
		return "", false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(repo, p)
	}
	return p, true
}

// workingTargets walks a repo's working tree and returns one non-recursive
// target per directory, in walk order. The walk stops at the first repo
// boundary — a nested checkout or bare repo owns its own watch set, and its
// paths must not be double-counted against the parent's budget — and skips
// pruned directories entirely, since no answer the dashboard renders depends
// on node_modules churn. The walk is capped: when the budget is spent the
// remaining tree is simply not watched and those directories degrade to
// sweep-driven updates.
func workingTargets(repo string, prune map[string]bool) []WatchTarget {
	var rels []string
	fs.WalkDir(os.DirFS(repo), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			// One unreadable directory in a thousand-checkout fleet must not
			// abort the walk; skip the subtree and keep going.
			return fs.SkipDir
		}
		if rel == "." {
			return nil // the root is already its own target
		}
		if d.IsDir() {
			if d.Name() == ".git" || prune[d.Name()] {
				return fs.SkipDir
			}
			if hasRepoMarker(filepath.Join(repo, filepath.FromSlash(rel))) {
				return fs.SkipDir
			}
			if len(rels) >= maxWorkingDirsPerRepo {
				return errCapReached
			}
			rels = append(rels, rel)
			return nil
		}
		return nil
	})
	targets := make([]WatchTarget, 0, len(rels))
	for _, rel := range rels {
		targets = append(targets, WatchTarget{Path: filepath.Join(repo, filepath.FromSlash(rel))})
	}
	return targets
}

// hasRepoMarker reports whether dir is itself a repository — a checkout (any
// .git entry, file or directory) or a bare shape. Symlinks are never
// followed: watching through a link to a deep tree would silently re-create
// the unbounded watch the design forbids.
func hasRepoMarker(dir string) bool {
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
		return true
	}
	return bareShape(dir)
}

// expandTargets turns logical targets into the concrete list of directories
// to register. Recursive targets are walked with one watch per directory —
// fsnotify exposes no recursive mode, and hand-rolling here is what keeps
// every individual watch small and every watch set auditable. Duplicate
// paths collapse: a linked worktree's git dir frequently lives inside the
// main repo's .git/worktrees, which the main repo's set already covers.
func expandTargets(targets []WatchTarget, cap int) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, t := range targets {
		if !t.Recursive {
			add(t.Path)
			continue
		}
		fs.WalkDir(os.DirFS(t.Path), ".", func(rel string, d fs.DirEntry, err error) error {
			if err != nil {
				return fs.SkipDir
			}
			if !d.IsDir() {
				return nil
			}
			if len(out) >= cap {
				return errCapReached
			}
			if rel == "." {
				add(t.Path)
				return nil
			}
			add(filepath.Join(t.Path, filepath.FromSlash(rel)))
			return nil
		})
	}
	return out
}
