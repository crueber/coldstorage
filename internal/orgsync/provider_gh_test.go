package orgsync

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The gh fixture is the real captured shape: a JSON array with nested
// owner.login and camelCase field names.
const ghFixture = `[
  {"name":"fleet-api","sshUrl":"git@github.com:acme/fleet-api.git","url":"https://github.com/acme/fleet-api.git","isArchived":false,"isFork":false,"owner":{"login":"acme"}},
  {"name":"old-site","sshUrl":"git@github.com:acme/old-site.git","url":"https://github.com/acme/old-site.git","isArchived":true,"isFork":false,"owner":{"login":"acme"}},
  {"name":"vendored","sshUrl":"git@github.com:acme/vendored.git","url":"https://github.com/acme/vendored.git","isArchived":false,"isFork":true,"owner":{"login":"acme"}}
]`

func TestParseGhRepos(t *testing.T) {
	repos, err := parseGhRepos(ghFixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 3 {
		t.Fatalf("parsed %d repos, want 3", len(repos))
	}
	r := repos[0]
	if r.Name != "fleet-api" || r.OwnerLogin != "acme" ||
		r.SSHURL != "git@github.com:acme/fleet-api.git" ||
		r.HTTPSURL != "https://github.com/acme/fleet-api.git" ||
		r.Archived || r.Fork {
		t.Errorf("row 0 = %+v", r)
	}
	if !repos[1].Archived || repos[1].Fork {
		t.Errorf("old-site flags = archived %v fork %v", repos[1].Archived, repos[1].Fork)
	}
	if !repos[2].Fork || repos[2].Archived {
		t.Errorf("vendored flags = archived %v fork %v", repos[2].Archived, repos[2].Fork)
	}

	if _, err := parseGhRepos("gh: (EOF)"); err == nil {
		t.Error("non-JSON output must be an error, not an empty listing")
	}
}

// TestListGitHub exercises the whole exec path through a fake `gh` on PATH
// and pins the spec's exact invocation (§11.2), with the include flags off:
// the archived and forked rows must already be filtered out by the listing.
func TestListGitHub(t *testing.T) {
	bin := t.TempDir()
	writeScript(t, filepath.Join(bin, "gh"), `echo "$@" >> "$ORGSYNC_LOG"
echo '`+ghFixture+`'
`)
	putOnPATH(t, bin)
	log := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("ORGSYNC_LOG", log)

	repos, err := ListRepos(Source{Provider: "github", Owner: "acme"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(repos), []string{"fleet-api"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ListRepos = %v, want %v (forks/archived filtered at the listing)", got, want)
	}

	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"repo list acme", "--limit 1000", "isArchived,isFork,owner"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("gh args missing %q: %s", want, raw)
		}
	}
}

// With both include flags on, all three fixture rows survive the listing.
func TestListGitHubIncludes(t *testing.T) {
	bin := t.TempDir()
	writeScript(t, filepath.Join(bin, "gh"), `echo '`+ghFixture+`'
`)
	putOnPATH(t, bin)

	repos, err := ListRepos(Source{
		Provider: "github", Owner: "acme",
		IncludeForks: true, IncludeArchived: true,
	}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 3 {
		t.Errorf("got %d repos, want 3: %v", len(repos), names(repos))
	}
}
