package orgsync

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Gitea/Forgejo listing goes through `tea api /repos/search` and nothing
// else (§11.2). `tea repos list --output json` is a trap: tea 0.15.1
// answers it with its own compact schema — owner/name/type/ssh, four
// fields, no clone URL, no archived, no fork — which silently gutted sync
// once. `tea api` also prints a chatter line before the JSON, so parsing
// starts at the first bracket, not at byte zero.
//
// The search endpoint's owner parameter is advisory on some instances: it
// also answers with repos from reachable orgs (the mhs/membership-db
// incident). Results are therefore pinned client-side to exactly the
// registered owner, case-insensitively.

// teaOwner accepts both shapes the tooling produces: the API's owner object
// ({login:...}) and tea 0.15.1's compact schema, where owner is a plain
// string.
type teaOwner struct {
	Login string
}

func (o *teaOwner) UnmarshalJSON(b []byte) error {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		o.Login = s
		return nil
	}
	var raw struct {
		Login    string `json:"login"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return err
	}
	o.Login = raw.Login
	if o.Login == "" {
		o.Login = raw.Username
	}
	return nil
}

type teaRepo struct {
	Name     string   `json:"name"`
	Owner    teaOwner `json:"owner"`
	SSHURL   string   `json:"ssh_url"`
	CloneURL string   `json:"clone_url"`
	SSH      string   `json:"ssh"`
	Archived bool     `json:"archived"`
	Fork     bool     `json:"fork"`
}

// listGitea pages the search endpoint until a page adds nothing new.
// Nothing-new, rather than a short page, is the stop signal because the
// pinned-out foreign rows a page can carry would otherwise mask the end.
func listGitea(src Source, timeout time.Duration) ([]Repo, error) {
	var all []Repo
	seen := make(map[string]bool)

	for page := 1; page <= maxPages; page++ {
		args := []string{}
		if src.Login != "" {
			args = append(args, "-l", src.Login)
		}
		args = append(args, "api",
			fmt.Sprintf("/repos/search?owner=%s&limit=100&page=%d", url.QueryEscape(src.Owner), page))

		out, err := runProvider(timeout, nil, "tea", args...)
		if err != nil {
			return nil, fmt.Errorf("gitea: %w", err)
		}
		rows, err := parseTeaRepos(out)
		if err != nil {
			return nil, err
		}

		for _, r := range rows {
			// Owner pinning: the endpoint's owner parameter is advisory on
			// some instances — it also answers with repos from reachable
			// orgs — so only rows owned by exactly the registered owner
			// survive (§11.2, the mhs/membership-db case).
			if !strings.EqualFold(r.OwnerLogin, src.Owner) {
				continue
			}
			key := strings.ToLower(r.OwnerLogin + "/" + r.Name)
			if seen[key] {
				continue
			}
			seen[key] = true
			all = append(all, r)
		}
		// Nothing-new means the page came back empty: a page of
		// pinned-out foreign rows must not stop the walk, since later
		// pages can still carry owned repos.
		if len(rows) == 0 {
			break
		}
	}
	return all, nil
}

// parseTeaRepos parses `tea api` output: zero or more chatter lines, then
// the JSON — usually the {"ok":..,"data":[…]} wrapper, tolerated as a bare
// array too. A `data: []` wrapper is a real empty answer, not a parse
// failure (§11.2).
func parseTeaRepos(out string) ([]Repo, error) {
	start := strings.IndexAny(out, "[{")
	if start < 0 {
		return nil, fmt.Errorf("gitea: tea api printed no JSON: %q", firstLine(out))
	}
	rest := out[start:]
	if rest[0] == '[' {
		var rows []teaRepo
		if err := json.Unmarshal([]byte(rest), &rows); err != nil {
			return nil, fmt.Errorf("gitea: tea api output: %w", err)
		}
		return teaToRepos(rows), nil
	}
	var wrapper struct {
		OK   bool      `json:"ok"`
		Data []teaRepo `json:"data"`
	}
	if err := json.Unmarshal([]byte(rest), &wrapper); err != nil {
		return nil, fmt.Errorf("gitea: tea api output: %w", err)
	}
	return teaToRepos(wrapper.Data), nil
}

// teaToRepos normalizes both the API shape (ssh_url/clone_url) and tea
// 0.15.1's compact repos-list shape (ssh only, no https URL, no
// archived/fork — documented loss: an https-protocol org over the compact
// shape gets the engine's loud empty-URL error row instead of a doomed
// clone).
func teaToRepos(rows []teaRepo) []Repo {
	repos := make([]Repo, 0, len(rows))
	for _, tr := range rows {
		ssh := tr.SSHURL
		if ssh == "" {
			ssh = tr.SSH
		}
		repos = append(repos, Repo{
			Name:       tr.Name,
			OwnerLogin: tr.Owner.Login,
			SSHURL:     ssh,
			HTTPSURL:   tr.CloneURL,
			Archived:   tr.Archived,
			Fork:       tr.Fork,
		})
	}
	return repos
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
