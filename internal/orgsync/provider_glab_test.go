package orgsync

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// glabPage builds one repo-list page as a bare JSON array — the shape glab
// prints for --output json.
func glabPage(items ...string) string {
	return "[" + strings.Join(items, ",") + "]"
}

func glabItem(name string, extra ...string) string {
	return fmt.Sprintf(
		`{"name":%q,"ssh_url_to_repo":"git@gitlab.example:acme/%[1]s.git","http_url_to_repo":"https://gitlab.example/acme/%[1]s.git"%s}`,
		name, strings.Join(extra, ","))
}

// writeGlabFixture installs a fake glab: the namespace lookup answers the
// given kind, repo-list pages come from fixture files, and every invocation
// must carry GITLAB_HOST or the fake fails the listing — pinning the §11.2
// rule that self-hosted instances resolve through GITLAB_HOST.
func writeGlabFixture(t *testing.T, kind string, pages ...string) {
	t.Helper()
	bin := t.TempDir()
	fix := t.TempDir()

	ns := fmt.Sprintf(`{"id":7,"name":"Acme","path":"acme","kind":%q}`, kind)
	if err := os.WriteFile(filepath.Join(fix, "ns.json"), []byte(ns), 0o644); err != nil {
		t.Fatal(err)
	}
	for i, page := range pages {
		if err := os.WriteFile(filepath.Join(fix, fmt.Sprintf("page%d.json", i+1)), []byte(page), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	script := `echo "$@" >> "$GLAB_LOG"
if [ "$GITLAB_HOST" != "gitlab.example" ]; then
  echo "GITLAB_HOST is '$GITLAB_HOST'" >&2
  exit 9
fi
case "$*" in
  *namespaces/acme*) cat "$GLAB_FIX/ns.json" ;;
`
	for i := range pages {
		script += fmt.Sprintf("  *\"--page %d\"*) cat \"$GLAB_FIX/page%d.json\" ;;\n", i+1, i+1)
	}
	script += `  *) cat "$GLAB_FIX/page1.json" ;;
esac
`
	writeScript(t, filepath.Join(bin, "glab"), script)
	putOnPATH(t, bin)
	t.Setenv("GLAB_FIX", fix)
}

func TestParseGlabNamespace(t *testing.T) {
	for _, kind := range []string{"user", "group"} {
		got, err := parseGlabNamespace(fmt.Sprintf(`{"id":7,"path":"acme","kind":%q}`, kind))
		if err != nil || got != kind {
			t.Errorf("kind %q: got %q, %v", kind, got, err)
		}
	}
	if _, err := parseGlabNamespace(`{"kind":"mystery"}`); err == nil {
		t.Error("unknown kind must be an error, not a guessed scope")
	}
	if _, err := parseGlabNamespace("glab: auth required"); err == nil {
		t.Error("non-JSON output must be an error")
	}
}

func TestParseGlabPage(t *testing.T) {
	repos, err := parseGlabPage(glabPage(
		glabItem("ops-tool", `,"namespace":{"path":"acme"}`),
		glabItem("stale-proj", `,"archived":true`),
		glabItem("mirrored", `,"forked_from_project":{"id":1,"name":"other"}`),
		glabItem("null-fork", `,"forked_from_project":null`),
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 4 {
		t.Fatalf("parsed %d rows, want 4", len(repos))
	}
	if repos[0].OwnerLogin != "acme" || repos[0].SSHURL == "" || repos[0].HTTPSURL == "" {
		t.Errorf("row 0 = %+v", repos[0])
	}
	if !repos[1].Archived {
		t.Error("stale-proj must be archived")
	}
	if !repos[2].Fork {
		t.Error("presence of forked_from_project means fork")
	}
	if repos[3].Fork {
		t.Error("a null forked_from_project is not a fork")
	}
}

// TestListGitLabGroup pins the group path end to end: namespace kind group,
// --group scope, --include-subgroups honored, pagination continuing to a
// second page and stopping at the short page, GITLAB_HOST on every child.
func TestListGitLabGroup(t *testing.T) {
	// Page 1 is a full page of 100 (built programmatically), page 2 is
	// short: the listing must stop there.
	full := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		full = append(full, glabItem(fmt.Sprintf("p%03d", i)))
	}
	writeGlabFixture(t, "group", glabPage(full...), glabPage(
		glabItem("tail-one"),
	))

	log := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("GLAB_LOG", log)

	repos, err := ListRepos(Source{
		Provider: "gitlab", Host: "gitlab.example", Owner: "acme",
		IncludeSubgroups: true,
	}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 101 {
		t.Fatalf("got %d repos, want 101 across two pages", len(repos))
	}
	if repos[100].Name != "tail-one" {
		t.Errorf("row 100 = %+v", repos[100])
	}

	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	args := string(raw)
	for _, want := range []string{
		"api namespaces/acme",
		"repo list --group acme --include-subgroups",
		"--per-page 100",
		"--page 2",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("glab args missing %q", want)
		}
	}
	if strings.Contains(args, "--page 3") {
		t.Error("pagination must stop at the short page")
	}
	if n := strings.Count(args, "--page 1"); n != 1 {
		t.Errorf("expected exactly one first page, got %d", n)
	}
}

// TestListGitLabUserKind pins the user path: kind user switches the scope
// flag and --include-subgroups is never sent (it is group-only).
func TestListGitLabUserKind(t *testing.T) {
	writeGlabFixture(t, "user", glabPage(
		glabItem("solo-project", `,"namespace":{"path":"acme"}`),
	))

	log := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("GLAB_LOG", log)

	repos, err := ListRepos(Source{
		Provider: "gitlab", Host: "gitlab.example", Owner: "acme",
		IncludeSubgroups: true,
	}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(repos), []string{"solo-project"}; len(got) != len(want) || got[0] != want[0] {
		t.Errorf("ListRepos = %v, want %v", got, want)
	}

	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	args := string(raw)
	if !strings.Contains(args, "repo list --user acme") {
		t.Errorf("glab args missing user scope: %s", args)
	}
	if strings.Contains(args, "--include-subgroups") {
		t.Error("--include-subgroups is group-only and must not be sent for a user")
	}
}
