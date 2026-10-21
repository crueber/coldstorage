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

// TestResolveConfigDirMacLayout drives the macOS rule with a synthetic GOOS,
// so it runs on every platform. The regression it pins: an empty
// ~/.config/coldstorage must not shadow a real config.toml in Application
// Support, and a config.toml in ~/.config must still win when it exists.
func TestResolveConfigDirMacLayout(t *testing.T) {
	home := t.TempDir()
	platformRoot := filepath.Join(t.TempDir(), "Library", "Application Support")
	legacy := filepath.Join(home, ".config", "coldstorage")
	platformDir := filepath.Join(platformRoot, "coldstorage")

	// Neither location has a config yet: a fresh install gets the
	// XDG-style home, which is where the dashboard creates its file.
	if got := resolveConfigDir("darwin", home, platformRoot); got != legacy {
		t.Fatalf("fresh install ConfigDir = %q, want %q", got, legacy)
	}

	// A real config in Application Support is honored even when an empty
	// ~/.config/coldstorage tree exists. The old directory-based check
	// picked the empty tree and silently ran on defaults.
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	if err := os.MkdirAll(platformDir, 0o755); err != nil {
		t.Fatalf("mkdir platform: %v", err)
	}
	writeConfigFile(t, filepath.Join(platformDir, "config.toml"), `roots = ["~/code"]`)
	got := resolveConfigDir("darwin", home, platformRoot)
	if got != platformDir {
		t.Fatalf("ConfigDir = %q, want the Application Support config", got)
	}
	cfg, err := Load(filepath.Join(got, "config.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Roots) != 1 || cfg.Roots[0] != "~/code" {
		t.Errorf("Roots = %v, want the Application Support config's roots", cfg.Roots)
	}

	// Once the owner has a config synced under ~/.config, that file wins.
	writeConfigFile(t, filepath.Join(legacy, "config.toml"), `roots = ["~/dev"]`)
	got = resolveConfigDir("darwin", home, platformRoot)
	if got != legacy {
		t.Fatalf("ConfigDir = %q, want the synced ~/.config location", got)
	}
	cfg, err = Load(filepath.Join(got, "config.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Roots) != 1 || cfg.Roots[0] != "~/dev" {
		t.Errorf("Roots = %v, want the synced config's roots", cfg.Roots)
	}

	// Non-Darwin platforms never consult ~/.config separately: their
	// platform default already is ~/.config (or $XDG_CONFIG_HOME).
	linuxRoot := filepath.Join(t.TempDir(), ".config")
	if got := resolveConfigDir("linux", home, linuxRoot); got != filepath.Join(linuxRoot, "coldstorage") {
		t.Errorf("linux ConfigDir = %q, want the platform default", got)
	}
}

func TestResolveConfigDirMacMissingHome(t *testing.T) {
	platformRoot := filepath.Join(t.TempDir(), "Library", "Application Support")
	if got := resolveConfigDir("darwin", "", platformRoot); got != filepath.Join(platformRoot, "coldstorage") {
		t.Errorf("ConfigDir = %q, want the platform default", got)
	}
}

func writeConfigFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content+"\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
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
