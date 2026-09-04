package orgsync

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crueber/coldstorage/internal/gitmode"
)

// Opts carries what the executor needs that the plan does not hold: where
// checkouts live and the budget for one git operation (remote.timeout for
// the network-bound clone and pull calls, §2).
type Opts struct {
	Path    string
	Timeout time.Duration
}

// ListFn is the listing seam: ListRepos in production, a stub in tests, and
// whatever the TUI wants to compose otherwise.
type ListFn func(src Source, timeout time.Duration) ([]Repo, error)

// Outcome is one report row (§11.5). Action is one of cloned, updated,
// current, skipped, orphaned, error.
type Outcome struct {
	Action string
	Name   string
	Detail string
}

// SyncPlan is a plan plus the story of how it was reached. Err is non-nil
// when the listing failed and the plan degraded to update-only (§11.3) —
// Rows then holds the leading error row the report must print, so a failed
// listing can never read as "the whole org is orphans".
type SyncPlan struct {
	Plan Plan
	Err  error
	Rows []Outcome
}

// PlanSync orchestrates one planning pass: ask the provider, ask the disk,
// diff. It is the seam tests plug stubs into. A listing failure is returned
// as an error, not swallowed here — ListSync is the wrapper that decides
// the degradation — and a missing checkout path fails loudly (missing org
// path fails loudly, §11.3).
func PlanSync(src Source, opts Opts, diskFn func(string) []string, list ListFn) (SyncPlan, error) {
	if opts.Path == "" {
		return SyncPlan{}, fmt.Errorf("org %q: no checkout path configured", src.Owner)
	}
	// A checkout path that does not exist yet is the NORMAL state for a new
	// registration: creating it by cloning is the whole point of the first
	// sync. Only an unresolvable path (no configured path AND no roots to
	// derive one from) is an error. The Rust original had this same rule.
	if list == nil {
		list = ListRepos
	}
	repos, err := list(src, opts.Timeout)
	if err != nil {
		return SyncPlan{}, fmt.Errorf("org %q: listing failed: %w", src.Owner, err)
	}
	return SyncPlan{Plan: NewPlan(repos, diskFn(opts.Path), src)}, nil
}

// ListSync wraps PlanSync with the §11.3 degradation rule: a failed listing
// becomes an update-only plan — clones dropped, orphans empty — with a
// leading error row. A successful-but-empty listing is trusted, and still
// produces real orphans. ListSync never returns an error; every failure is
// already a row the report can print.
func ListSync(src Source, opts Opts, diskFn func(string) []string, list ListFn) SyncPlan {
	sp, err := PlanSync(src, opts, diskFn, list)
	if err == nil {
		return sp
	}
	return SyncPlan{
		Plan: Plan{ToUpdate: diskFnOf(opts.Path, diskFn)},
		Err:  err,
		Rows: []Outcome{{Action: "error", Name: src.Owner, Detail: err.Error()}},
	}
}

// diskFnOf guards the degraded path the same way PlanSync does: a missing
// or unknown path contributes no disk rows instead of panicking the report.
func diskFnOf(path string, diskFn func(string) []string) []string {
	if diskFn == nil || path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	return diskFn(path)
}

// Execute runs the plan, strictly serially (§11.3): clones first, then
// updates, each bucket in the plan's name order, one git operation at a
// time no matter what any config says — concurrency here is how a fleet of
// checkouts turns into a lock-contention storm. progress fires once per
// clone/update, one at a time, before the operation runs. Orphaned and
// skipped rows carry no work and need no progress. No outcome ever aborts
// the batch; nothing is ever deleted.
func Execute(src Source, plan Plan, opts Opts, progress func(done, total int, label string)) []Outcome {
	total := len(plan.ToClone) + len(plan.ToUpdate)
	done := 0
	step := func(label string) {
		done++
		if progress != nil {
			progress(done, total, label)
		}
	}

	out := make([]Outcome, 0, total+len(plan.Orphans)+len(plan.Skipped))
	for _, r := range plan.ToClone {
		step(r.Name)
		out = append(out, cloneOne(src, r, opts))
	}
	for _, name := range plan.ToUpdate {
		step(name)
		out = append(out, updateOne(opts, name))
	}
	for _, name := range plan.Orphans {
		out = append(out, Outcome{
			Action: "orphaned",
			Name:   name,
			Detail: "on disk but not in the listing; nothing touched",
		})
	}
	for _, s := range plan.Skipped {
		out = append(out, Outcome{Action: "skipped", Name: s.Repo.Name, Detail: s.Reason})
	}
	return out
}

