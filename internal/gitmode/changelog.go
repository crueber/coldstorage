package gitmode

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// changelogHeading tolerates the shapes real changelogs use for a version
// heading: `# 1.2.3`, `## [1.2.3]`, `# v1.2.3 - date`, `## 1.2.3
// (2026-01-02)`. It captures the version token — optional `v`, optional
// brackets, optional semver suffix — and deliberately refuses headings like
// "Unreleased" or "Changelog", which carry no version and must not fake
// one. Tolerance is the point: a changelog format that drifts must not
// blank the release column.
var changelogHeading = regexp.MustCompile(`^#{1,6}\s*\[?\s*(v?\d+(?:\.\d+)*(?:[-+][0-9A-Za-z.]+)?)\s*\]?`)

// ReadChangelog parses the first of the configured changelog files that
// exists into the tier-1 changelog facts (§6): the top version heading, and
// whether a tag matches that version — a changelog claiming a version no
// tag records is an in-flight release waiting to be cut. A missing or
// unreadable file answers nil rather than an error; the changelog is a
// courtesy column, never a reason for a probe to fail.
func ReadChangelog(root string, files []string, tags []string) *ChangelogInfo {
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		return parseChangelog(string(data), tags)
	}
	return nil
}

// parseChangelog walks the version headings top-down. Tagged refers to the
// top heading only; UnreleasedBlocks counts the distinct version headings
// sitting above the last tagged one — more than one means release blocks
// stacked up behind a cut that never happened.
func parseChangelog(text string, tags []string) *ChangelogInfo {
	type heading struct {
		version string
		tagged  bool
	}
	var heads []heading
	for _, line := range strings.Split(text, "\n") {
		m := changelogHeading.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		heads = append(heads, heading{version: m[1], tagged: tagExists(tags, m[1])})
	}
	if len(heads) == 0 {
		return &ChangelogInfo{}
	}

	lastTagged := -1
	for i, h := range heads {
		if h.tagged {
			lastTagged = i
		}
	}
	blocks := 0
	seen := map[string]bool{}
	bound := lastTagged
	if bound < 0 {
		// Nothing in the file is tagged: every version heading is
		// unreleased by the same reading that counts them one by one.
		bound = len(heads)
	}
	for i := range bound {
		// A tagged heading above an older tagged one is a released
		// version, not an unreleased block — only untagged headings
		// count as work waiting on a cut.
		if heads[i].tagged {
			continue
		}
		if !seen[heads[i].version] {
			seen[heads[i].version] = true
			blocks++
		}
	}

	return &ChangelogInfo{
		Version:          heads[0].version,
		Tagged:           heads[0].tagged,
		UnreleasedBlocks: blocks,
	}
}

// tagExists compares the changelog's version against the repo's tags,
// normalizing the optional `v` prefix on both sides — the changelog saying
// `v1.2.3` while the tag says `1.2.3` is the same release, not a mismatch
// to nag about.
func tagExists(tags []string, version string) bool {
	for _, t := range tags {
		if normVersion(t) == normVersion(version) {
			return true
		}
	}
	return false
}

func normVersion(s string) string {
	return strings.TrimPrefix(strings.TrimSpace(s), "v")
}
