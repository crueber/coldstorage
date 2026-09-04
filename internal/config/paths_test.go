package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolvePathsHonorsXDG(t *testing.T) {
	tmp := t.TempDir()
	cfgRoot := filepath.Join(tmp, "xdg-config")
	cacheRoot := filepath.Join(tmp, "xdg-cache")
	t.Setenv("XDG_CONFIG_HOME", cfgRoot)
	t.Setenv("XDG_CACHE_HOME", cacheRoot)

	p, err := ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}
	if p.ConfigDir != filepath.Join(cfgRoot, "coldstorage") {
		t.Errorf("ConfigDir = %q, want under %q", p.ConfigDir, cfgRoot)
	}
	if p.CacheDir != filepath.Join(cacheRoot, "coldstorage") {
		t.Errorf("CacheDir = %q, want under %q", p.CacheDir, cacheRoot)
	}
	if p.ConfigFile != filepath.Join(cfgRoot, "coldstorage", "config.toml") {
		t.Errorf("ConfigFile = %q", p.ConfigFile)
	}
	if p.LogFile != filepath.Join(cacheRoot, "coldstorage", "coldstorage.log") {
		t.Errorf("LogFile = %q", p.LogFile)
	}
}

func TestResolvePathsPlatformDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")

	p, err := ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}
	wantConfig := filepath.Join(home, ".config", "coldstorage")
	if runtime.GOOS == "darwin" {
		wantConfig = filepath.Join(home, "Library", "Application Support", "coldstorage")
	}
	if p.ConfigDir != wantConfig {
		t.Errorf("ConfigDir = %q, want %q", p.ConfigDir, wantConfig)
	}
	wantCache := filepath.Join(home, ".cache", "coldstorage")
	if runtime.GOOS == "darwin" {
		wantCache = filepath.Join(home, "Library", "Caches", "coldstorage")
	}
	if p.CacheDir != wantCache {
		t.Errorf("CacheDir = %q, want %q", p.CacheDir, wantCache)
	}
}

func TestResolvePathsMacSyncedConfigRule(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the synced-config override only applies on macOS")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")

	// Without ~/.config/coldstorage the platform default wins.
	p, err := ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}
	if p.ConfigDir != filepath.Join(home, "Library", "Application Support", "coldstorage") {
		t.Errorf("ConfigDir = %q, want the macOS platform default", p.ConfigDir)
	}

	// An existing ~/.config/coldstorage means the config was synced there
	// by a dotfile manager; it must keep working without a migration.
	if err := os.MkdirAll(filepath.Join(home, ".config", "coldstorage"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p, err = ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}
	if p.ConfigDir != filepath.Join(home, ".config", "coldstorage") {
		t.Errorf("ConfigDir = %q, want the synced ~/.config location", p.ConfigDir)
	}
	if p.ConfigFile != filepath.Join(home, ".config", "coldstorage", "config.toml") {
		t.Errorf("ConfigFile = %q", p.ConfigFile)
	}
}

func TestExpandContractRoundTrip(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory to test against")
	}

	cases := []struct{ in, expanded, contracted string }{
		{"~", home, "~"},
		{"~/dev", filepath.Join(home, "dev"), "~/dev"},
		{"~/", home, "~"},
		{"/opt/elsewhere", "/opt/elsewhere", "/opt/elsewhere"},
		{"", "", ""},
		{"relative/path", "relative/path", "relative/path"},
		{"~other", "~other", "~other"},
		{home + "beyond", home + "beyond", home + "beyond"},
	}
	for _, tc := range cases {
		if got := Expand(tc.in); got != tc.expanded {
			t.Errorf("Expand(%q) = %q, want %q", tc.in, got, tc.expanded)
		}
		if got := Contract(tc.in); got != tc.contracted {
			t.Errorf("Contract(%q) = %q, want %q", tc.in, got, tc.contracted)
		}
		// The pair is an involution on paths that start at home.
		if got := Contract(Expand(tc.in)); got != tc.contracted {
			t.Errorf("Contract(Expand(%q)) = %q, want %q", tc.in, got, tc.contracted)
		}
	}
}
