package gitmode

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// logRepo makes a repo with one commit per subject, oldest first.
func logRepo(t *testing.T, subjects ...string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	for _, s := range subjects {
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", ".")
		run("commit", "-q", "-m", s)
	}
	return dir
}

func TestLogTitlesAndOrder(t *testing.T) {
	dir := logRepo(t, "first", "second", "third")
	commits, err := Log(dir, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 3 {
		t.Fatalf("got %d commits, want 3", len(commits))
	}
	if commits[0].Subject != "third" || commits[2].Subject != "first" {
		t.Errorf("newest first: %v", commits)
	}
	if commits[0].Time.After(time.Now()) || commits[0].Time.Before(time.Now().Add(-time.Hour)) {
		t.Errorf("commit time must be the commit's own: %v", commits[0].Time)
	}
}

func TestLogPaging(t *testing.T) {
	dir := logRepo(t, "c1", "c2", "c3", "c4", "c5")
	page1, err := Log(dir, 2, 0)
	if err != nil || len(page1) != 2 || page1[0].Subject != "c5" {
		t.Fatalf("page1: %v %v", page1, err)
	}
	page2, err := Log(dir, 2, 2)
	if err != nil || len(page2) != 2 || page2[0].Subject != "c3" {
		t.Fatalf("page2: %v %v", page2, err)
	}
	page3, err := Log(dir, 2, 4)
	if err != nil || len(page3) != 1 || page3[0].Subject != "c1" {
		t.Fatalf("page3: %v %v", page3, err)
	}
	page4, err := Log(dir, 2, 6)
	if err != nil || len(page4) != 0 {
		t.Fatalf("past the end must be empty: %v %v", page4, err)
	}
}

func TestLogEmptyRepo(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init: %v: %s", err, out)
	}
	commits, err := Log(dir, 10, 0)
	if err != nil {
		t.Fatalf("an unborn branch is an answer, not an error: %v", err)
	}
	if commits != nil {
		t.Fatalf("no history: %v", commits)
	}
}

func TestLogBareRepo(t *testing.T) {
	src := logRepo(t, "one", "two")
	bare := t.TempDir()
	cmd := exec.Command("git", "clone", "-q", "--bare", src, bare)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone --bare: %v: %s", err, out)
	}
	commits, err := Log(bare, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 || commits[0].Subject != "two" {
		t.Errorf("bare repos log HEAD: %v", commits)
	}
}

func TestLogMissingRepoIsError(t *testing.T) {
	if _, err := Log(filepath.Join(t.TempDir(), "nope"), 10, 0); err == nil {
		t.Fatal("a nonexistent root must be an error, not silence")
	}
}

func TestParseLogSkipsMalformed(t *testing.T) {
	commits := parseLog("1750000000\treal one\nnot-a-line\n\n99999999\tafter garbage\n")
	if len(commits) != 2 {
		t.Fatalf("got %v", commits)
	}
	if commits[0].Subject != "real one" || commits[1].Subject != "after garbage" {
		t.Errorf("subjects: %v", commits)
	}
}

func TestParseLogEmpty(t *testing.T) {
	if parseLog("") != nil || parseLog("\n") != nil {
		t.Error("empty output is no commits")
	}
}
