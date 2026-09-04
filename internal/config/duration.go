package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// unitTable maps every accepted duration suffix to its magnitude, longest
// first so "mo" is never read as "m" + "o" and "sec" never as "s" + "ec".
// Months are 30 days and years are 365: these strings back cache ages and
// recheck cadences, not calendars, and the fixed approximation keeps the
// arithmetic reproducible.
var unitTable = []struct {
	suffix string
	value  time.Duration
}{
	{"ms", time.Millisecond},
	{"weeks", 7 * 24 * time.Hour}, {"week", 7 * 24 * time.Hour}, {"wk", 7 * 24 * time.Hour}, {"w", 7 * 24 * time.Hour},
	{"months", 30 * 24 * time.Hour}, {"month", 30 * 24 * time.Hour}, {"mo", 30 * 24 * time.Hour},
	{"years", 365 * 24 * time.Hour}, {"yr", 365 * 24 * time.Hour}, {"y", 365 * 24 * time.Hour},
	{"secs", time.Second}, {"sec", time.Second}, {"s", time.Second},
	{"mins", time.Minute}, {"min", time.Minute}, {"m", time.Minute},
	{"hrs", time.Hour}, {"hr", time.Hour}, {"h", time.Hour},
	{"days", 24 * time.Hour}, {"day", 24 * time.Hour}, {"d", 24 * time.Hour},
}

// ParseDuration accepts the duration grammar of §4: an integer, optionally
// followed by a unit suffix (ms, s/sec/secs, m/min/mins, h/hr/hrs,
// d/day/days, w/wk/week/weeks, mo/month/months, y/yr/years), case
// insensitive, with a bare number meaning seconds. It rejects everything
// else — including negatives, floats, compound strings like "1h30m", and
// the empty string — because a silently-misparsed interval in a config the
// owner hand-writes is worse than a refusal at load time.
func ParseDuration(s string) (time.Duration, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, errors.New(`empty duration string (expected e.g. "5m", "90s", "1h")`)
	}
	lowered := strings.ToLower(trimmed)
	if lowered[0] == '-' {
		return 0, fmt.Errorf("negative duration %q", s)
	}

	i := 0
	for i < len(lowered) && lowered[i] >= '0' && lowered[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("invalid duration %q: expected a number followed by a unit", s)
	}
	n, err := strconv.Atoi(lowered[:i])
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %v", s, err)
	}
	unit := lowered[i:]
	if unit == "" {
		return time.Duration(n) * time.Second, nil
	}
	for _, u := range unitTable {
		if unit == u.suffix {
			return time.Duration(n) * u.value, nil
		}
	}
	return 0, fmt.Errorf("invalid duration %q: unknown unit %q (expected ms, s, m, h, d, w, mo, or y)", s, unit)
}
