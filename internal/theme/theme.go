// The theme resolution (§12 color grammar): the dashboard's verdict colors
// come from the environment's own scheme, not a palette this tool invented.
// Detection order is most-specific first — an explicit [ui] theme override,
// then Omarchy's active theme (the owner's Linux setup, read from its
// alacritty/kitty files), then the terminal's own background (OSC 11, which
// covers iTerm2, Terminal.app, Alacritty and friends on the mac), then the
// macOS system appearance, then COLORFGBG, then a dark default. Every step
// degrades silently: a theme it cannot read must never cost the dashboard
// its colors, only its exactness.
package theme

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Theme is the resolved color scheme: one color per §12 grammar slot, as a
// string lipgloss accepts (an ANSI index like "3" or a hex like "#7aa2f7").
type Theme struct {
	Dark   bool
	Name   string // e.g. "tokyo-night", "" for the generic palettes
	Source string // which detector decided: config, omarchy, terminal, macos, colorfgbg, default

	Dirty    string // working tree has changes
	Unpushed string // local commits ahead of upstream
	Release  string // commits or changes past the newest tag
	Conflict string // merge conflicts and hard errors
	Dim      string // secondary text
	Muted    string // chrome: status line, bare repos
	Accent   string // spinner, active widgets
	Header   string // table caption row

	SelectedBg string // selection highlight background
}

// Generic is the built-in palette for a dark or light terminal: the §12
// ANSI grammar, with the selection background the only slot that must
// change between the two backgrounds.
func Generic(dark bool) Theme {
	t := Theme{
		Dark:       dark,
		Source:     "default",
		Dirty:      "3",
		Unpushed:   "6",
		Release:    "5",
		Conflict:   "1",
		Dim:        "8",
		Muted:      "8",
		Accent:     "6",
		Header:     "8",
		SelectedBg: "236",
	}
	if !dark {
		t.SelectedBg = "253"
		t.Header = "245"
		t.Dim = "245"
		t.Muted = "245"
	}
	return t
}

// Detect resolves the theme. configured is the [ui] theme value ("",
// "auto", "dark" or "light"); termDark is the terminal's answer about its
// own background (the caller asks the terminal once, before the alternate
// screen takes over). Omarchy wins over the terminal query because its
// files carry exact accents, not just a dark/light verdict.
func Detect(configured string, termDark bool) Theme {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return detectHome(configured, termDark, home)
}

// detectHome is Detect with the home directory injected, so the Omarchy
// probe is testable without touching the running user's real config.
func detectHome(configured string, termDark bool, home string) Theme {
	switch strings.ToLower(strings.TrimSpace(configured)) {
	case "dark":
		t := Generic(true)
		t.Source = "config"
		return t
	case "light":
		t := Generic(false)
		t.Source = "config"
		return t
	}

	if home != "" {
		if t, ok := omarchyTheme(home); ok {
			return t
		}
	}

	t := Generic(termDark)
	t.Source = "terminal"
	if runtime.GOOS == "darwin" {
		// The terminal's answer may be a fallback guess (some emulators
		// never answer the query); on the mac the system appearance is the
		// authority Terminal.app itself follows.
		if d := macosDark(); d != termDark {
			t = Generic(d)
			t.Source = "macos"
		}
		return t
	}
	if dark, ok := colorFGBGDark(os.Getenv("COLORFGBG")); ok && dark != termDark {
		t = Generic(dark)
		t.Source = "colorfgbg"
	}
	return t
}

// omarchyTheme reads the active Omarchy theme: ~/.config/omarchy/current/
// theme is a symlink into a theme directory carrying alacritty.toml (and
// often kitty.conf). A missing, non-Omarchy, or unparseable setup returns
// false and the next detector decides.
func omarchyTheme(home string) (Theme, bool) {
	dir, err := filepath.EvalSymlinks(filepath.Join(home, ".config", "omarchy", "current", "theme"))
	if err != nil {
		return Theme{}, false
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return Theme{}, false
	}

	var pal palette
	if data, err := os.ReadFile(filepath.Join(dir, "alacritty.toml")); err == nil {
		pal, _ = parseAlacritty(data)
	}
	if pal.bg == "" {
		if data, err := os.ReadFile(filepath.Join(dir, "kitty.conf")); err == nil {
			pal, _ = parseKitty(data)
		}
	}
	if pal.bg == "" {
		return Theme{}, false
	}

	t := pal.theme()
	t.Name = filepath.Base(dir)
	t.Source = "omarchy"
	return t, true
}

// macosDark reports the macOS system appearance by asking `defaults` — the
// same answer Terminal.app uses to pick its profile. Anything unexpected
// (non-darwin, no defaults binary) is not dark.
func macosDark() bool {
	out, err := exec.Command("defaults", "read", "-g", "AppleInterfaceStyle").Output()
	return err == nil && strings.TrimSpace(string(out)) == "Dark"
}

// colorFGBGDark parses the rxvt convention ("foreground;background" ANSI
// indices): a background of 0-6 or 8 means dark, 7 and 9-15 mean light.
func colorFGBGDark(v string) (bool, bool) {
	parts := strings.Split(strings.TrimSpace(v), ";")
	if len(parts) < 2 {
		return false, false
	}
	n, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return false, false
	}
	return n <= 6 || n == 8, true
}

// palette is the raw color set one theme file yields; empty strings mean
// "the file didn't say", and the theme mapping falls back per slot.
type palette struct {
	bg, fg                                  string
	black, white                            string
	red, green, yellow, blue, magenta, cyan string
	brightBlack, brightWhite                string
}

