package discovery

import (
	"os"
	"testing"
)

func TestRealFleetDebug(t *testing.T) {
	home, _ := os.UserHomeDir()
	opts := Options{Roots: []string{"~/Projects", "~/dev/github.com"}, MaxDepth: 4}
	repos, stats, err := Discover(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("home=%s repos=%d stats=%+v", home, len(repos), stats)
	for _, r := range repos {
		t.Logf("  %s (%s/%s)", r.Root, r.Group, r.Name)
	}
	if _, err := os.Stat(home + "/dev/github.com/crueber/drydock/.git"); err != nil {
		t.Logf("drydock .git stat: %v", err)
	}
}
