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
