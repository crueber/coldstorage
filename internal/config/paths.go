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
// Support, but a config.toml under ~/.config/coldstorage takes precedence:
// configs synced by dotfile managers land in ~/.config, and the dashboard
// must keep reading them without a migration step.
//
// The precedence is decided by the config *file*, not by the directory. A
// bare ~/.config/coldstorage (a leftover mkdir, or a dotfile manager that
// creates the tree before the file) once shadowed a real config in
// Application Support, so a working setup silently ran on the default
// ~/Projects root. A directory only wins when it actually holds the file
// the owner is editing.
//
// The cache has no such rule — it is disposable by design, and a missing or
// unreadable cache is a non-event.
func ResolvePaths() (Paths, error) {
	var p Paths

	if configRoot := os.Getenv("XDG_CONFIG_HOME"); configRoot != "" {
		p.ConfigDir = filepath.Join(configRoot, "coldstorage")
	} else {
		platformRoot, err := os.UserConfigDir()
		if err != nil {
			return Paths{}, err
		}
		home, _ := os.UserHomeDir()
		p.ConfigDir = resolveConfigDir(runtime.GOOS, home, platformRoot)
	}

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

// resolveConfigDir picks the directory coldstorage reads and writes its
// config in. On macOS the platform default is ~/Library/Application Support,
// but ~/.config/coldstorage wins when it already contains a config.toml:
// synced configs keep working without the owner being asked to move
// anything. Everywhere else the platform default is the only candidate (on
// Linux it is already ~/.config or $XDG_CONFIG_HOME).
//
// Tests drive this with a synthetic GOOS and temp dirs, so the macOS rule
// is exercised on every platform, not just on a Mac.
func resolveConfigDir(goos, home, platformRoot string) string {
	platformDir := filepath.Join(platformRoot, "coldstorage")
	if goos != "darwin" || home == "" {
		return platformDir
	}
	legacy := filepath.Join(home, ".config", "coldstorage")
	if legacy == platformDir || !hasConfigFile(legacy) {
		return platformDir
	}
	return legacy
}

// hasConfigFile reports whether dir already holds a config file. A missing
// or dangling path, an unreadable directory, or a directory named
// config.toml all count as "no config here", so resolution falls through to
// the next candidate instead of pointing the loader at a dead end.
func hasConfigFile(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "config.toml"))
	return err == nil && !info.IsDir()
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
