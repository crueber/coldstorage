// Group background colors (§12): a group mapped in [ui] group_colors gets
// its rows painted with that background across the full table width. The
// selection outranks it, and the state grammar stays foreground-only, so
// the verdict colors read the same on every background. Group keys match
// case-insensitively — groups are org logins, and logins differ only in
// case between registrations.
package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// colorNames maps the human color words the config accepts to their ANSI
// indices. Hex values pass through untouched.
var colorNames = map[string]string{
	"black": "0", "red": "1", "green": "2", "yellow": "3",
	"blue": "4", "magenta": "5", "cyan": "6", "white": "7",
	"gray": "8", "grey": "8",
	"bright-black": "8", "bright-red": "9", "bright-green": "10",
	"bright-yellow": "11", "bright-blue": "12", "bright-magenta": "13",
	"bright-cyan": "14", "bright-white": "15",
}

// parseColorValue accepts what a config value may say: a hex color, an ANSI
// index, or one of the colorNames words. Anything else is "" — an unknown
// color is ignored, never a broken render.
func parseColorValue(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return ""
	}
	if strings.HasPrefix(v, "#") {
		if len(v) == 4 || len(v) == 7 {
			hex := v[1:]
			for _, r := range hex {
				if !strings.ContainsRune("0123456789abcdef", r) {
					return ""
				}
			}
			return v
		}
		return ""
	}
	if len(v) <= 3 && isAllDigits(v) {
		return v
	}
	return colorNames[v]
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

// groupColor resolves a row's group to its configured background, "" when
// the group has none. Keys match case-insensitively.
func (m model) groupColor(group string) lipgloss.Color {
	v, ok := m.cfg.UI.GroupColors[strings.ToLower(group)]
	if !ok {
		return ""
	}
	return lipgloss.Color(parseColorValue(v))
}
