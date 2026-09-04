package orgsync

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// GitLab listing is two calls (§11.2): namespaces/<owner> decides whether
// the owner is a user or a group — the repo-list flag differs (--user vs
// --group) and only groups honor --include-subgroups — then repo list pages
// through the projects. Every glab child gets GITLAB_HOST so self-hosted
// instances resolve without relying on the user's glab default host.

type glabNamespace struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

type glabProject struct {
	Name          string `json:"name"`
	SSHURLToRepo  string `json:"ssh_url_to_repo"`
	HTTPURLToRepo string `json:"http_url_to_repo"`
	Archived      bool   `json:"archived"`
	// ForkedFrom is probed for presence, not contents: GitLab signals "is a
	// fork" by the mere existence of forked_from_project, and the parent's
	// shape has drifted across API versions.
	ForkedFrom json.RawMessage `json:"forked_from_project"`
	Namespace  *struct {
		Path string `json:"path"`
	} `json:"namespace"`
}

// glabPerPage is the GitLab API's hard maximum page size; asking for more
// silently returns fewer.
const glabPerPage = 100

// listGitLab resolves the owner's namespace kind, then pages repo list
// until a short page — the only signal GitLab gives that the listing ended.
func listGitLab(src Source, timeout time.Duration) ([]Repo, error) {
	env := []string{"GITLAB_HOST=" + src.Host}

	out, err := runProvider(timeout, env, "glab", "api", "namespaces/"+src.Owner)
	if err != nil {
		return nil, fmt.Errorf("gitlab: %w", err)
	}
	kind, err := parseGlabNamespace(out)
	if err != nil {
		return nil, err
	}

	args := []string{"repo", "list"}
	scope := "--user"
	if kind == "group" {
		scope = "--group"
		args = append(args, scope, src.Owner)
		if src.IncludeSubgroups {
			args = append(args, "--include-subgroups")
		}
	} else {
		args = append(args, scope, src.Owner)
	}
	args = append(args, "--output", "json", "--per-page", strconv.Itoa(glabPerPage))

	var all []Repo
	for page := 1; page <= maxPages; page++ {
		pageArgs := append(append([]string(nil), args...), "--page", strconv.Itoa(page))
		out, err := runProvider(timeout, env, "glab", pageArgs...)
		if err != nil {
			return nil, fmt.Errorf("gitlab: %w", err)
		}
		rows, err := parseGlabPage(out)
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
		if len(rows) < glabPerPage {
			break
		}
	}
	return all, nil
}

// parseGlabNamespace reads the kind out of the namespaces API answer. A kind
// this code does not recognize is an error, not a guess: guessing user for a
// group (or vice versa) picks the wrong repo-list flag and lists nothing.
func parseGlabNamespace(out string) (string, error) {
	var ns glabNamespace
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &ns); err != nil {
		return "", fmt.Errorf("gitlab: namespace lookup: %w", err)
	}
	switch ns.Kind {
	case "user", "group":
		return ns.Kind, nil
	}
	return "", fmt.Errorf("gitlab: namespace %q has kind %q, neither user nor group", ns.Path, ns.Kind)
}

// parseGlabPage decodes one page of `glab repo list --output json`, which is
// a bare JSON array of projects.
func parseGlabPage(out string) ([]Repo, error) {
	var raw []glabProject
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("gitlab: repo list page: %w", err)
	}
	repos := make([]Repo, 0, len(raw))
	for _, p := range raw {
		var owner string
		if p.Namespace != nil {
			owner = p.Namespace.Path
		}
		repos = append(repos, Repo{
			Name:       p.Name,
			OwnerLogin: owner,
			SSHURL:     p.SSHURLToRepo,
			HTTPSURL:   p.HTTPURLToRepo,
			Archived:   p.Archived,
			Fork:       len(p.ForkedFrom) > 0 && string(p.ForkedFrom) != "null",
		})
	}
	return repos, nil
}
