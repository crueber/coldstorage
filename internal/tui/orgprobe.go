// The provider-CLI auth probe and owner listing (GO-PORT-SPEC.md §11.1):
// the org form's provider and host rows cycle only through the hosts the
// user's own tooling is authenticated against, and the owner row offers the
// owners the provider API knows about. Both answers come from shelling out
// to gh/glab/tea — per §2 the tool never speaks HTTP itself — and both are
// network calls, so they run exclusively inside Bubble Tea commands
// (goroutines), never on the UI thread.
//
// The gate is §11.1's "no auth, no add": a provider whose CLI is missing or
// not logged in contributes nothing to the probe, and a save that selects it
// is refused with the login command that would unlock it.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// orgAuth is one provider's authenticated hosts, as the probe found them.
type orgAuth struct {
	Provider string
	Hosts    []string
}

// probeRunner is the seam the probe and the owner listing execute through:
// run the named CLI with the §2 rails and return its combined output. A
// missing binary or a failing child is an error value, never a panic — one
// unavailable tool degrades one row of the form, not the dashboard.
var probeRunner = func(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "LC_ALL=C", "NO_COLOR=1")
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// probeAuth runs the three CLIs' auth checks and returns what each is
// authenticated against. A CLI that is missing, fails, or answers without
// parseable hosts contributes nothing — §11.1: a missing binary or no login
// contributes nothing, and several providers may be authenticated at once.
func probeAuth(timeout time.Duration) []orgAuth {
	var authed []orgAuth
	if out, _ := probeRunner(timeout, "gh", "auth", "status"); out != "" {
		if hosts := parseGhAuthHosts(out); len(hosts) > 0 {
			authed = append(authed, orgAuth{Provider: "github", Hosts: hosts})
		}
	}
	if out, _ := probeRunner(timeout, "glab", "auth", "status"); out != "" {
		if hosts := parseGlabAuthHosts(out); len(hosts) > 0 {
			authed = append(authed, orgAuth{Provider: "gitlab", Hosts: hosts})
		}
	}
	if out, _ := probeRunner(timeout, "tea", "logins", "ls"); out != "" {
		if hosts := parseTeaLoginHosts(out); len(hosts) > 0 {
			authed = append(authed, orgAuth{Provider: "gitea", Hosts: hosts})
		}
	}
	return authed
}

// loginCommands names the login command that would unlock each provider —
// §11.1: the refusal names the login command that would unlock it.
var loginCommands = map[string]string{
	"github": "gh auth login",
	"gitlab": "glab auth login",
	"gitea":  "tea login add",
}

// noAuthRefusal is the §11.1 refusal for a probe that found no
// authenticated CLI at all: it names every login command, because no single
// one is the answer when nothing is unlocked.
func noAuthRefusal() string {
	var cmds []string
	for _, p := range []string{"github", "gitlab", "gitea"} {
		cmds = append(cmds, loginCommands[p]+" ("+p+")")
	}
	return "no authenticated provider CLI — run " + strings.Join(cmds, " · ")
}

