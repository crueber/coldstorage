package orgsync

import (
	"encoding/json"
	"fmt"
	"time"
)

// The gh invocation is deliberately the one from the spec (§11.2): one call
// for both users and orgs, JSON output so nothing depends on gh's table
// rendering, and --limit 1000 because gh's default page of 30 would silently
// sync a fraction of a large org. owner.login is carried even though gh
// already scopes results to the requested owner, so callers can pin or
// display without a second call.

type ghOwner struct {
	Login string `json:"login"`
}

type ghRepo struct {
	Name       string  `json:"name"`
	SSHURL     string  `json:"sshUrl"`
	URL        string  `json:"url"`
	IsArchived bool    `json:"isArchived"`
	IsFork     bool    `json:"isFork"`
	Owner      ghOwner `json:"owner"`
}

// listGitHub lists the owner's repositories via `gh repo list`. gh scopes
// results to <owner> itself, so — unlike the gitea search endpoint — no
// client-side owner pinning is needed here.
func listGitHub(src Source, timeout time.Duration) ([]Repo, error) {
	out, err := runProvider(timeout, nil, "gh", "repo", "list", src.Owner,
		"--limit", "1000",
		"--json", "name,sshUrl,url,isArchived,isFork,owner")
	if err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}
	return parseGhRepos(out)
}

// parseGhRepos decodes gh's JSON array. gh prints exactly one JSON document
// on stdout, so there is no chatter to strip — unlike tea (§11.2).
func parseGhRepos(out string) ([]Repo, error) {
	var raw []ghRepo
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("github: gh repo list output: %w", err)
	}
	repos := make([]Repo, 0, len(raw))
	for _, r := range raw {
		repos = append(repos, Repo{
			Name:       r.Name,
			OwnerLogin: r.Owner.Login,
			SSHURL:     r.SSHURL,
			HTTPSURL:   r.URL,
			Archived:   r.IsArchived,
			Fork:       r.IsFork,
		})
	}
	return repos, nil
}
