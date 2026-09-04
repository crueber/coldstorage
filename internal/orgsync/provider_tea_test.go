package orgsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The tea fixtures are the real captured shapes (§11.2): a chatter line
// before the JSON, the {"ok":..,"data":[…]} wrapper, API rows with
// ssh_url/clone_url/owner{login}, and tea 0.15.1's compact repos-list row
// (owner string, ssh, no https URL).

const teaPage1 = `{"ok":true,"data":[
  {"name":"membership-db","owner":{"login":"mhs"},"ssh_url":"git@gitea.example:mhs/membership-db.git","clone_url":"https://gitea.example/mhs/membership-db.git","archived":false,"fork":false},
  {"name":"side-project","owner":{"login":"friends"},"ssh_url":"git@gitea.example:friends/side-project.git","clone_url":"https://gitea.example/friends/side-project.git","archived":false,"fork":false},
  {"name":"legacy","owner":{"login":"mhs"},"ssh_url":"git@gitea.example:mhs/legacy.git","clone_url":"https://gitea.example/mhs/legacy.git","archived":true,"fork":false}
]}`

const teaPage2 = `{"ok":true,"data":[
  {"name":"membership-db","owner":"mhs","type":"own","ssh":"git@gitea.example:mhs/membership-db.git"}
]}`

func TestParseTeaShapes(t *testing.T) {
	// Chatter line before the JSON; parsing starts at the first bracket.
	repos, err := parseTeaRepos("NOTE: login mhs in use\n" + teaPage1)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 3 {
		t.Fatalf("parsed %d repos, want 3", len(repos))
	}
	r := repos[0]
	if r.Name != "membership-db" || r.OwnerLogin != "mhs" ||
		r.SSHURL != "git@gitea.example:mhs/membership-db.git" ||
		r.HTTPSURL != "https://gitea.example/mhs/membership-db.git" ||
		r.Archived || r.Fork {
		t.Errorf("row 0 = %+v", r)
	}

	// tea 0.15.1 compact schema: owner is a string, ssh is the only URL,
	// archived/fork are unknowable. The https loss is documented (§11.2).
	compact, err := parseTeaRepos(`chatter
{"ok":true,"data":[{"name":"c","owner":"mhs","type":"own","ssh":"git@gitea.example:mhs/c.git"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if compact[0].OwnerLogin != "mhs" ||
		compact[0].SSHURL != "git@gitea.example:mhs/c.git" ||
		compact[0].HTTPSURL != "" {
		t.Errorf("compact row = %+v", compact[0])
	}

	// A `data: []` wrapper is a real empty answer, not a parse failure.
	empty, err := parseTeaRepos(`{"ok":true,"data":[]}`)
	if err != nil {
		t.Fatalf("empty data must parse: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("empty data returned %d rows", len(empty))
	}

	// Some builds print a bare array; tolerate it.
	bare, err := parseTeaRepos(`[{"name":"b","owner":{"username":"mhs"},"ssh_url":"git@gitea.example:mhs/b.git","clone_url":"https://gitea.example/mhs/b.git"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if bare[0].OwnerLogin != "mhs" {
		t.Errorf("bare array owner = %+v", bare[0].OwnerLogin)
	}

	if _, err := parseTeaRepos("tea: no such login"); err == nil {
		t.Error("output with no JSON must be an error, not an empty listing")
	}
}

// TestListGitea runs the whole exec path through a fake `tea`: page 1
// answers with the membership-db case — the right repo, plus repos from
// other namespaces that the advisory owner parameter drags in — and page 2
// answers with the same repo in the compact schema. The listing must pin to
// the registered owner, dedupe, skip foreign rows, and stop.
func TestListGitea(t *testing.T) {
	bin := t.TempDir()
	writeScript(t, filepath.Join(bin, "tea"), `echo "$@" >> "$TEA_LOG"
echo "NOTE: using login mhs"
case "$*" in
  *page=3*) echo '{"ok":true,"data":[]}' ;;
  *page=2*) echo '`+teaPage2+`' ;;
  *) echo '`+teaPage1+`' ;;
esac
`)
	putOnPATH(t, bin)
	log := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("TEA_LOG", log)

	repos, err := ListRepos(Source{
		Provider: "gitea", Host: "gitea.example", Owner: "mhs", Login: "mhs",
	}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Exactly the owned, unarchived repo; side-project (foreign namespace)
	// is pinned out, legacy (archived) is filtered, and the duplicate from
	// page 2 is deduped.
	if got, want := names(repos), []string{"membership-db"}; len(got) != len(want) || got[0] != want[0] {
		t.Errorf("ListRepos = %v, want %v", got, want)
	}
	if repos[0].SSHURL != "git@gitea.example:mhs/membership-db.git" {
		t.Errorf("dedupe must keep the first occurrence: %+v", repos[0])
	}

	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	args := string(raw)
	if !strings.Contains(args, "-l mhs") {
		t.Error("a configured tea login must be passed as -l")
	}
	for _, want := range []string{"/repos/search?owner=mhs&limit=100&page=1", "page=2"} {
		if !strings.Contains(args, want) {
			t.Errorf("tea args missing %q: %s", want, args)
		}
	}
	if !strings.Contains(args, "page=3") {
		t.Error("a page of pinned-out foreign rows must not stop pagination")
	}
	if strings.Contains(args, "page=4") {
		t.Error("pagination must stop on the empty page")
	}
}

// TestListGiteaPinningIsCaseInsensitive: some instances answer with the
// owner in different case than the registration.
func TestListGiteaPinningIsCaseInsensitive(t *testing.T) {
	rows, err := parseTeaRepos(`{"ok":true,"data":[
	  {"name":"mixed-case","owner":{"login":"MHS"},"ssh_url":"git@gitea.example:mhs/mixed-case.git"}
	]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(rows[0].OwnerLogin, "mhs") {
		t.Fatalf("fixture broken: %q", rows[0].OwnerLogin)
	}
	src := Source{Owner: "mhs"}
	if skipReason(rows[0], src) != "" {
		t.Error("pinning is case-insensitive; this row must survive")
	}
	if got := filterRepos(rows, src); len(got) != 1 {
		t.Errorf("filterRepos = %v, want the row kept", got)
	}
}
