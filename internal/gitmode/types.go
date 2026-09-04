// Package gitmode contains the local git probes: everything derivable from a
// checkout's `.git` directory and working tree without touching a provider.
// It is the Go port of drydock's git.rs and probe.rs tiers, and the behavioral
// contract is GO-PORT-SPEC.md §6–§8 and §7 (the git semantics that MUST
// survive the port — each numbered rule there has an incident behind it).
package gitmode

import "time"

// Head is where a checkout's HEAD points.
type HeadKind string

const (
	HeadBranch   HeadKind = "branch"   // on a branch, e.g. develop
	HeadDetached HeadKind = "detached" // detached at a commit
	HeadUnborn   HeadKind = "unborn"   // a fresh repo with no commits yet
)

type Head struct {
	Kind   HeadKind
	Branch string // when Kind == HeadBranch
	SHA    string // when Kind == HeadDetached
}

// Label renders HEAD the way the detail view and JSON show it: the branch
// name, `@sha` when detached, `(unborn)` for a repo with no commits.
func (h Head) Label() string {
	switch h.Kind {
	case HeadBranch:
		return h.Branch
	case HeadDetached:
		return "@" + h.SHA
	default:
		return "(unborn)"
	}
}

// BranchName is the branch name when HEAD is on one, empty otherwise.
func (h Head) BranchName() string {
	if h.Kind == HeadBranch {
		return h.Branch
	}
	return ""
}

// Operation is a git operation left half-finished in the working tree,
// detected from its marker files under .git.
type Operation string

const (
	OpMerge      Operation = "merge"
	OpRebase     Operation = "rebase"
	OpCherryPick Operation = "cherry-pick"
	OpRevert     Operation = "revert"
	OpBisect     Operation = "bisect"
)

// Label renders the operation the way the state column shows it: "merging",
// "rebasing", "cherry-picking", "reverting", "bisecting".
func (o Operation) Label() string {
	switch o {
	case OpMerge:
		return "merging"
	case OpRebase:
		return "rebasing"
	case OpCherryPick:
		return "cherry-picking"
	case OpRevert:
		return "reverting"
	case OpBisect:
		return "bisecting"
	}
	return string(o)
}

// TagInfo is a tag name paired with its creation time.
type TagInfo struct {
	Name string
	At   time.Time
}

// CommitInfo is one commit: sha, author time, subject, author.
type CommitInfo struct {
	SHA     string
	At      time.Time
	Subject string
	Author  string
}

// ChangelogInfo is what the top block of CHANGELOG.md claims, versus the
// newest tag. A changelog whose top version has no matching tag is an
// in-flight release waiting to be cut.
type ChangelogInfo struct {
	Version string
	Tagged  bool
	// UnreleasedBlocks counts distinct unreleased version headings found
	// above the last tagged one. More than one means blocks stacked up.
	UnreleasedBlocks int
}

// BranchInfo is one local branch and where it stands against its upstream.
// Ahead/Behind are only as fresh as the last fetch (spec §7.2).
type BranchInfo struct {
	Name        string
	Upstream    string // empty when there is no upstream
	Ahead       int
	Behind      int
	Gone        bool // upstream configured but no longer exists on the remote
	CommittedAt time.Time
	SHA         string
	Subject     string
}

// ChangeKind classifies one changed file, from its porcelain v2 status.
type ChangeKind string

const (
	ChangeStaged     ChangeKind = "staged"
	ChangeUnstaged   ChangeKind = "unstaged"
	ChangeUntracked  ChangeKind = "untracked"
	ChangeConflicted ChangeKind = "conflicted"
)

// ChangedFile is one entry of the working-tree scan: the repo-relative path,
// the raw two-letter XY code from porcelain v2 (e.g. `M.`, `.M`, `??`), the
// derived kind, and the file's mtime — the mtime is what makes "modified in
// the last hour" work for a dirty repo.
type ChangedFile struct {
	Path  string
	Code  string
	Kind  ChangeKind
	Mtime time.Time
}

// WorkInfo is the tier-2 working-tree scan: the expensive half, cached
// against a WorkKey and re-run only where something moved.
type WorkInfo struct {
	Staged    int
	Unstaged  int
	Untracked int
	Conflicts int
	// NewestMtime is the newest mtime across changed files.
	NewestMtime time.Time
	Files       []ChangedFile
	Truncated   bool
}

// Total is every outstanding change, of every kind.
func (w WorkInfo) Total() int {
	return w.Staged + w.Unstaged + w.Untracked + w.Conflicts
}

