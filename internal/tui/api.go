// The surface main.go consumes: constructors and the small wiring glue, so
// the entrypoint never touches the package's internals.
package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/crueber/coldstorage/internal/config"
)

// LoadCache reads the probe cache (§15). Exported for main.
func LoadCache(dir string) map[string]RepoState { return loadCache(dir) }

// NewEngine builds the background pipeline over the config and cache.
func NewEngine(cfg config.Config, cacheDir string, cache map[string]RepoState) *Engine {
	return &Engine{engine: newEngine(cfg, cacheDir, cache)}
}

// Engine is the exported handle to the background pipeline: main wires Send
// after constructing the program and calls Close on exit.
type Engine struct{ *engine }

// Send wires the program's message sink so background work can reach the UI.
func (e Engine) Send(fn func(msg tea.Msg)) {
	e.send = func(m any) { fn(m) }
}

// New assembles the dashboard model.
func New(cfg config.Config, cacheDir string, cached map[string]RepoState, warnings []string, eng *Engine) tea.Model {
	var inner *engine
	if eng != nil {
		inner = eng.engine
	}
	return newModel(cfg, cacheDir, cached, warnings, inner)
}
