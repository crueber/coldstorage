// This file implements the fingerprint gate (GO-PORT-SPEC.md §6): a cheap,
// process-stable hash of everything under .git that moves when probe-worthy
// state moves. A repo whose fingerprint matches the cached row is skipped
// without spawning a single process — the entire scale story of the tool
// rests on this hash being both sensitive (a new commit, a new ref, a fetch
// all flip it) and inert (nothing in the working tree touches it).
//
// The inputs are mtimes, deliberately: file mtimes are what the filesystem
// hands us for free, on every repo, in microseconds. Content hashes would be
// exact but cost a full read of .git across the whole fleet on every sweep —
// the thing the gate exists to avoid.
package gitmode

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FNV-1a's 64-bit parameters. Chosen over the stdlib's hash/fnv because the
// folding below mixes fixed-width integers, not byte slices, and staying
// dependency-free and branch-free in the hot loop is worth the ten lines.
const (
	fnvOffset uint64 = 0xcbf29ce484222325
	fnvPrime  uint64 = 0x100000001b3
)

func fnvByte(h uint64, b byte) uint64 {
	h ^= uint64(b)
	return h * fnvPrime
}

func fnvString(h uint64, s string) uint64 {
	for i := range len(s) {
		h = fnvByte(h, s[i])
	}
	return h
}

// fnvUint64 folds a fixed-width integer little-endian, the same way on every
// platform — this is what makes the fingerprint comparable across processes,
// and across machines if the cache is ever shared.
func fnvUint64(h uint64, v uint64) uint64 {
	for range 8 {
		h = fnvByte(h, byte(v))
		v >>= 8
	}
	return h
}

// fingerprintFiles are the top-level .git files whose mtimes make up the
// cheap half of the fingerprint. Each name is folded in before its state so
// that a missing file is a distinct, per-file value — a repo that has never
// fetched (no FETCH_HEAD) must not read as identical to one whose
// FETCH_HEAD happens to hash the same as the sentinel.
var fingerprintFiles = []string{"HEAD", "index", "packed-refs", "FETCH_HEAD", "config"}

// fingerprintTrees get a bounded recursive max-mtime instead. Depth 3 covers
// refs/heads/feature and logs/refs/heads/feature, which is deeper than real
// fleets grow: refs are rewritten in place on every update and directories
// do not move, so without descending, a commit on an existing branch would
// leave refs/heads' mtime untouched and the gate would swallow the change.
// Both trees are tiny (one log line per ref update, one file per ref), so
// the walk is a handful of stat calls, not a scan.
var fingerprintTrees = []struct {
	name  string
	depth int
}{
	{"refs", 3},
	{"logs", 3},
}

// Fingerprint hashes the checkout's git-state inputs into a single uint64.
// It never runs a process and never returns an error: a file that does not
// exist yet folds in as a distinct missing sentinel (its absence is state —
// the first fetch creates FETCH_HEAD and must move the fingerprint), and an
// unreadable tree contributes nothing.
//
// The documented trade (spec §6): unstaged working-tree edits do not move
// the fingerprint. Working-tree files live outside .git, and tracking them
// here would turn every save in an editor into a full re-probe. Watching
// those edits is the watcher's job (spec §13), which bypasses this gate
// anyway.
func Fingerprint(root string) uint64 {
	gd := gitDir(root)
	common := commonDir(gd)

	h := fnvOffset
	for _, name := range fingerprintFiles {
		h = fnvString(h, name)
		if fi, err := os.Stat(filepath.Join(common, name)); err == nil {
			h = fnvByte(h, 1)
			h = fnvUint64(h, uint64(fi.ModTime().UnixNano()))
		} else {
			h = fnvByte(h, 0)
		}
	}

	for _, tree := range fingerprintTrees {
		h = fnvString(h, tree.name)
		h = fnvUint64(h, treeMaxMtime(common, tree.name, tree.depth))
	}
	return h
}

// treeMaxMtime returns the newest file mtime under gd/name, descending at
// most maxDepth levels below it, or 0 when the tree is missing or empty.
// Unreadable entries are skipped rather than fatal: a concurrent git GC
// deleting a file mid-walk must not turn into a failed probe.
func treeMaxMtime(gd, name string, maxDepth int) uint64 {
	walkRoot := filepath.Join(gd, name)
	var newest uint64
	err := filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == walkRoot {
			return nil
		}
		rel, relErr := filepath.Rel(walkRoot, path)
		if relErr != nil {
			return nil
		}
		depth := strings.Count(rel, string(filepath.Separator)) + 1
		if d.IsDir() {
			if depth >= maxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if fi, infoErr := d.Info(); infoErr == nil {
			if t := uint64(fi.ModTime().UnixNano()); t > newest {
				newest = t
			}
		}
		return nil
	})
	if err != nil {
		return 0
	}
	return newest
}
