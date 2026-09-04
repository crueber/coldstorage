package theme

import (
	"os"
	"path/filepath"
	"testing"
)

const alacrittyFixture = `
[colors.primary]
background = "#1a1b26"
foreground = "#c0caf5"

[colors.normal]
black = "#15161e"
red = "#f7768e"
green = "#9ece6a"
yellow = "#e0af68"
blue = "#7aa2f7"
magenta = "#bb9af7"
cyan = "#7dcfff"
white = "#a9b1d6"

[colors.bright]
black = "#414868"
white = "#c0caf5"
`

const kittyFixture = `
# tokyonight
foreground            #c0caf5
background            #1a1b26
color0  #15161e
color1  #f7768e
color2  #9ece6a
color3  #e0af68
color4  #7aa2f7
color5  #bb9af7
color6  #7dcfff
color7  #a9b1d6
color8  #414868
color15 #c0caf5
`

func TestParseAlacritty(t *testing.T) {
	p, ok := parseAlacritty([]byte(alacrittyFixture))
	if !ok {
		t.Fatal("a well-formed theme must parse")
	}
	if p.bg != "#1a1b26" || p.red != "#f7768e" || p.brightBlack != "#414868" {
		t.Fatalf("palette fields lost: %+v", p)
	}
	th := p.theme()
	if !th.Dark {
		t.Error("tokyo-night is a dark theme")
	}
	if th.Dirty != "#e0af68" || th.Conflict != "#f7768e" || th.Unpushed != "#7dcfff" || th.Release != "#bb9af7" {
		t.Errorf("grammar slots must carry the theme accents: %+v", th)
	}
	if th.SelectedBg != "#414868" {
		t.Errorf("selection background = %s, want the bright black that sits next to the bg", th.SelectedBg)
	}
	if th.Source != "omarchy" || th.Name == "" {
		// Source is stamped by omarchyTheme; parseAlacritty only proves the parse.
		t.Log("name/source stamped upstream")
	}
}

func TestParseKitty(t *testing.T) {
	p, ok := parseKitty([]byte(kittyFixture))
	if !ok {
		t.Fatal("a well-formed kitty theme must parse")
	}
	if p.bg != "#1a1b26" || p.yellow != "#e0af68" || p.brightWhite != "#c0caf5" {
		t.Fatalf("palette fields lost: %+v", p)
	}
}

func TestParseAlacrittyRejectsGarbage(t *testing.T) {
	if _, ok := parseAlacritty([]byte("not a theme")); ok {
		t.Error("a file with no background must not parse")
	}
	if _, ok := parseKitty([]byte("")); ok {
		t.Error("an empty file must not parse")
	}
}

func TestHexNormalizes(t *testing.T) {
	cases := map[string]string{
		"#ABC":         "#aabbcc",
		"#1a1b26":      "#1a1b26",
		"rgb:1a/1b/26": "#1a1b26",
		"notacolor":    "",
		"#12345":       "",
	}
	for in, want := range cases {
		if got := hex(in); got != want {
			t.Errorf("hex(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLuminance(t *testing.T) {
	if luminance("#1a1b26") >= 0.5 {
		t.Error("tokyo-night background is dark")
	}
	if luminance("#ffffff") != 1 || luminance("#000000") != 0 {
		t.Error("black and white anchors must saturate")
	}
	if luminance("zzz") != 0 {
		t.Error("garbage is black, never a crash")
	}
}

func TestColorFGBG(t *testing.T) {
	cases := map[string]struct {
		dark bool
		ok   bool
	}{
		"15;0": {true, true},
		"0;15": {false, true},
		"0;8":  {true, true},
		"7":    {false, false},
		"x;y":  {false, false},
	}
	for in, want := range cases {
		dark, ok := colorFGBGDark(in)
		if dark != want.dark || ok != want.ok {
			t.Errorf("colorFGBGDark(%q) = (%v,%v), want (%v,%v)", in, dark, ok, want.dark, want.ok)
		}
	}
}

func TestDetectConfiguredOverride(t *testing.T) {
	light := Detect("light", true)
	if light.Dark || light.Source != "config" {
		t.Error("an explicit light override must win over the terminal query")
	}
	dark := Detect("Dark", false)
	if !dark.Dark || dark.Source != "config" {
		t.Error("an explicit dark override must win, case-insensitively")
	}
}

func TestDetectOmarchyDirectory(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "omarchy", "current", "theme")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alacritty.toml"), []byte(alacrittyFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	th := detectHome("auto", true, home)
	if th.Source != "omarchy" || !th.Dark || th.Name != "theme" {
		t.Fatalf("omarchy detection failed: %+v", th)
	}
}

func TestDetectOmarchyKittyFallback(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "omarchy", "current", "theme")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kitty.conf"), []byte(kittyFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	th := detectHome("auto", true, home)
	if th.Source != "omarchy" || th.Dirty != "#e0af68" {
		t.Fatalf("kitty.conf fallback failed: %+v", th)
	}
}

func TestDetectFallsBackToTerminal(t *testing.T) {
	th := Detect("auto", false)
	if th.Dark || th.Source != "terminal" {
		t.Errorf("no omarchy on the box: the terminal verdict must decide: %+v", th)
	}
	if th.SelectedBg != "253" {
		t.Errorf("light selection must not paint dark grey: %s", th.SelectedBg)
	}
}

func TestGenericPalettes(t *testing.T) {
	d := Generic(true)
	l := Generic(false)
	if d.SelectedBg == l.SelectedBg {
		t.Error("dark and light need different selection backgrounds")
	}
	if d.Dirty != "3" || l.Dirty != "3" {
		t.Error("the ANSI verdict hues carry across both")
	}
}
