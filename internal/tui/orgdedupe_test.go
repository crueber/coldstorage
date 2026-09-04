// The double-listing incident: adding an org over a directory another
// registration already covers must replace the older registration, not
// double-list the directory.
package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/crueber/coldstorage/internal/config"
)

func TestSaveCandidateReplacesExistingDirectory(t *testing.T) {
	root := t.TempDir()
	m, _ := orgTestModel(t, demoOrg(root, "acme"))
	existing := demoOrg(root, "acme") // path: root/acme

	// A NEW registration (not an edit) over the same directory, under a
	// different owner name.
	m.orgForm = orgForm{
		provider:  "github",
		host:      "github.com",
		owner:     "acme-v2",
		path:      existing.Path,
		probeDone: true,
		authed:    []orgAuth{{Provider: "github", Hosts: []string{"github.com"}}},
		editing:   -1,
	}
	m.orgForm.pathTouched = true

	cand := m.orgCandidate()
	if len(cand.Orgs) != 1 {
		t.Fatalf("candidate has %d orgs, want 1 — the directory must not double-list", len(cand.Orgs))
	}
	if cand.Orgs[0].Owner != "acme-v2" {
		t.Errorf("survivor = %q, want the newer acme-v2", cand.Orgs[0].Owner)
	}
	// autoRoot may add the org's parent (the fixture's checkout lives
	// outside the config's own roots), but it must never grow DUPLICATE
	// roots — that is the other half of double-listing.
	seen := map[string]bool{}
	for _, r := range cand.Roots {
		e := config.Expand(r)
		if seen[e] {
			t.Errorf("duplicate root after save: %v", cand.Roots)
			break
		}
		seen[e] = true
	}
}

func TestSaveCandidateReplacesOnEdit(t *testing.T) {
	root := t.TempDir()
	m, _ := orgTestModel(t, demoOrg(root, "acme"), demoOrg(root, "other"))
	// Editing org 0 onto org 1's directory: the edited registration is the
	// newer intent, but list position says org 1 is later — §11.4 keeps the
	// later, so the edit must retarget the surviving registration's fields.
	m.orgForm = orgForm{
		provider:  "github",
		host:      "github.com",
		owner:     "acme-renamed",
		path:      demoOrg(root, "other").Path,
		probeDone: true,
		authed:    []orgAuth{{Provider: "github", Hosts: []string{"github.com"}}},
		editing:   0,
	}
	m.orgForm.pathTouched = true

	cand := m.orgCandidate()
	if len(cand.Orgs) != 1 {
		t.Fatalf("candidate has %d orgs, want 1", len(cand.Orgs))
	}
	if cand.Orgs[0].Owner != "other" {
		t.Errorf("later registration must win on collision: %+v", cand.Orgs[0])
	}
}

// The wedged-pricing incident: an interrupted clone left .git debris with
// no HEAD, and orgDiskRepos counted it as a checkout — the repo could
// neither update nor re-clone. Debris is not a checkout.
func TestOrgDiskReposExcludesDebris(t *testing.T) {
	path := t.TempDir()
	dead := filepath.Join(path, "pricing")
	if err := os.MkdirAll(filepath.Join(dead, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(path, "walhub")
	if err := os.MkdirAll(filepath.Join(live, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(live, ".git", "HEAD"), []byte("ref: refs/heads/main"), 0o644)

	got := orgDiskRepos(path)
	if len(got) != 1 || got[0] != "walhub" {
		t.Errorf("orgDiskRepos = %v, want only the live checkout", got)
	}
	if !isCheckoutDir(live) {
		t.Error("the live checkout must still count")
	}
}