// cloneOne clones one repo into <path>/<name>. Empty URL is a loud error
// row, never a doomed clone (§11.3). A pre-existing directory is never
// touched — it is somebody's checkout, and deleting it is how work is lost.
// A failed clone removes the partial directory the clone itself created and
// nothing else.
func cloneOne(src Source, r Repo, opts Opts) Outcome {
	cloneURL := r.cloneURL(src.Protocol)
	if cloneURL == "" {
		return Outcome{Action: "error", Name: r.Name, Detail: "the provider listing gave no clone URL"}
	}
	// The checkout path may not exist until the first clone lands (new
	// registration); create it rather than refusing to sync. Update-side
	// entries always exist on disk already, so this never masks a typo for
	// an existing checkout — discovery only lists repos that exist.
	if err := os.MkdirAll(opts.Path, 0o755); err != nil {
		return Outcome{Action: "error", Name: r.Name, Detail: err.Error()}
	}
	target := filepath.Join(opts.Path, r.Name)
	if _, err := os.Stat(target); err == nil {
		return Outcome{Action: "error", Name: r.Name, Detail: "checkout directory already exists; nothing touched"}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Outcome{Action: "error", Name: r.Name, Detail: "stat: " + err.Error()}
	}

	if _, err := gitmode.RunGit(opts.Path, opts.Timeout, "clone", cloneURL, target); err != nil {
		// The stat above established the directory did not exist before the
		// clone; whatever is there now is the clone's own debris.
		os.RemoveAll(target)
		return Outcome{Action: "error", Name: r.Name, Detail: "clone failed: " + err.Error()}
	}
	return Outcome{Action: "cloned", Name: r.Name, Detail: cloneURL}
}

// updateOne runs `git pull --ff-only` and nothing else (§11.3): a
// fast-forward is updated, "Already up to date" is current, and every
// repository-state refusal — divergence, dirt, detached HEAD, missing
// upstream — is skipped with its reason. The repo is left byte-identical to
// how it was found; it is never merged, rewritten, or lost. Unrecognized
// failures (a dead remote, say) surface as error rows with git's own words.
func updateOne(opts Opts, name string) Outcome {
	dir := filepath.Join(opts.Path, name)
	if err := requireDir(dir); err != nil {
		return Outcome{Action: "error", Name: name, Detail: err.Error()}
	}

	out, err := gitmode.RunGit(dir, opts.Timeout, "pull", "--ff-only")
	if err == nil {
		if strings.Contains(out, "Already up to date") {
			return Outcome{Action: "current", Name: name, Detail: "already up to date"}
		}
		return Outcome{Action: "updated", Name: name, Detail: firstLine(out)}
	}

	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not possible to fast-forward"),
		strings.Contains(msg, "divergent branches"),
		strings.Contains(msg, "need to specify how to reconcile"):
		return Outcome{Action: "skipped", Name: name, Detail: "diverged from upstream; left alone"}
	case strings.Contains(msg, "would be overwritten by merge"),
		strings.Contains(msg, "cannot pull with rebase"),
		strings.Contains(msg, "unstaged changes"),
		strings.Contains(msg, "uncommitted changes"):
		return Outcome{Action: "skipped", Name: name, Detail: "working tree has local changes; left alone"}
	case strings.Contains(msg, "not currently on a branch"),
		strings.Contains(msg, "detached head"):
		return Outcome{Action: "skipped", Name: name, Detail: "detached HEAD; left alone"}
	case strings.Contains(msg, "no tracking information"):
		return Outcome{Action: "skipped", Name: name, Detail: "branch has no upstream; left alone"}
	}
	return Outcome{Action: "error", Name: name, Detail: err.Error()}
}

// requireDir turns a missing directory into a loud, named error instead of
// a git message about the wrong working directory.
func requireDir(dir string) error {
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("checkout path %s does not exist", dir)
		}
		return fmt.Errorf("stat %s: %w", dir, err)
	}
	return nil
}
