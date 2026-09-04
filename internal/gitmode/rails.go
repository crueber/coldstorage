package gitmode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// GitTimeout is the default budget for one local git call. A fleet probe
// runs dozens of these per repo; without a hard ceiling a single wedged
// repository (a stale lock, a hung pager) stalls the whole dashboard.
// Thirty seconds matches the spec's local-git budget (§2).
func GitTimeout() time.Duration { return 30 * time.Second }

// childEnv layers the rails on top of the inherited environment. Later
// entries win, so a hostile or lazy user shell config cannot re-enable
// prompting, color, or optional locking. A probe must never take a lock,
// never prompt for credentials, and never emit terminal escapes — the
// output is parsed, not displayed (§2, §7.1).
func childEnv() []string {
	return append(os.Environ(),
		"LC_ALL=C",
		"NO_COLOR=1",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
	)
}

// run executes one child process in dir under the rails: stdin is nil so a
// probe can never block reading a terminal it must not touch, and a
// context timeout kills the child on expiry. A timed-out or failing child
// is an error value carrying stderr — never a panic — so a probe of one
// bad repository degrades that repository alone and the fleet keeps going.
func run(dir string, timeout time.Duration, name string, args ...string) (string, error) {
	if timeout <= 0 {
		timeout = GitTimeout()
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdin = nil
	cmd.Env = childEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("%s %s: timed out after %s: %s",
				name, strings.Join(args, " "), timeout, msg)
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}

// RunGit runs one git invocation in dir under the rails (§2). The args are
// the subcommand onward; the helper prepends --no-optional-locks itself so
// no call site can forget it — polling must never fight the user's editor
// for index.lock (§7.1). Stdout is returned raw; trim at the call site.
func RunGit(dir string, timeout time.Duration, args ...string) (string, error) {
	full := make([]string, 0, len(args)+1)
	full = append(full, "--no-optional-locks")
	full = append(full, args...)
	return run(dir, timeout, "git", full...)
}

// RunGitOK is RunGit for invocations where a non-zero exit is an expected
// answer rather than a failure — describe with no tags in reach, diff
// --quiet reporting a tree difference, branch --contains finding nothing.
// Any error, timeout included, collapses to ("", false): the caller degrades
// to the empty verdict instead of failing the whole probe.
func RunGitOK(dir string, timeout time.Duration, args ...string) (string, bool) {
	out, err := RunGit(dir, timeout, args...)
	if err != nil {
		return "", false
	}
	return out, true
}
