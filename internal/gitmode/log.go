// The commit log reader (§7, §9 detail): the titles a repo's history is
// made of, newest first, paged. The detail view loads one page at a time —
// a 10,000-commit monorepo must cost the same as a fresh project until the
// owner actually scrolls that deep — so Log speaks skip/limit, exactly one
// child process per page, under the shared rails (§2).
package gitmode

import (
	"strconv"
	"strings"
	"time"
)

// Commit is one history line: when it landed and what it says. The detail
// view shows titles only (§9); if it ever grows bodies or authors, this is
// the type that grows with it.
type Commit struct {
	Time    time.Time
	Subject string
}

// emptyRepoPatterns are the ways `git log` says "there is no history yet":
// an unborn branch on a fresh `init`, or an unborn HEAD on a bare clone
// that has not received a push. That is an answer, not a failure.
var emptyRepoPatterns = []string{
	"does not have any commits yet",
	"bad revision 'head'",
	"unknown revision or path not in the working tree",
	"ambiguous argument 'head'",
}

// Log lists the repo's commit subjects, newest first, paged by skip and
// limit. A repository without commits yields (nil, nil) — the caller
// renders nothing and stops asking. Any other git failure is an error; the
// detail view degrades to the fields it already has.
func Log(root string, limit, skip int) ([]Commit, error) {
	if limit <= 0 {
		limit = 100
	}
	args := []string{
		"log", "--no-color", "--date-order",
		"--pretty=format:%at%x09%s",
		"-n", strconv.Itoa(limit),
	}
	if skip > 0 {
		args = append(args, "--skip", strconv.Itoa(skip))
	}
	out, err := RunGit(root, GitTimeout(), args...)
	if err != nil {
		msg := strings.ToLower(err.Error())
		for _, pat := range emptyRepoPatterns {
			if strings.Contains(msg, pat) {
				return nil, nil
			}
		}
		return nil, err
	}
	return parseLog(out), nil
}

// parseLog reads the %at%x09%s stream: one epoch-seconds TAB subject per
// line. Malformed lines are skipped — a subject may contain anything, but
// the format guarantees the tab before it.
func parseLog(out string) []Commit {
	if strings.TrimSpace(out) == "" {
		return nil
	}
	var commits []Commit
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		epoch, subject, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		secs, err := strconv.ParseInt(epoch, 10, 64)
		if err != nil {
			continue
		}
		commits = append(commits, Commit{
			Time:    time.Unix(secs, 0).UTC(),
			Subject: subject,
		})
	}
	return commits
}
