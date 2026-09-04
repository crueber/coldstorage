# AGENTS.md

Guidance for AI agents (and humans) building **coldstorage** — the Go
reimplementation of `drydock`, a live TUI dashboard for a fleet of git
repositories plus organization sync.

## The contract

`GO-PORT-SPEC.md` in this repo is the complete behavioral specification. It
is the source of truth for what to build and how to verify it. When the
code and the spec disagree, the spec wins — or the spec gets amended
deliberately, with the section referenced in the commit message.

Scope note from the owner: **TUI only.** The Rust original's CLI commands
(`list`, `status`, `scan`, …) are explicitly out of scope; everything they
do is delivered through the dashboard.

**Status:** the port is complete — config, discovery, git probes, watcher,
org sync, the dashboard, and the org manager — through the first UI polish
pass (full-width stable table, the header operation widget, detail commit
history, shell theming). The repo of record is `crueber/coldstorage`; the
upstream PR to `yetidevworks/drydock` was closed by its author, and the
relationship is credited, not merged: never push to the upstream.

## Gates (every change, before commit)

```sh
gofmt -l .            # must print nothing
go vet ./...
go test -count=1 ./...   # -count=1: a cached "ok" prints no result line
```

Go 1.22+ (repo uses 1.27). Tests are **network-free**: tempdirs, `file://`
remotes, and fixture strings only — a test that needs a real provider login
or the network is a broken test.

## Dependency policy

Approved, in use: `github.com/BurntSushi/toml` (config),
`github.com/bmatcuk/doublestar/v4` (exclude globs),
`github.com/fsnotify/fsnotify` (watching), and the Charm stack —
`bubbletea` and `lipgloss` (the TUI; `x/ansi` for ANSI-aware truncation of
styled chrome). Anything else needs a stated reason in its commit. No cgo,
ever.

## Layout

```
cmd/coldstorage/     entrypoint (the TUI)
internal/config/     TOML config, durations, paths, org validation   (spec §3–4)
internal/discovery/  finding repos in the scan roots                (spec §5)
internal/gitmode/    local git probes: refs, status, fingerprint    (spec §6–7)
internal/watcher/    per-repo selective watch sets, reconcile       (spec §13)
internal/orgsync/    provider listings + serial sync engine         (spec §11)
internal/tui/        the dashboard                                  (spec §12)
internal/theme/      theme detection: omarchy, OSC 11, COLORFGBG    (spec §12)
```

## Invariants — these have incidents attached; do not re-break them

Each item cites its spec section. The section explains the failure the rule
prevents; read it before touching the area.

1. **UI preemption** (§12): terminal events live on their own channel and
   are drained first, every iteration; their effects are painted before
   background work resumes; background work runs in bounded batches (32
   events) with a UI drain between batches; no key handler spawns
   processes, touches the network, or walks the filesystem synchronously.
2. **Fingerprint gate** (§6): a repo whose git-state fingerprint matches
   the cached one is skipped without spawning a single process; `force`
   (watcher events) bypasses; an expired max-age does **not** override the
   gate.
3. **Re-probe discipline** (§13): one re-probe per repo at a time (in-flight
   dedup, changes mid-probe are pending and re-issued), a 30s cooldown, a
   two-permit fleet-wide cap, a 64-repo batch cap, and events dropped while
   an org sync runs.
4. **Watching** (§13): never a recursive watch on a scan root — per-repo
   selective watch sets only (root, working dirs pruned and capped,
   `.git` non-recursive, `refs`/`logs`/`worktrees` recursive), reconcile on
   a background thread after each sweep, registration failures warn once.
5. **Sync safety** (§11.3): strictly serial, `pull --ff-only` only, nothing
   ever deleted, orphans reported, empty clone URL = loud error, failed
   listing degrades to update-only, missing org path fails loudly.
6. **Parse defensively** (§11.2): provider CLI output shapes drift by
   version — tea 0.15.1 answers `repos list --output json` with a compact
   schema that has no clone URL (use `tea api /repos/search`), `tea api`
   prints a chatter line before the JSON, and the search endpoint's owner
   parameter is advisory (pin results client-side). Start parsers at the
   first bracket; fixture-test every captured shape.
7. **Config is append-friendly** (§4): new fields need defaults so old
   files load; unknown keys are a hard error with the key named.
8. **Discovery stops at the first repo** (§5): no descending into
   checkouts; bare repos and worktrees count as repos.
9.  **Missing scan roots are surfaced** (§17): a root that does not exist is
    warned about on every sweep — a typo'd root once left a 550-repo fleet
    displaying as an empty dashboard, which reads as "the app is broken,"
    not "the config is."
10. **First sync creates the checkout path** (§11.3): a missing org path is
    created by the first clone, never an error — a brand-new registration
    always points at a directory that does not exist yet. Only an
    unresolvable path (no path configured and no root to derive one from)
    fails loudly.

## Conventions

- Doc comments explain **why**, in prose, in the voice of the spec.
- Git/provider child processes get the rails: nil stdin, `LC_ALL=C`,
  `NO_COLOR=1`, `GIT_TERMINAL_PROMPT=0`, `GIT_ASKPASS=""`,
  `GIT_OPTIONAL_LOCKS=0`, `context.WithTimeout` per call. Never read or
  store provider tokens; ride the user's CLI logins.
- Every user-visible behavior change ships with the test that pins it.
- Visual target for the TUI: the drydock dashboard's layout, colors and
  column grammar (spec §12) — lipgloss styling, not ASCII improvisation.
- Owner runs fleets of 500–1000 repos; anything that scales worse than
  O(changed repos) is a bug.
- **Releases** are automated, never hand-assembled: push a `v*` tag and
  `.github/workflows/release.yml` re-runs the gates and publishes the
  package (six cross-compiled binaries via GoReleaser, `.goreleaser.yaml`
  is the contract). Ordinary pushes get the same gates on CI. Version
  strings come from ldflags stamping `main.version/commit/date` — never
  edit them into the source.
