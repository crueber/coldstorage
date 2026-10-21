package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A fresh install must end up with a real file to save into, not just the
// in-memory defaults.
func TestEnsureCreatesDefaultConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coldstorage", "config.toml")
	if err := Ensure(path); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat created config: %v", err)
	}
	if info.IsDir() {
		t.Fatalf("%s is a directory, want a file", path)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load(created): %v", err)
	}
	if len(c.Roots) != 1 || c.Roots[0] != "~/Projects" {
		t.Errorf("Roots = %v, want the default ~/Projects root", c.Roots)
	}
	if c.MaxDepth != Default().MaxDepth {
		t.Errorf("MaxDepth = %d, want %d", c.MaxDepth, Default().MaxDepth)
	}
	if c.UI.DefaultSort != Default().UI.DefaultSort {
		t.Errorf("DefaultSort = %q, want %q", c.UI.DefaultSort, Default().UI.DefaultSort)
	}
}

func TestEnsureNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	custom := "roots = [\"~/code\"]\n"
	if err := os.WriteFile(path, []byte(custom), 0o600); err != nil {
		t.Fatalf("write custom config: %v", err)
	}

	if err := Ensure(path); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(got) != custom {
		t.Errorf("Ensure overwrote the owner's config:\n%s", got)
	}
}

func TestEnsureReportsUnusablePath(t *testing.T) {
	// A path whose parent is a regular file cannot be created; the caller
	// needs the error so it can warn instead of pretending the save target
	// exists.
	parent := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatalf("write parent: %v", err)
	}
	if err := Ensure(filepath.Join(parent, "config.toml")); err == nil {
		t.Fatal("Ensure on an unusable path returned nil")
	}
}
