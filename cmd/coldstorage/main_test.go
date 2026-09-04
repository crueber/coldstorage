package main

import (
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
