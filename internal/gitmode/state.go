// This file derives the coarse per-repo verdicts (GO-PORT-SPEC.md §8): the
// one-word state the table's STATE column shows, and the release verdict the
// filters and the releasable view are built on. Both are pure functions of
// the probe results so that every view — table, detail, JSON — renders the
// same structure the same way.
package gitmode

// StateLabel renders the per-repo state with the precedence the spec fixes
// (§8), first match wins: error → conflict → the in-progress operation's
// label → dirty → unpushed → bare → "…" (never scanned) → clean.
//
// The error step lives with the caller, not here: the data model carries a
// probe error outside RefsInfo and WorkInfo (both survive a failed probe
// with their last good values), so an errored repo is checked before this
// function is reached, and everything below is the precedence that remains.
//
// The bare check sits after unpushed on purpose: a bare repo holding
// branches its remote hasn't seen is still worth saying so about. It has no
// working tree, though — it never runs tier 2 — so without this ordering the
// "never scanned" ellipsis below would be permanent for bare repos.
func StateLabel(r RefsInfo, w *WorkInfo) string {
	if w != nil && w.Conflicts > 0 {
		return "conflict"
	}
	if r.Operation != "" {
		return r.Operation.Label()
	}
	if w != nil && w.Dirty() {
		return "dirty"
	}
	if r.Unpushed() > 0 {
		return "unpushed"
	}
	if r.IsBare {
		return "bare"
	}
	if w == nil {
		return "…"
	}
	return "clean"
}

// ReleaseStateOf places a repo against its own release history — a
// separate axis from the working state, because a repo can be dirty and
// released, or spotless and still needing a release. It is the work-aware
// sibling of release.go's ReleaseStateOf, which judges commits alone: dirty
// work past the newest reachable tag is also needs-release here, because
// the tree has moved on from the tag whether or not it has been committed.
func ReleaseStateOf(r RefsInfo, w *WorkInfo) ReleaseState {
	if r.NewestTag == nil {
		return ReleaseUnreleased
	}
	if r.CommitsSinceTag > 0 || (w != nil && w.Dirty()) {
		return ReleaseNeedsRelease
	}
	return ReleaseReleased
}
