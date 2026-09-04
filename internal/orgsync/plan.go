package orgsync

import "sort"

// Plan is the pure diff between what the provider lists and what sits on
// disk (§11.3). It is computed up front so a dry run can print it verbatim
// and so the executor — the only part that touches the network or working
// trees — stays dumb. All buckets are sorted by name, which is what makes
// the serial engine's "one repo at a time, sorted by name" contract hold
// without the executor knowing about ordering.
type Plan struct {
	ToClone  []Repo
	ToUpdate []string
	Orphans  []string
	Skipped  []Skip
}

// Skip is a listed repository the configuration refuses to sync, with the
// reason it will be reported under.
type Skip struct {
	Repo   Repo
	Reason string
}

// Plan diffs the listing against disk. Every bucket is deterministic:
// listed repos are deduplicated by name, and anything on disk that the
// listing does not name is an orphan — archived-and-filtered, excluded,
// renamed upstream, deleted, or private-and-unlisted alike. Orphans are
// reported, never touched: nothing is ever deleted (§11.3).
//
// A failed listing is not this function's problem: an empty listing is
// trusted as real, and the caller (PlanSync/ListSync) decides what a
// listing failure degrades to.
func NewPlan(repos []Repo, disk []string, src Source) Plan {
	listed := make(map[string]struct{}, len(repos))
	unique := make([]Repo, 0, len(repos))
	for _, r := range repos {
		if _, dup := listed[r.Name]; dup {
			continue
		}
		listed[r.Name] = struct{}{}
		unique = append(unique, r)
	}

	onDisk := make(map[string]struct{}, len(disk))
	for _, d := range disk {
		onDisk[d] = struct{}{}
	}

	var p Plan
	for _, r := range unique {
		if reason := skipReason(r, src); reason != "" {
			p.Skipped = append(p.Skipped, Skip{Repo: r, Reason: reason})
			continue
		}
		if _, have := onDisk[r.Name]; have {
			p.ToUpdate = append(p.ToUpdate, r.Name)
			continue
		}
		p.ToClone = append(p.ToClone, r)
	}
	for _, d := range disk {
		if _, known := listed[d]; !known {
			p.Orphans = append(p.Orphans, d)
		}
	}

	sort.Slice(p.ToClone, func(i, j int) bool { return p.ToClone[i].Name < p.ToClone[j].Name })
	sort.Strings(p.ToUpdate)
	sort.Strings(p.Orphans)
	sort.Slice(p.Skipped, func(i, j int) bool { return p.Skipped[i].Repo.Name < p.Skipped[j].Repo.Name })
	return p
}
