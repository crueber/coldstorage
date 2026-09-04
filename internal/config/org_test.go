package config

import (
	"strings"
	"testing"
)

func TestFromHost(t *testing.T) {
	cases := []struct {
		host  string
		want  OrgProvider
		known bool
	}{
		{"github.com", OrgProviderGitHub, true},
		{"gitlab.com", OrgProviderGitLab, true},
		{"gitea.com", OrgProviderGitea, true},
		{"GitHub.COM", OrgProviderGitHub, true},
		{"git.example.com", OrgProviderGitHub, false},
		{"", OrgProviderGitHub, false},
	}
	for _, tc := range cases {
		got, ok := FromHost(tc.host)
		if ok != tc.known || (ok && got != tc.want) {
			t.Errorf("FromHost(%q) = (%v, %t), want (%v, %t)", tc.host, got, ok, tc.want, tc.known)
		}
	}
}

func TestResolvedProvider(t *testing.T) {
	cases := []struct {
		name string
		org  OrgConfig
		want string
	}{
		{"explicit provider wins", OrgConfig{Provider: "GitLab", Host: "github.com"}, "gitlab"},
		{"inferred from public host", OrgConfig{Host: "gitlab.com"}, "gitlab"},
		{"inferred from gitea host", OrgConfig{Host: "gitea.com"}, "gitea"},
		{"falls back to github when unknown", OrgConfig{Host: "git.acme.corp"}, "github"},
		{"falls back when nothing set", OrgConfig{}, "github"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.org.ResolvedProvider(); got != tc.want {
				t.Errorf("ResolvedProvider() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolvedPath(t *testing.T) {
	c := Default() // roots = [~/Projects]
	expandedRoot := Expand("~/Projects")

	if got := (OrgConfig{Owner: "acme"}).ResolvedPath(c); got != expandedRoot+"/acme" {
		t.Errorf("derived path = %q, want %q", got, expandedRoot+"/acme")
	}
	if got := (OrgConfig{Owner: "acme", Path: "~/dev/gl"}).ResolvedPath(c); got != Expand("~/dev/gl") {
		t.Errorf("explicit path = %q, want %q", got, Expand("~/dev/gl"))
	}
	if got := (OrgConfig{}).ResolvedPath(c); got != "" {
		t.Errorf("no owner and no path = %q, want empty", got)
	}
}

func TestOrgProblemsValid(t *testing.T) {
	c := Default()
	c.Orgs = []OrgConfig{
		{Host: "github.com", Owner: "acme", Path: "~/dev/github.com/acme"},
		{Provider: "gitea", Host: "git.acme.corp", Owner: "ops", Path: "~/dev/gitea/ops"},
	}
	if got := c.OrgProblems(); len(got) != 0 {
		t.Errorf("OrgProblems() = %v, want empty", got)
	}
}

func TestOrgProblemsKinds(t *testing.T) {
	problemsOf := func(c Config) string {
		return strings.Join(c.OrgProblems(), "\n")
	}

	t.Run("empty owner", func(t *testing.T) {
		c := Config{Orgs: []OrgConfig{{Host: "github.com"}}}
		if s := problemsOf(c); !strings.Contains(s, "owner is empty") {
			t.Errorf("problems = %q, want an owner complaint", s)
		}
	})

	t.Run("empty host", func(t *testing.T) {
		c := Config{Orgs: []OrgConfig{{Owner: "acme"}}}
		if s := problemsOf(c); !strings.Contains(s, "host is empty") {
			t.Errorf("problems = %q, want a host complaint", s)
		}
	})

	t.Run("unknown host without provider", func(t *testing.T) {
		c := Config{Orgs: []OrgConfig{{Host: "git.acme.corp", Owner: "ops"}}}
		s := problemsOf(c)
		if !strings.Contains(s, "provider is unset") || !strings.Contains(s, "git.acme.corp") {
			t.Errorf("problems = %q, want an unknown-host complaint", s)
		}
	})

	t.Run("unknown host with explicit provider is fine", func(t *testing.T) {
		c := Config{Orgs: []OrgConfig{{Provider: "gitea", Host: "git.acme.corp", Owner: "ops", Path: "~/dev/gitea/ops"}}}
		if got := c.OrgProblems(); len(got) != 0 {
			t.Errorf("problems = %v, want empty", got)
		}
	})

	t.Run("duplicate registration", func(t *testing.T) {
		c := Config{Orgs: []OrgConfig{
			{Host: "github.com", Owner: "acme", Path: "~/dev/a"},
			{Host: "github.com", Owner: "acme", Path: "~/dev/b"},
		}}
		s := problemsOf(c)
		if !strings.Contains(s, "duplicate") || !strings.Contains(s, "orgs[0]") {
			t.Errorf("problems = %q, want a duplicate complaint naming orgs[0]", s)
		}
	})

	t.Run("duplicate after provider inference", func(t *testing.T) {
		c := Config{Orgs: []OrgConfig{
			{Host: "github.com", Owner: "acme", Path: "~/dev/a"},
			{Provider: "github", Host: "github.com", Owner: "acme", Path: "~/dev/b"},
		}}
		if s := problemsOf(c); !strings.Contains(s, "duplicate") {
			t.Errorf("problems = %q, want a duplicate complaint across inferred and explicit providers", s)
		}
	})

	t.Run("nested path either direction", func(t *testing.T) {
		c := Config{Orgs: []OrgConfig{
			{Host: "github.com", Owner: "acme", Path: "~/dev/github.com"},
			{Host: "gitlab.com", Owner: "beta", Path: "~/dev/github.com/beta"},
		}}
		if s := problemsOf(c); !strings.Contains(s, "sits inside") {
			t.Errorf("problems = %q, want a nesting complaint", s)
		}
	})

	t.Run("coinciding paths", func(t *testing.T) {
		c := Config{Orgs: []OrgConfig{
			{Host: "github.com", Owner: "acme", Path: "~/dev/shared"},
			{Host: "gitlab.com", Owner: "beta", Path: "~/dev/shared/"},
		}}
		if s := problemsOf(c); !strings.Contains(s, "coincides") {
			t.Errorf("problems = %q, want an overlap complaint", s)
		}
	})

	t.Run("derived paths nest", func(t *testing.T) {
		c := Default()
		c.Roots = []string{"~/Projects"}
		c.Orgs = []OrgConfig{
			{Host: "github.com", Owner: "acme", Path: "~/Projects"},
			{Host: "gitlab.com", Owner: "beta"}, // derives to ~/Projects/beta
		}
		if s := problemsOf(c); !strings.Contains(s, "sits inside") {
			t.Errorf("problems = %q, want a nesting complaint against the derived path", s)
		}
	})

	t.Run("unresolvable paths are skipped", func(t *testing.T) {
		c := Config{Orgs: []OrgConfig{
			{Host: "github.com", Owner: "acme"},
			{Host: "gitlab.com", Owner: "beta"},
		}}
		if got := c.OrgProblems(); len(got) != 0 {
			t.Errorf("problems = %v, want empty with no roots to derive from", got)
		}
	})

	t.Run("one bad org does not cascade into derived complaints", func(t *testing.T) {
		c := Config{Orgs: []OrgConfig{
			{Host: "github.com"},
			{Host: "github.com"},
		}}
		s := problemsOf(c)
		if !strings.Contains(s, "owner is empty") {
			t.Errorf("problems = %q, want the owner complaints", s)
		}
		if strings.Contains(s, "duplicate") {
			t.Errorf("problems = %q, want no duplicate complaint while owners are already flagged", s)
		}
	})
}

// §11.4: an org added over a directory another registration covers
// replaces the older one — a directory must never be double-listed.
func TestDedupeOrgsKeepsNewest(t *testing.T) {
	c := Default()
	c.Roots = []string{"~/dev"}
	c.Orgs = []OrgConfig{
		{Provider: "github", Host: "github.com", Owner: "acme", Path: "~/dev/acme"},
		{Provider: "github", Host: "github.com", Owner: "other", Path: "~/dev/other"},
		{Provider: "github", Host: "github.com", Owner: "acme-v2", Path: "~/dev/acme"}, // covers org 0
	}
	if changed := c.DedupeOrgs(); !changed {
		t.Fatal("dedupe must report the dropped registration")
	}
	if len(c.Orgs) != 2 {
		t.Fatalf("orgs = %d, want 2", len(c.Orgs))
	}
	if c.Orgs[0].Owner != "acme-v2" {
		t.Errorf("survivor = %q, want the newer acme-v2", c.Orgs[0].Owner)
	}
	if c.Orgs[1].Owner != "other" {
		t.Errorf("unrelated org must survive: %+v", c.Orgs[1])
	}
	// Idempotent.
	if c.DedupeOrgs() {
		t.Error("a second pass must change nothing")
	}
}

func TestDedupeOrgsKeepsUnresolvable(t *testing.T) {
	c := Default() // roots = ~/Projects
	c.Orgs = []OrgConfig{
		{Provider: "github", Host: "github.com", Owner: "orphan"},             // no path, resolves to root/owner
		{Provider: "gitea", Host: "gitea.example", Owner: "orphan", Path: ""}, // same derived path
	}
	c.DedupeOrgs()
	if len(c.Orgs) != 1 || c.Orgs[0].Provider != "gitea" {
		t.Errorf("derived-path duplicates must collapse to the newest: %+v", c.Orgs)
	}
}

func TestLoadDedupesOrgsAndRoots(t *testing.T) {
	path := writeConfig(t, `
roots = ["~/dev", "~/dev"]
[[orgs]]
host = "github.com"
owner = "acme"
path = "~/dev/acme"
[[orgs]]
host = "github.com"
owner = "acme"
path = "~/dev/acme"
`)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Roots) != 1 {
		t.Errorf("roots = %v, want the duplicate dropped", c.Roots)
	}
	if len(c.Orgs) != 1 {
		t.Fatalf("orgs = %d, want 1", len(c.Orgs))
	}
	if c.Orgs[0].Owner != "acme" {
		t.Errorf("survivor = %+v", c.Orgs[0])
	}
}
