package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Paths names the on-disk locations of §3. The directories are derived
// once and shared by everything that reads or writes state, so the log,
// the cache, and the config never drift apart about where "here" is.
type Paths struct {
	ConfigDir  string
	CacheDir   string
	LogFile    string
	ConfigFile string
}

// ResolvePaths resolves the platform locations of §3. The spec spells the
// function `Paths()` returning a `Paths`, which a single Go package cannot
// declare twice; here the function is ResolvePaths.
//
// $XDG_CONFIG_HOME and $XDG_CACHE_HOME win whenever set, on every
// platform. On macOS the platform config root is ~/Library/Application
// Support, but an existing ~/.config/coldstorage takes precedence: configs
// synced by dotfile managers land in ~/.config, and the dashboard must
// keep reading them without a migration step. The cache has no such rule —
// it is disposable by design, and a missing or unreadable cache is a
// non-event.
func ResolvePaths() (Paths, error) {
	var p Paths

	configRoot := os.Getenv("XDG_CONFIG_HOME")
	if configRoot == "" {
		if runtime.GOOS == "darwin" {
			if candidate, ok := darwinLegacyConfigRoot(); ok {
				configRoot = candidate
			}
		}
		if configRoot == "" {
			root, err := os.UserConfigDir()
			if err != nil {
				return Paths{}, err
			}
			configRoot = root
		}
	}
	p.ConfigDir = filepath.Join(configRoot, "coldstorage")

	cacheRoot := os.Getenv("XDG_CACHE_HOME")
	if cacheRoot == "" {
		root, err := os.UserCacheDir()
		if err != nil {
			return Paths{}, err
		}
		cacheRoot = root
	}
	p.CacheDir = filepath.Join(cacheRoot, "coldstorage")

	p.LogFile = filepath.Join(p.CacheDir, "coldstorage.log")
	p.ConfigFile = filepath.Join(p.ConfigDir, "config.toml")
	return p, nil
}

// darwinLegacyConfigRoot reports ~/.config when it already holds a
// coldstorage directory, so synced configs keep working without the owner
// being asked to move anything.
func darwinLegacyConfigRoot() (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	candidate := filepath.Join(home, ".config", "coldstorage")
	if _, err := os.Stat(candidate); err != nil {
		return "", false
	}
	return filepath.Join(home, ".config"), true
}

// Expand resolves a leading ~ to the home directory, so config values can
// stay portable across machines while every consumer works with absolute
// paths.
func Expand(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	switch {
	case p == "~":
		return home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:])
	}
	return p
}

// Contract is the inverse of Expand: it rewrites the home prefix back to ~
// so paths echoed to the user (and stored in the config) stay short and
// portable. Paths merely adjacent to home — /home/you vs /home/yourorg —
// are left alone.
func Contract(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	switch {
	case p == "~" || p == "~/":
		return "~"
	case p == home:
		return "~"
	case strings.HasPrefix(p, home+string(filepath.Separator)):
		rest := p[len(home)+len(string(filepath.Separator)):]
		if rest == "" {
			return "~" // "home/" is home with a trailing slash, not a child
		}
		return "~" + string(filepath.Separator) + rest
	}
	return p
}
