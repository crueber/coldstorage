package tui

// The nameless-path incident: the org form's vim navigation (h/j/k/l) must
// not eat text typed into the text rows — typing "/home/user" once cycled
// the row backward on the h, and forward on the j and the l.

import (
	"os"
	"strings"
	"testing"
)

func TestOrgFormTypesPathVerbatim(t *testing.T) {
	restore := stubProbe(t, func(name string, args ...string) (string, error) {
		if name == "gh" {
			return "\n  ✓ Logged in to github.com account octocat (keyring)\n", nil
		}
		return "", os.ErrNotExist
	})
	defer restore()

	m, _ := orgTestModel(t)
	m, _ = sendKey(t, m, "A")
	m, cmd := sendKey(t, m, "a")
	m = runCmd(t, m, cmd)
	if m.mode != modeOrgForm {
		t.Fatalf("mode = %v, want the org form", m.mode)
	}
	// Land on the path row: provider, then down through owner.
	m, _ = sendKey(t, m, "down")
	m, _ = sendKey(t, m, "down")
	m, _ = sendKey(t, m, "down")
	f := m.orgForm
	if f.rows()[f.cursor].id != "path" {
		t.Fatalf("cursor on %q, want path", f.rows()[f.cursor].id)
	}

	for _, r := range []rune("/home/user/repo") {
		m, _ = sendKey(t, m, string(r))
	}
	if m.orgForm.path != "/home/user/repo" {
		t.Errorf("path = %q, want /home/user/repo", m.orgForm.path)
	}
	if !m.orgForm.pathTouched {
		t.Error("typing must retire the path auto-fill")
	}
}

func TestOrgFormTextRowsNavigateByArrows(t *testing.T) {
	restore := stubProbe(t, func(name string, args ...string) (string, error) {
		if name == "gh" {
			return "\n  ✓ Logged in to github.com account octocat (keyring)\n", nil
		}
		return "", os.ErrNotExist
	})
	defer restore()

	m, _ := orgTestModel(t)
	m, _ = sendKey(t, m, "A")
	m, cmd := sendKey(t, m, "a")
	m = runCmd(t, m, cmd)
	// From provider, the owner row is two down (host sits between); j must
	// NOT move off it — j is text on the owner row.
	m, _ = sendKey(t, m, "down")
	m, _ = sendKey(t, m, "down")
	f := m.orgForm
	if f.rows()[f.cursor].id != "owner" {
		t.Fatalf("cursor on %q, want owner", f.rows()[f.cursor].id)
	}
	m, _ = sendKey(t, m, "j")
	if f.rows()[m.orgForm.cursor].id != "owner" {
		t.Error("j on the owner row must type, not navigate")
	}
	if !strings.HasSuffix(m.orgForm.owner, "j") {
		t.Errorf("owner = %q, want j typed", m.orgForm.owner)
	}
	// Arrows navigate one row at a time, even off a text row.
	m, _ = sendKey(t, m, "up")
	if f2 := m.orgForm; f2.rows()[f2.cursor].id != "host" {
		t.Errorf("up from owner landed on %q, want host", f2.rows()[f2.cursor].id)
	}
}
