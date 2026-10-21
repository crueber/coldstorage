package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Ensure writes the default configuration to path when nothing exists there
// yet. A fresh install must get a real file, not just in-memory defaults:
// the org manager and the group-color overlay save settings, and a save with
// no config file behind it is how a working setup once looked like it kept
// forgetting itself. An existing file is never touched — the owner's edits
// win. Callers pass the platform location, never an explicit -config path:
// a typo in a hand-typed path must stay visible instead of silently
// becoming a new file.
func Ensure(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("coldstorage: %s: %w", path, err)
	}
	if err := Save(path, Default()); err != nil {
		return fmt.Errorf("coldstorage: %s: %w", path, err)
	}
	return nil
}

// Save writes the configuration to path (§3). The write is atomic — encode
// to a temporary file in the same directory, fsync, then rename — because a
// torn config.toml is worse than no config: the dashboard falls back to
// defaults with a warning, and every org registration the owner built in the
// org manager silently vanishes. The directory is created on demand so a
// first save on a fresh install does not need a separate init step.
//
// The struct tags are the single source of key names, so a file this writes
// always round-trips through Load's strict unknown-key check.
func Save(path string, cfg Config) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.toml")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename

	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, path)
}