// Dirty is true when anything is outstanding in the working tree.
func (w WorkInfo) Dirty() bool { return w.Total() > 0 }

// WorkKey is the cache-validity key for tier 2: if HEAD and the index are
// unchanged, a cached working-tree result is still good.
type WorkKey struct {
	HeadSHA    string
	IndexMtime time.Time
	IndexSize  int64
	HasIndex   bool
}

// ActivitySource explains why a repo's activity timestamp is what it is, so
// the age column can always show its provenance.
type ActivitySource string

const (
	ActivityCommit      ActivitySource = "last commit"
	ActivityWorkingTree ActivitySource = "file edit"
	ActivityUnknown     ActivitySource = "unknown"
)

// ReleaseState is where a repo stands against its own release history — a
// separate axis from the working state. A repo can be dirty and released, or
// spotless and still needing a release.
type ReleaseState string

const (
	ReleaseUnreleased   ReleaseState = "unreleased"    // no tags at all
	ReleaseReleased     ReleaseState = "released"      // tagged, nothing since
	ReleaseNeedsRelease ReleaseState = "needs-release" // tagged, work since
)

// Visibility is a repo's visibility on its hosting service, as last observed
// via the provider CLI. Nothing under .git records it; it can only come from
// asking the host.
type Visibility string

const (
	VisibilityPublic   Visibility = "public"
	VisibilityPrivate  Visibility = "private"
	VisibilityInternal Visibility = "internal"
)

// VisibilityStatus carries the check outcome with its provenance: a failed
// check is a stored fact ("check failed"), never a guess.
type VisibilityStatus string

const (
	VisKnown        VisibilityStatus = "known"
	VisCheckFailed  VisibilityStatus = "check failed"
	VisDisabled     VisibilityStatus = "checking disabled"
	VisNeverChecked VisibilityStatus = "never checked"
)

// VisibilityInfo pairs a visibility verdict with when it was checked.
type VisibilityInfo struct {
	Status    VisibilityStatus
	Seen      Visibility
	CheckedAt time.Time
	Error     string
}

// RefsInfo is tier 1: everything derivable from .git without touching the
// working tree.
type RefsInfo struct {
	Head             Head
	Branches         []BranchInfo
	LastCommit       *CommitInfo
	Stashes          int
	Operation        Operation // empty Operation when none is in progress
	NewestTag        *TagInfo  // newest tag by creation date, reachable or not
	DescribedTag     *TagInfo  // nearest tag reachable from HEAD
	CommitsSinceTag  int       // commits on HEAD since DescribedTag
	SinceTagSubjects []string  // newest first, capped
	TagsOrphaned     bool      // newest tag sits on history no branch reaches
	IndexMtime       time.Time
	FetchedAt        time.Time // zero means it never fetched
	RemoteURL        string
	Changelog        *ChangelogInfo
	IsBare           bool
	IsShallow        bool
}

// Unpushed totals commits sitting on local branches that their upstreams
// don't have.
func (r RefsInfo) Unpushed() int {
	n := 0
	for _, b := range r.Branches {
		n += b.Ahead
	}
	return n
}

// Unpulled totals commits upstreams have that we don't. Only as fresh as
// the last fetch.
func (r RefsInfo) Unpulled() int {
	n := 0
	for _, b := range r.Branches {
		n += b.Behind
	}
	return n
}

// CurrentBranch finds the BranchInfo for whichever branch HEAD is on.
func (r RefsInfo) CurrentBranch() *BranchInfo {
	name := r.Head.BranchName()
	for i := range r.Branches {
		if r.Branches[i].Name == name {
			return &r.Branches[i]
		}
	}
	return nil
}

// TagOffBranch is true when the newest tag isn't reachable from HEAD. Normal
// in git-flow (tags land on master, work continues on develop) but it means
// "commits since tag" needs reading with care. An orphaned tag never counts
// as off-branch: it isn't a release of anything checked out.
func (r RefsInfo) TagOffBranch() bool {
	if r.TagsOrphaned {
		return false
	}
	if r.NewestTag == nil || r.DescribedTag == nil {
		return r.NewestTag != nil
	}
	return r.NewestTag.Name != r.DescribedTag.Name
}

// NewestCommitAt is the newest commit time across all local branches.
func (r RefsInfo) NewestCommitAt() time.Time {
	var newest time.Time
	for _, b := range r.Branches {
		if b.CommittedAt.After(newest) {
			newest = b.CommittedAt
		}
	}
	return newest
}