// theme maps a raw palette onto the §12 grammar: the verdict slots take the
// colors a human already reads as warning/information/accent in their
// terminal, the selection background sits next to the background, and
// missing slots fall back to the generic grammar.
func (p palette) theme() Theme {
	t := Generic(true)
	t.Dirty = p.yellow
	t.Unpushed = p.cyan
	t.Release = p.magenta
	t.Conflict = p.red
	t.Accent = p.cyan
	t.Dim = p.brightBlack
	t.Muted = p.brightBlack
	t.Header = p.brightBlack
	if p.fg != "" {
		if t.Dim == "" {
			t.Dim = p.fg
		}
		if t.Muted == "" {
			t.Muted = p.fg
		}
		if t.Header == "" {
			t.Header = p.fg
		}
	}
	t.SelectedBg = p.black
	if p.brightBlack != "" {
		t.SelectedBg = p.brightBlack
	}
	if luminance(p.bg) >= 0.5 {
		t.Dark = false
		if p.brightWhite != "" {
			t.SelectedBg = p.brightWhite
		}
	}
	// Anything the file did not say keeps the generic verdict color.
	if t.Dirty == "" {
		t.Dirty = "3"
	}
	if t.Unpushed == "" {
		t.Unpushed = "6"
	}
	if t.Release == "" {
		t.Release = "5"
	}
	if t.Conflict == "" {
		t.Conflict = "1"
	}
	if t.Accent == "" {
		t.Accent = "6"
	}
	if t.Dim == "" {
		t.Dim = "8"
	}
	if t.Muted == "" {
		t.Muted = "8"
	}
	if t.Header == "" {
		t.Header = "8"
	}
	if t.SelectedBg == "" {
		if t.Dark {
			t.SelectedBg = "236"
		} else {
			t.SelectedBg = "253"
		}
	}
	return t
}

// parseAlacritty reads the [colors] tables of an alacritty.toml theme.
func parseAlacritty(data []byte) (palette, bool) {
	var doc struct {
		Colors struct {
			Primary struct {
				Background string `toml:"background"`
				Foreground string `toml:"foreground"`
			} `toml:"primary"`
			Normal map[string]string `toml:"normal"`
			Bright map[string]string `toml:"bright"`
		} `toml:"colors"`
	}
	if err := toml.Unmarshal(data, &doc); err != nil {
		return palette{}, false
	}
	p := palette{
		bg:     hex(doc.Colors.Primary.Background),
		fg:     hex(doc.Colors.Primary.Foreground),
		black:  hex(doc.Colors.Normal["black"]),
		red:    hex(doc.Colors.Normal["red"]),
		green:  hex(doc.Colors.Normal["green"]),
		yellow: hex(doc.Colors.Normal["yellow"]),
		blue:   hex(doc.Colors.Normal["blue"]),
		cyan:   hex(doc.Colors.Normal["cyan"]),
	}
	p.magenta = hex(doc.Colors.Normal["magenta"])
	p.white = hex(doc.Colors.Normal["white"])
	p.brightBlack = hex(doc.Colors.Bright["black"])
	p.brightWhite = hex(doc.Colors.Bright["white"])
	if p.bg == "" {
		return palette{}, false
	}
	return p, true
}

// parseKitty reads a kitty.conf theme: `foreground #…`, `background #…`
// and `colorN #…` lines. The include line most theme files carry is
// ignored — Omarchy themes inline their colors.
func parseKitty(data []byte) (palette, bool) {
	var p palette
	kv := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		kv[strings.TrimSpace(key)] = strings.TrimSpace(val)
	}
	p.bg = hex(kv["background"])
	p.fg = hex(kv["foreground"])
	p.black = hex(kv["color0"])
	p.red = hex(kv["color1"])
	p.green = hex(kv["color2"])
	p.yellow = hex(kv["color3"])
	p.blue = hex(kv["color4"])
	p.magenta = hex(kv["color5"])
	p.cyan = hex(kv["color6"])
	p.white = hex(kv["color7"])
	p.brightBlack = hex(kv["color8"])
	p.brightWhite = hex(kv["color15"])
	if p.bg == "" {
		return palette{}, false
	}
	return p, true
}

// hex normalizes one color literal: #rgb grows to #rrggbb, kitty's
// rgb:rr/gg/bb shrinks to #rrggbb, anything else is kept only if it is a
// hex form lipgloss will accept.
func hex(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if strings.HasPrefix(s, "rgb:") {
		parts := strings.Split(strings.TrimPrefix(s, "rgb:"), "/")
		if len(parts) == 3 {
			s = "#" + strings.Join(parts, "")
		}
	}
	if !strings.HasPrefix(s, "#") {
		return ""
	}
	h := strings.TrimPrefix(s, "#")
	switch len(h) {
	case 3:
		return "#" + string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	case 6:
		return s
	}
	return ""
}

// luminance is the perceived brightness of a #rrggbb color, 0 to 1. It
// decides which side of the dark/light line a theme file sits on.
func luminance(hexColor string) float64 {
	h := strings.TrimPrefix(hexColor, "#")
	if len(h) != 6 {
		return 0
	}
	ch := func(i int) float64 {
		v, err := strconv.ParseInt(h[i:i+2], 16, 32)
		if err != nil {
			return 0
		}
		return float64(v) / 255
	}
	r, g, b := ch(0), ch(2), ch(4)
	return 0.2126*r + 0.7152*g + 0.0722*b
}
