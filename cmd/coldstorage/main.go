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
		fmt.Printf("coldstorage %s (commit %s, built %s)\n", version, commit, date)
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
