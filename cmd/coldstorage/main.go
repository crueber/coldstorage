// coldstorage — the dashboard (GO-PORT-SPEC.md §12). The binary has no
// subcommands: invoking it opens the TUI over the configured fleet. The only
// flags are the config override, a version stamp, and a hidden test
// affordance. version/commit/date are stamped at release time by goreleaser
// (ldflags); a build from source reports "dev".
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/crueber/coldstorage/internal/config"
	"github.com/crueber/coldstorage/internal/tui"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	configPath := flag.String("config", "", "path to config.toml (default: the platform config dir)")
	quitAfter := flag.Duration("quit-after", 0, "hidden test affordance: exit cleanly after this duration")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		v, c, d := stamped(version, commit, date)
		fmt.Printf("coldstorage %s (commit %s, built %s)\n", v, c, d)
		return
	}

	paths, err := config.ResolvePaths()
	if err != nil {
		fmt.Fprintln(os.Stderr, "coldstorage: resolving platform paths:", err)
		os.Exit(1)
	}

	file := *configPath
	if file == "" {
		file = paths.ConfigFile
	}

	cfg, err := config.Load(file)
	// §4: the dashboard falls back to defaults with a visible warning
	// rather than refusing to open.
	var warnings []string
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("config: %v; using defaults", err))
		cfg = config.Default()
	}
	// §4: org problems are reported, never silently repaired.
	for _, problem := range cfg.OrgProblems() {
		warnings = append(warnings, "org: "+problem)
	}

	// §12: the dashboard speaks the shell's colors — resolved once, before
	// the program asks the terminal about its background.
	tui.ApplyTheme(cfg.UI.Theme)

	// §15: the cache paints the first frame; a missing or unreadable cache
	// is a non-event.
	cached := tui.LoadCache(paths.CacheDir)

	eng := tui.NewEngine(cfg, paths.CacheDir, cached)
	m := tui.New(cfg, paths.CacheDir, cached, warnings, eng)

	opts := []tea.ProgramOption{}
	if *quitAfter > 0 {
		// Headless (test) runs have no TTY: nil input keeps the event
		// loop alive without reading a terminal.
		opts = append(opts, tea.WithInput(nil))
	}
	prog := tea.NewProgram(m, opts...)
	eng.Send(prog.Send)

	if *quitAfter > 0 {
		go func() {
			time.Sleep(*quitAfter)
			prog.Quit()
		}()
	}

	if _, err := prog.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "coldstorage:", err)
		os.Exit(1)
	}
	eng.Close()
}

// stamped fills unstamped slots from the build info that go install records
// for versioned module installs: without it, a `go install ...@v1.0.9`
// binary reports "dev" and every update looks like it did not take. Builds
// inside a git worktree also carry the revision and its time.
func stamped(version, commit, date string) (string, string, string) {
	if version != "dev" && commit != "none" && date != "unknown" {
		return version, commit, date
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return version, commit, date
	}
	return stampedFrom(version, commit, date, bi)
}

// stampedFrom is the fill logic, split for tests: the test binary has its
// own build info, so the filler takes it as a value.
func stampedFrom(version, commit, date string, bi *debug.BuildInfo) (string, string, string) {
	if version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		version = strings.TrimPrefix(bi.Main.Version, "v")
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if commit == "none" && s.Value != "" {
				commit = s.Value
				if len(commit) > 7 {
					commit = commit[:7]
				}
			}
		case "vcs.time":
			if date == "unknown" && s.Value != "" {
				date = s.Value
			}
		}
	}
	return version, commit, date
}
