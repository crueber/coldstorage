package main

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
)

// A go-installed binary carries its version in Main.Version, not ldflags —
// without the fallback it reported "dev" and every install looked like the
// update did not take.
func TestStampedFillsFromBuildInfo(t *testing.T) {
	bi := &debug.BuildInfo{Main: debug.Module{Version: "v1.0.9"}}
	bi.Settings = append(bi.Settings,
		debug.BuildSetting{Key: "vcs.revision", Value: "ce43d352e1d0c20dc0a828049a8b7c6fc68d36bd"},
		debug.BuildSetting{Key: "vcs.time", Value: "2026-09-04T21:17:11Z"},
	)
	v, c, d := stampedFrom("dev", "none", "unknown", bi)
	if v != "1.0.9" {
		t.Errorf("version = %q, want 1.0.9 from Main.Version", v)
	}
	if c != "ce43d35" {
		t.Errorf("commit = %q, want the short revision", c)
	}
	if d != "2026-09-04T21:17:11Z" {
		t.Errorf("date = %q, want the vcs time", d)
	}
}

func TestStampedLdflagsWin(t *testing.T) {
	v, c, d := stamped("1.0.8", "abc1234", "2026-09-04T00:00:00Z")
	if v != "1.0.8" || c != "abc1234" || d != "2026-09-04T00:00:00Z" {
		t.Errorf("ldflags-stamped values = %q %q %q, want them preserved", v, c, d)
	}
}

// §3: a fresh install gets a config.toml at the platform location before the
// first load, so the dashboard has a file to save into from the start.
func TestLoadConfigCreatesDefaultFile(t *testing.T) {
	platform := filepath.Join(t.TempDir(), "coldstorage", "config.toml")
	cfg, warnings := loadConfig(platform, "")
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if _, err := os.Stat(platform); err != nil {
		t.Fatalf("config was not created: %v", err)
	}
	if len(cfg.Roots) != 1 || cfg.Roots[0] != "~/Projects" {
		t.Errorf("Roots = %v, want the default root", cfg.Roots)
	}
}

func TestLoadConfigKeepsExistingFile(t *testing.T) {
	platform := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(platform, []byte("roots = [\"~/code\"]\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, warnings := loadConfig(platform, "")
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if len(cfg.Roots) != 1 || cfg.Roots[0] != "~/code" {
		t.Errorf("Roots = %v; the owner's file was not loaded", cfg.Roots)
	}
}

// An explicit -config path is the owner's business: a missing one means
// defaults for this run, not a new file at the typed path or at the platform
// location.
func TestLoadConfigDoesNotCreateOverride(t *testing.T) {
	platform := filepath.Join(t.TempDir(), "platform", "config.toml")
	override := filepath.Join(t.TempDir(), "explicit", "config.toml")

	cfg, warnings := loadConfig(platform, override)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if _, err := os.Stat(override); !os.IsNotExist(err) {
		t.Errorf("override file was created: %v", err)
	}
	if _, err := os.Stat(platform); !os.IsNotExist(err) {
		t.Errorf("platform file was created despite the override: %v", err)
	}
	if len(cfg.Roots) != 1 || cfg.Roots[0] != "~/Projects" {
		t.Errorf("Roots = %v, want defaults", cfg.Roots)
	}
}
