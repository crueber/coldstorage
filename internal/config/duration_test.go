package config

import (
	"strings"
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	const day = 24 * time.Hour
	cases := []struct {
		in    string
		want  time.Duration
		error bool
	}{
		// Every unit spelling from the table, with case insensitivity.
		{"250ms", 250 * time.Millisecond, false},
		{"5s", 5 * time.Second, false},
		{"5sec", 5 * time.Second, false},
		{"5secs", 5 * time.Second, false},
		{"5m", 5 * time.Minute, false},
		{"5min", 5 * time.Minute, false},
		{"5mins", 5 * time.Minute, false},
		{"5h", 5 * time.Hour, false},
		{"5hr", 5 * time.Hour, false},
		{"5hrs", 5 * time.Hour, false},
		{"1d", day, false},
		{"1day", day, false},
		{"1days", day, false},
		{"1w", 7 * day, false},
		{"1wk", 7 * day, false},
		{"1week", 7 * day, false},
		{"1weeks", 7 * day, false},
		{"1mo", 30 * day, false},
		{"1month", 30 * day, false},
		{"1months", 30 * day, false},
		{"1y", 365 * day, false},
		{"1yr", 365 * day, false},
		{"1years", 365 * day, false},
		{"5S", 5 * time.Second, false},
		{"1MONTH", 30 * day, false},
		{"  5m  ", 5 * time.Minute, false},
		// Bare numbers are seconds.
		{"45", 45 * time.Second, false},
		{"0", 0, false},
		// Everything else is rejected.
		{"", 0, true},
		{"   ", 0, true},
		{"-5m", 0, true},
		{"1.5h", 0, true},
		{"1h30m", 0, true},
		{"m", 0, true},
		{"5 m", 0, true},
		{"5x", 0, true},
		{"abc", 0, true},
		{"5mo+3d", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseDuration(tc.in)
			if tc.error {
				if err == nil {
					t.Errorf("ParseDuration(%q) = %v, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDuration(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseDuration(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseDurationErrorsNameTheProblem(t *testing.T) {
	for _, in := range []string{"", "-5m", "5x"} {
		_, err := ParseDuration(in)
		if err != nil && strings.TrimSpace(err.Error()) == "" {
			t.Errorf("ParseDuration(%q) error message is empty", in)
		}
	}
}
