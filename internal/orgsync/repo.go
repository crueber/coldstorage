// Package orgsync implements organization sync (spec §11): repo listings
// through the user's own provider CLIs, a pure plan, and a strictly serial
// executor that clones and fast-forwards but never deletes.
//
// Everything here shells out, per §2: credentials ride the CLI logins the
// user already has, provider API drift is absorbed by the CLI instead of by
// this codebase, and visibility — which no git object records — can only
// come from asking the host. The package exposes APIs for the TUI layer;
// there are no CLI commands.
package orgsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/crueber/coldstorage/internal/gitmode"
)

// Repo is one provider listing row, normalized across the three providers.
// Both URLs are carried even though only the configured protocol's URL is
// cloned from: the other one is what a protocol switch costs nothing.
type Repo struct {
	Name       string
	OwnerLogin string
	SSHURL     string
	HTTPSURL   string
	Archived   bool
	Fork       bool
}

// Source is the sync-facing slice of a registered org (config.OrgConfig):
// which provider to ask, which owner, where checkouts live, and what the
// config refuses to sync.
type Source struct {
	Provider         string
	Host             string
	Owner            string
	Path             string
	Login            string
	Protocol         string
	IncludeForks     bool
	IncludeArchived  bool
	IncludeSubgroups bool
	Exclude          []string
}

// maxPages is the pagination safety valve. A healthy listing never needs
// this many pages at 100 per page; the cap exists so a provider that keeps
// "answering" full pages forever cannot hang a sync.
const maxPages = 200

// ListRepos asks the configured provider for the owner's repositories and
// applies the source's filters. Dispatching on src.Provider keeps every
// provider's output-shape hazards (§11.2) inside its own file.
//
// An empty result is a real answer and is returned as such — the caller
// decides what a failed listing does, but a successful empty listing means
// the owner genuinely lists nothing.
func ListRepos(src Source, timeout time.Duration) ([]Repo, error) {
	var (
		repos []Repo
		err   error
	)
	switch src.Provider {
	case "github":
		repos, err = listGitHub(src, timeout)
	case "gitlab":
		repos, err = listGitLab(src, timeout)
	case "gitea":
		repos, err = listGitea(src, timeout)
	default:
		return nil, fmt.Errorf("orgsync: unknown provider %q", src.Provider)
	}
	if err != nil {
		return nil, err
	}
	return filterRepos(repos, src), nil
}

// filterRepos drops the rows the source refuses: forks, archived repos, and
// exclude-glob matches. The planner re-guards these for any caller that
// hands it unfiltered rows; filtering here as well keeps a thousand-repo
// listing from turning into a thousand skip rows.
func filterRepos(repos []Repo, src Source) []Repo {
	kept := make([]Repo, 0, len(repos))
	for _, r := range repos {
		if skipReason(r, src) == "" {
			kept = append(kept, r)
		}
	}
	return kept
}

// skipReason names why a repo is refused under this source, or "" when it
// fits. One shared implementation so the listing filter and the planner can
// never disagree about what is skipped.
func skipReason(r Repo, src Source) string {
	if r.Fork && !src.IncludeForks {
		return "fork"
	}
	if r.Archived && !src.IncludeArchived {
		return "archived"
	}
	for _, pattern := range src.Exclude {
		if match, _ := doublestar.Match(pattern, r.Name); match {
			return fmt.Sprintf("matches exclude %q", pattern)
		}
	}
	return ""
}

// cloneURL picks the URL the configured protocol clones from. An empty
// result is a loud error row in the engine, never a doomed clone (§11.3) —
// which is also how tea 0.15.1's compact schema, which has no https URL at
// all, surfaces when someone registers a gitea org with protocol = "https".
func (r Repo) cloneURL(protocol string) string {
	if protocol == "https" {
		return r.HTTPSURL
	}
	return r.SSHURL
}

// runProvider executes one provider CLI invocation under the §2 rails:
// stdin is nil, locale and color are pinned so the output is parseable, a
// context timeout kills a hung child, and extraEnv carries provider-specific
// rails such as GITLAB_HOST. Like the git rails, a failing child is an
// error value carrying stderr — never a panic — so one bad provider call
// degrades one sync, not the process.
func runProvider(timeout time.Duration, extraEnv []string, name string, args ...string) (string, error) {
	if timeout <= 0 {
		timeout = gitmode.GitTimeout()
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(),
		"LC_ALL=C",
		"NO_COLOR=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
	)
	cmd.Env = append(cmd.Env, extraEnv...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("%s %s: timed out after %s: %s",
				name, strings.Join(args, " "), timeout, msg)
		}
		return "", fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}
