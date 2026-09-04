package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestDefaultMatchesSpec(t *testing.T) {
	c := Default()
	if got := c.Roots; len(got) != 1 || got[0] != "~/Projects" {
		t.Errorf("Roots = %v, want [~/Projects]", got)
	}
	if c.MaxDepth != 4 {
		t.Errorf("MaxDepth = %d, want 4", c.MaxDepth)
	}
	if c.FollowNestedRepos || c.FollowSymlinks {
		t.Errorf("FollowNestedRepos/FollowSymlinks = %t/%t, want false/false", c.FollowNestedRepos, c.FollowSymlinks)
	}
	if c.Refresh.Interval != "5m" || !c.Refresh.Watch || c.Refresh.Debounce != "1s" {
		t.Errorf("Refresh = %+v, want 5m/true/1s", c.Refresh)
	}
	if c.Status.Untracked != "normal" || c.Status.MaxFiles != 200 || c.Status.MaxAge != "1h" {
		t.Errorf("Status = %+v, want normal/200/1h", c.Status)
	}
	if c.Remote.Fetch || c.Remote.Interval != "1h" || c.Remote.Concurrency != 4 || c.Remote.Timeout != "20s" {
		t.Errorf("Remote = %+v, want false/1h/4/20s", c.Remote)
	}
	if c.Visibility.Enabled || c.Visibility.Interval != "24h" || c.Visibility.Concurrency != 4 || c.Visibility.Timeout != "10s" {
		t.Errorf("Visibility = %+v, want false/24h/4/10s", c.Visibility)
	}
	if c.Release.TagPattern != "*[0-9]*" || c.Release.MaxSubjects != 30 || !c.Release.ReadChangelog {
		t.Errorf("Release = %+v", c.Release)
	}
	if want := []string{"CHANGELOG.md", "CHANGELOG", "changelog.md"}; strings.Join(c.Release.ChangelogFiles, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("ChangelogFiles = %v, want %v", c.Release.ChangelogFiles, want)
	}
	if c.UI.DefaultSort != "activity" || c.UI.DefaultSince != "" || len(c.UI.DefaultFilters) != 0 {
		t.Errorf("UI sort/since/filters = %q/%q/%v", c.UI.DefaultSort, c.UI.DefaultSince, c.UI.DefaultFilters)
	}
	if got := c.UI.EditorCommand; strings.Join(got, "\x00") != "zed\x00{path}" {
		t.Errorf("EditorCommand = %v, want [zed {path}]", got)
	}
	if len(c.UI.GitClientCommand) != 0 || len(c.UI.FileManagerCommand) != 0 {
		t.Errorf("GitClientCommand = %v, FileManagerCommand = %v, want empty", c.UI.GitClientCommand, c.UI.FileManagerCommand)
	}
	if len(c.UI.TerminalCommand) != 0 {
		t.Errorf("TerminalCommand = %v, want empty (auto-detect $SHELL)", c.UI.TerminalCommand)
	}
	if len(c.Orgs) != 0 {
		t.Errorf("Orgs = %v, want empty", c.Orgs)
	}
}

const roundTripConfig = `
roots = ["~/code", "~/work"]
max_depth = 6
follow_symlinks = true
exclude = ["vendor/**"]
prune = ["node_modules"]

[refresh]
watch = false

[[orgs]]
host = "github.com"
owner = "acme"

[[orgs]]
provider = "gitlab"
host = "gitlab.example.com"
owner = "beta"
path = "~/dev/gl/beta"
protocol = "https"
include_forks = true
include_subgroups = true
exclude = ["archived-*"]
enabled = false
`

func TestLoadRoundTrip(t *testing.T) {
	path := writeConfig(t, roundTripConfig)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if strings.Join(c.Roots, ",") != "~/code,~/work" {
		t.Errorf("Roots = %v", c.Roots)
	}
	if c.MaxDepth != 6 || !c.FollowSymlinks || c.FollowNestedRepos {
		t.Errorf("depth/symlinks/nested = %d/%t/%t", c.MaxDepth, c.FollowSymlinks, c.FollowNestedRepos)
	}
	if c.Refresh.Watch {
		t.Errorf("Refresh.Watch = true, want the file's false override")
	}
	if c.Refresh.Interval != "5m" || c.Refresh.Debounce != "1s" {
		t.Errorf("Refresh = %+v, want absent fields kept at defaults", c.Refresh)
	}

	// Absent sections and fields keep their spec defaults.
	if c.Status != (StatusConfig{Untracked: "normal", MaxFiles: 200, MaxAge: "1h"}) {
		t.Errorf("Status = %+v, want defaults for absent section", c.Status)
	}
	if c.Remote.Concurrency != 4 {
		t.Errorf("Remote = %+v, want defaults for absent section", c.Remote)
	}

	if len(c.Orgs) != 2 {
		t.Fatalf("Orgs = %+v, want 2 entries", c.Orgs)
	}
	first := c.Orgs[0]
	if first.Provider != "" || first.Host != "github.com" || first.Owner != "acme" {
		t.Errorf("orgs[0] = %+v", first)
	}
	if first.Protocol != "ssh" || !first.Enabled || first.IncludeForks || first.IncludeSubgroups {
		t.Errorf("orgs[0] = %+v, want primed defaults for absent fields", first)
	}
	second := c.Orgs[1]
	if second.Provider != "gitlab" || second.Host != "gitlab.example.com" || second.Owner != "beta" ||
		second.Path != "~/dev/gl/beta" || second.Protocol != "https" ||
		!second.IncludeForks || !second.IncludeSubgroups || second.Enabled ||
		len(second.Exclude) != 1 || second.Exclude[0] != "archived-*" {
		t.Errorf("orgs[1] = %+v", second)
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist", "config.toml")
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load of missing file: %v", err)
	}
	if !reflect.DeepEqual(c, Default()) {
		t.Errorf("Load(missing) = %+v, want Default()", c)
	}
}

func TestLoadUnparsableFileNamesPath(t *testing.T) {
	path := writeConfig(t, "roots = [unclosed")
	_, err := Load(path)
	if err == nil {
		t.Fatal("want error for unparsable file")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the path", err)
	}
}

func TestLoadUnknownKeyIsHardError(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantKey string
	}{
		{"top level", "bogus_key = 1\n", "bogus_key"},
		{"inside section", "[refresh]\nbogus = 1\n", "refresh.bogus"},
		{"inside orgs", "[[orgs]]\nhost = \"github.com\"\nowner = \"acme\"\nbogus = true\n", "orgs[0].bogus"},
		{"inside second org", "[[orgs]]\nowner = \"a\"\n\n[[orgs]]\nowner = \"b\"\nbogus = 1\n", "orgs[1].bogus"},
		{"unknown table", "[bogus_section]\nkey = 1\n", "bogus_section"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, tc.content)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("want hard error for unknown key %q", tc.wantKey)
			}
			if !strings.Contains(err.Error(), tc.wantKey) {
				t.Errorf("error %q does not name the key %q", err, tc.wantKey)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error %q does not name the path", err)
			}
		})
	}
}

func TestLoadWrongTypeIsError(t *testing.T) {
	path := writeConfig(t, "max_depth = \"four\"\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("want error for wrong-typed value")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the path", err)
	}
}
