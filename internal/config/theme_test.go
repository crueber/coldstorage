package config

import (
	"strings"
	"testing"
)

func TestThemeKeyLoads(t *testing.T) {
	c, err := Load(writeConfig(t, "[ui]\ntheme = \"light\"\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.UI.Theme != "light" {
		t.Errorf("theme = %q, want light", c.UI.Theme)
	}
}

func TestThemeDefaultsToAuto(t *testing.T) {
	c, err := Load(writeConfig(t, ""))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.UI.Theme != "auto" {
		t.Errorf("absent theme = %q, want auto", c.UI.Theme)
	}
}

func TestUnknownUIKeyStillHardError(t *testing.T) {
	_, err := Load(writeConfig(t, "[ui]\ntheme = \"dark\"\nbogus = 1\n"))
	if err == nil || !strings.Contains(err.Error(), "ui.bogus") {
		t.Errorf("theme must not widen the accepted key set: %v", err)
	}
}

func TestReleaseSectionSubkeysAreValidated(t *testing.T) {
	_, err := Load(writeConfig(t, "[release]\ntag_patern = \"*\"\n"))
	if err == nil || !strings.Contains(err.Error(), "release.tag_patern") {
		t.Errorf("a typo'd release subkey must be a hard error, got %v", err)
	}
	if _, err := Load(writeConfig(t, "[release]\ntag_pattern = \"*[0-9]*\"\n")); err != nil {
		t.Errorf("a valid release section must load: %v", err)
	}
}