// hostLike matches a bare hostname line: the per-host section headers both
// gh and glab print, e.g. "github.com" or "gitlab.example.com".
var hostLike = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*\.[a-zA-Z]{2,}$`)

// ghAuthLine extracts the host from gh's "✓ Logged in to github.com account
// user" lines.
var ghAuthLine = regexp.MustCompile(`Logged in to (\S+)`)

// parseGhAuthHosts reads `gh auth status`. gh prints one section per host,
// with the logged-in line carrying the host verbatim; output goes to stderr
// and the command exits non-zero when no host is logged in, so the caller
// hands us the text regardless and an empty parse is the no-login answer.
func parseGhAuthHosts(out string) []string {
	var hosts []string
	seen := map[string]bool{}
	for _, m := range ghAuthLine.FindAllStringSubmatch(out, -1) {
		host := strings.TrimSuffix(m[1], ":")
		if !seen[host] {
			seen[host] = true
			hosts = append(hosts, host)
		}
	}
	return hosts
}

// parseGlabAuthHosts reads `glab auth status`. glab prints a bare hostname
// header per section followed by "✓ Logged in as …" lines, and some
// versions inline the host as "gitlab.com: Logged in as …". Both shapes are
// read: the header before a logged-in line, or the prefix before the colon.
func parseGlabAuthHosts(out string) []string {
	var hosts []string
	seen := map[string]bool{}
	add := func(h string) {
		if h != "" && !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	header := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if idx := strings.Index(line, "Logged in as"); idx >= 0 {
			prefix := strings.TrimSpace(line[:idx])
			prefix = strings.TrimSuffix(prefix, ":")
			if hostLike.MatchString(prefix) {
				add(prefix)
			} else {
				add(header)
			}
		}
		if hostLike.MatchString(line) {
			header = line
		}
	}
	return hosts
}

// parseTeaLoginHosts reads `tea logins ls`, a fixed-width table whose URL
// column carries the instance host. The header row and the rule under it
// carry no URL and are skipped by that rule alone — tea's column layout is
// not a contract, so parsing keys off content, not positions.
func parseTeaLoginHosts(out string) []string {
	var hosts []string
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		for _, field := range strings.Fields(line) {
			if !strings.Contains(field, "://") {
				continue
			}
			if u, err := url.Parse(field); err == nil && u.Host != "" && !seen[u.Host] {
				seen[u.Host] = true
				hosts = append(hosts, u.Host)
			}
		}
	}
	sort.Strings(hosts)
	return hosts
}

// ownerListRunner is the seam the owner picker fetches through — the same
// §2 rails as the probe, a different seam so tests (and callers) can stub
// the two answers independently.
var ownerListRunner = probeRunner

// listOwners fetches the owners the provider knows the user belongs to
// (§11.1: the owner is a pick list fetched from the provider's API). The
// user themselves comes first, then orgs/groups; duplicates are dropped
// case-insensitively because owners are matched loosely everywhere else.
// Failures degrade to a shorter list — the form keeps free-typing as the
// fallback for memberships the API doesn't expose.
func listOwners(timeout time.Duration, provider, host, login string) []string {
	var owners []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[strings.ToLower(s)] {
			return
		}
		seen[strings.ToLower(s)] = true
		owners = append(owners, s)
	}

	switch provider {
	case "github":
		if out, err := ownerListRunner(timeout, "gh", "api", "user", "--jq", ".login"); err == nil {
			add(out)
		}
		if out, err := ownerListRunner(timeout, "gh", "api", "user/orgs", "--jq", ".[].login"); err == nil {
			for _, line := range strings.Split(out, "\n") {
				add(line)
			}
		}
	case "gitlab":
		if out, err := ownerListRunner(timeout, "glab", "api", "user", "--jq", ".username"); err == nil {
			add(out)
		}
		if out, err := ownerListRunner(timeout, "glab", "api", "groups", "--jq", ".[].full_path"); err == nil {
			for _, line := range strings.Split(out, "\n") {
				add(line)
			}
		}
	case "gitea":
		userArgs := []string{"api", "/user"}
		if login != "" {
			userArgs = append(userArgs, "-l", login)
		}
		if out, err := ownerListRunner(timeout, "tea", userArgs...); err == nil {
			var u struct {
				Login    string `json:"login"`
				UserName string `json:"username"`
			}
			if err := unmarshalTea(out, &u); err == nil {
				add(u.Login)
				add(u.UserName)
			}
		}
		orgArgs := []string{"api", "/user/orgs"}
		if login != "" {
			orgArgs = append(orgArgs, "-l", login)
		}
		if out, err := ownerListRunner(timeout, "tea", orgArgs...); err == nil {
			var orgs []struct {
				Login    string `json:"login"`
				Name     string `json:"name"`
				UserName string `json:"username"`
			}
			if err := unmarshalTea(out, &orgs); err == nil {
				for _, o := range orgs {
					add(o.Login)
					add(o.Name)
					add(o.UserName)
				}
			}
		}
	}
	return owners
}

// unmarshalTea parses `tea api` output (§11.2): zero or more chatter lines,
// then the JSON — usually the {"ok":..,"data":…} wrapper, tolerated bare.
// Parsing starts at the first bracket because the chatter is not a
// contract; a data wrapper is unwrapped, a bare value is used as-is.
func unmarshalTea(out string, v any) error {
	idx := strings.IndexAny(out, "[{")
	if idx < 0 {
		return fmt.Errorf("no JSON in tea output")
	}
	raw := []byte(out[idx:])
	var wrapper struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil && len(wrapper.Data) > 0 {
		raw = wrapper.Data
	}
	return json.Unmarshal(raw, v)
}
