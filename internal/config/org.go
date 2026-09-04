package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// OrgProvider enumerates the forge kinds the sync engine knows how to
// list. The TOML spells them lowercase (github | gitlab | gitea); the
// config stores the raw string and resolves it through the helpers below,
// because hand-edited configs and self-hosted hosts must not force every
// reader through a parse step.
type OrgProvider int

const (
	// OrgProviderGitHub targets github.com and GitHub Enterprise hosts.
	OrgProviderGitHub OrgProvider = iota
	// OrgProviderGitLab targets gitlab.com and self-hosted GitLab.
	OrgProviderGitLab
	// OrgProviderGitea targets gitea.com and Gitea/Forgejo instances.
	OrgProviderGitea
)

// String returns the lowercase TOML spelling of the provider.
func (p OrgProvider) String() string {
	switch p {
	case OrgProviderGitLab:
		return "gitlab"
	case OrgProviderGitea:
		return "gitea"
	default:
		return "github"
	}
}

// FromHost infers the provider from an instance hostname. The three
// public forges answer unambiguously; anything else (a self-hosted
// Gitea, a GitHub Enterprise appliance) needs the provider stated
// explicitly, and ok=false says so.
func FromHost(host string) (OrgProvider, bool) {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "github.com":
		return OrgProviderGitHub, true
	case "gitlab.com":
		return OrgProviderGitLab, true
	case "gitea.com":
		return OrgProviderGitea, true
	}
	return OrgProviderGitHub, false
}

// OrgConfig is one [[orgs]] registration: a provider, an owner, and the
// directory its checkouts live in. Entries decode over a primed value
// (protocol ssh, enabled true) so an entry that omits them still behaves
// as §11.3 syncs.
type OrgConfig struct {
	Provider         string   `toml:"provider"` // github | gitlab | gitea; inferred from host when empty
	Host             string   `toml:"host"`     // instance hostname
	Owner            string   `toml:"owner"`    // an organization or a single user
	Path             string   `toml:"path"`     // checkouts live here; defaults to <first root>/<owner>
	Login            string   `toml:"login"`    // gitea only: which `tea login` to use
	Protocol         string   `toml:"protocol"` // ssh | https
	IncludeForks     bool     `toml:"include_forks"`
	IncludeArchived  bool     `toml:"include_archived"`
	IncludeSubgroups bool     `toml:"include_subgroups"` // gitlab groups only
	Exclude          []string `toml:"exclude"`           // repo-name globs to skip
	Enabled          bool     `toml:"enabled"`
}

// defaultOrg is the primed value every [[orgs]] entry decodes over:
// sync clones over ssh and registrations are live unless switched off.
func defaultOrg() OrgConfig {
	return OrgConfig{
		Protocol: "ssh",
		Enabled:  true,
	}
}

// ResolvedProvider reports the forge this org targets. An explicit
// provider wins; otherwise the host is used to infer one. When neither
// the config nor the host pins it, the result falls back to "github":
// the majority of fleets are GitHub, and OrgProblems separately flags
// the unknown-host-no-provider case, so the fallback is never the only
// thing standing between the owner and a wrong guess.
func (o OrgConfig) ResolvedProvider() string {
	if o.Provider != "" {
		return strings.ToLower(o.Provider)
	}
	if p, ok := FromHost(o.Host); ok {
		return p.String()
	}
	return "github"
}

// ResolvedPath returns the directory this org's checkouts live in: the
// configured path when present, otherwise <first root>/<owner> — the
// same place `org add` puts new clones. It returns "" when the org has
// no path and the config has no root to derive one from; OrgProblems
// reports that combination rather than guessing.
func (o OrgConfig) ResolvedPath(c Config) string {
	if o.Path != "" {
		return filepath.Clean(Expand(o.Path))
	}
	if len(c.Roots) == 0 || o.Owner == "" {
		return ""
	}
	return filepath.Join(Expand(c.Roots[0]), o.Owner)
}

// OrgProblems validates the org registrations of §11.4 and §4: empty
// owner or host, a provider that cannot be inferred, duplicate
// registrations, and nested checkout paths. It reports problems as
// human-readable strings — the dashboard surfaces them, it does not
// repair them — and an empty slice means the orgs are sound. Checks that
// depend on identity (duplicates, nesting) are skipped for orgs already
// flagged on their own fields, so one typo does not bury the owner in
// derived complaints.
func (c Config) OrgProblems() []string {
	var problems []string

	type registration struct {
		provider, host, owner string
	}
	seen := make(map[registration]int)
	for i, o := range c.Orgs {
		label := fmt.Sprintf("orgs[%d]", i)
		if o.Owner == "" {
			problems = append(problems, label+": owner is empty")
		}
		if o.Host == "" {
			problems = append(problems, label+": host is empty")
		}
		if o.Provider == "" {
			if _, ok := FromHost(o.Host); !ok {
				problems = append(problems, fmt.Sprintf(
					"%s: provider is unset and host %q is not one of github.com, gitlab.com, gitea.com — set provider explicitly", label, o.Host))
			}
		}
		if o.Host != "" && o.Owner != "" {
			reg := registration{o.ResolvedProvider(), strings.ToLower(o.Host), o.Owner}
			if first, dup := seen[reg]; dup {
				problems = append(problems, fmt.Sprintf(
					"%s is a duplicate of orgs[%d] (%s/%s/%s)", label, first, reg.provider, reg.host, reg.owner))
			} else {
				seen[reg] = i
			}
		}
	}

	paths := make([]string, len(c.Orgs))
	for i, o := range c.Orgs {
		paths[i] = o.ResolvedPath(c)
	}
	for i := range c.Orgs {
		for j := i + 1; j < len(c.Orgs); j++ {
			if paths[i] == "" || paths[j] == "" {
				continue
			}
			switch {
			case paths[i] == paths[j], strings.HasPrefix(paths[i], paths[j]+string(filepath.Separator)):
				problems = append(problems, fmt.Sprintf(
					"orgs[%d] path %q sits inside or coincides with orgs[%d] path %q — overlapping orgs fight over the same checkouts",
					i, paths[i], j, paths[j]))
			case strings.HasPrefix(paths[j], paths[i]+string(filepath.Separator)):
				problems = append(problems, fmt.Sprintf(
					"orgs[%d] path %q sits inside orgs[%d] path %q — nested orgs fight over the same checkouts",
					j, paths[j], i, paths[i]))
			}
		}
	}
	return problems
}
