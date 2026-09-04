package orgsync

import (
	"reflect"
	"testing"
)

// The planner is the part a dry run prints verbatim, so its buckets are
// pinned exactly: names sorted, dedupe by name, and the §11.3 rule that an
// on-disk repo the listing no longer names is an orphan.
func TestPlanBuckets(t *testing.T) {
	src := Source{Owner: "acme", Exclude: []string{"tmp-*"}}
	repos := []Repo{
		{Name: "app", SSHURL: "git@host:acme/app.git"},
		{Name: "app", SSHURL: "git@host:acme/app.git"},     // duplicate listing row
		{Name: "fresh", SSHURL: "git@host:acme/fresh.git"}, // not on disk yet
		{Name: "vendored", SSHURL: "git@host:acme/vendored.git", Fork: true},
		{Name: "old-site", SSHURL: "git@host:acme/old-site.git", Archived: true},
		{Name: "tmp-scratch", SSHURL: "git@host:acme/tmp-scratch.git"},
	}
	disk := []string{"app", "gone", "vendored", "old-site", "tmp-scratch"}

	p := NewPlan(repos, disk, src)

	if got, want := names(p.ToClone), []string{"fresh"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ToClone = %v, want %v", got, want)
	}
	if got, want := p.ToUpdate, []string{"app"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ToUpdate = %v, want %v", got, want)
	}
	// Vendored (fork), old-site (archived) and tmp-scratch (excluded) are
	// still listed — the planner knows why they are refused and reports
	// them as skips. Only "gone" is a true orphan. (Through ListRepos the
	// refused rows are filtered out before this diff, and then surface as
	// orphaned rows on disk, per §11.3.)
	if got, want := p.Orphans, []string{"gone"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Orphans = %v, want %v", got, want)
	}
	wantSkips := map[string]string{
		"vendored":    "fork",
		"old-site":    "archived",
		"tmp-scratch": `matches exclude "tmp-*"`,
	}
	if len(p.Skipped) != len(wantSkips) {
		t.Fatalf("Skipped = %v, want %d rows", p.Skipped, len(wantSkips))
	}
	for _, s := range p.Skipped {
		if want, ok := wantSkips[s.Repo.Name]; !ok || s.Reason != want {
			t.Errorf("Skipped %s reason = %q, want %q", s.Repo.Name, s.Reason, want)
		}
	}
}

// An empty listing is trusted as real: everything on disk becomes an orphan
// and the caller (engine) is responsible for degrading failed listings.
func TestPlanEmptyListingTrusted(t *testing.T) {
	p := NewPlan(nil, []string{"app", "other"}, Source{Owner: "acme"})
	if got, want := p.Orphans, []string{"app", "other"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Orphans = %v, want %v", got, want)
	}
	if len(p.ToClone)+len(p.ToUpdate)+len(p.Skipped) != 0 {
		t.Errorf("empty listing must not produce work: %+v", p)
	}
}

func TestSkipReasonIncludeFlags(t *testing.T) {
	fork := Repo{Name: "v", Fork: true}
	archived := Repo{Name: "o", Archived: true}
	off := Source{Owner: "acme"}

	if skipReason(fork, off) != "fork" || skipReason(archived, off) != "archived" {
		t.Error("forks/archived must be refused when the include flags are off")
	}
	if got := skipReason(fork, Source{IncludeForks: true}); got != "" {
		t.Errorf("include_forks: skipReason = %q, want none", got)
	}
	if got := skipReason(archived, Source{IncludeArchived: true}); got != "" {
		t.Errorf("include_archived: skipReason = %q, want none", got)
	}
}

func names(repos []Repo) []string {
	out := make([]string, 0, len(repos))
	for _, r := range repos {
		out = append(out, r.Name)
	}
	return out
}
