package config

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

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
