# drydock — Go port specification

This document is the complete behavioral specification of `drydock`, written so
the application can be reimplemented in Go and verified against the Rust
original, feature for feature and edge case for edge case. It describes
**what the tool does and promises**, not how the Rust code is organized. Where
a behavior exists because of a hard-won lesson, the lesson is stated — those
are the parts most likely to be silently re-broken in a rewrite.

Companion documents: `README.md` (user docs), `FEATURES.md` (high-level
feature map), `AGENTS.md` (repo invariants for agents), `CHANGELOG.md`
(history).

Conventions: **MUST** is a contract the port must keep; **SHOULD** is strong
guidance. Every numbered rule in §13–14 has a named regression test in the
Rust tree; the Go port should reproduce the equivalent test.

---

## 1. Product definition

`drydock` answers, for a fleet of git repositories on disk, in one live
terminal UI and a set of scriptable commands:

- what am I in the middle of (uncommitted work, conflicts, stashes,
  half-finished operations),
- what have I not pushed (across *every* local branch),
- what is worth releasing (commits and changes past the newest tag),
- what have I touched recently,
- which repos are behind their remotes,
- and: keep a set of hosting-owner checkouts (an org or a user's repos)
  cloned and up to date from GitHub, GitLab, or Gitea/Forgejo.

Platforms: macOS and Linux. Distributed via Homebrew, `go install` (port
equivalent), and source. The tool is read-only with respect to the user's
repositories except where org sync explicitly clones and fast-forwards.

---

## 2. External integrations (the rails)

The tool **shells out** to external CLIs; it never links a git library and
never speaks HTTP to a provider. This is a port requirement, not an
accident: output must match what the user sees on the command line,
credentials come from tools the user has already authenticated, and every
provider API drift is absorbed by the CLI instead of by this codebase.

Every child process MUST be spawned with:

- `stdin = null`
- `LC_ALL=C`, `NO_COLOR=1`
- a hard timeout (`remote.timeout` for network-bound calls, ~30s for local
  git calls), killing the child on expiry
- for git: `GIT_OPTIONAL_LOCKS=0`, `GIT_TERMINAL_PROMPT=0`,
  `GIT_ASKPASS=""`, `SSH_ASKPASS=""` — a probe must never take a lock,
  never prompt, never hang

| Integration | Used for |
|---|---|
| `git` | everything local: refs, status, describe, fetch, clone, pull |
| `gh` | GitHub visibility, GitHub org/user repo listing |
| `glab` | GitLab namespace detection, group/user project listing |
| `tea` | Gitea/Forgejo logins, owner listing, repo listing via `tea api` |

Repository visibility (public/private) is **not** a git concept — nothing
under `.git` records it — so it can only come from asking the host. That is
the architectural reason the provider-CLI layer exists at all.

---

## 3. File locations

- Config: platform config dir, `drydock/config.toml` (`~/.config/drydock/`
  on Linux, honoring `$XDG_CONFIG_HOME`; `~/Library/Application
  Support/drydock/` on macOS). An explicit existing `~/.config/drydock`
  wins over the macOS platform default, so synced config dirs keep working.
- Cache: platform cache dir, `drydock/state.json` (honoring
  `$XDG_CACHE_HOME`). Disposable by design: a missing or unreadable cache
  is a non-event.
- Log: platform cache dir, `drydock/drydock.log`. The dashboard logs to the
  file (never stderr — stderr is the UI); CLI commands log to stderr.

---

## 4. Configuration reference (TOML)

Unknown keys are a **hard error** on load, with the file path in the
message — except the dashboard, which falls back to defaults with a
visible warning rather than refusing to open. New fields MUST be added with
serde-style defaults so older files keep loading.

```toml
roots = ["~/Projects"]          # dirs to scan; each immediate subdir = a "group"
max_depth = 4                   # how deep below a root to look
follow_nested_repos = false     # descend into a repo looking for more repos
follow_symlinks = false         # follow symlinked dirs while scanning
exclude = [ "pattern/**", ... ] # glob patterns, matched relative to the root
prune = []                      # extra dir names never to descend into

[refresh]
interval = "5m"                 # backstop sweep interval
watch = true                    # filesystem watching on/off
debounce = "1s"                 # fs event coalescing window

[status]
untracked = "normal"            # normal | all | no  (git status untracked mode)
max_files = 200                 # cap changed files listed per repo
max_age = "1h"                  # working-tree cache age backstop (see §7.3)

[remote]
fetch = false                   # periodic background fetch
interval = "1h"                 # fetch timer
concurrency = 4                 # concurrent fetches / tier-2 scans
timeout = "20s"                 # per-fetch timeout

[visibility]
enabled = false                 # opt-in GitHub public/private checks
interval = "24h"                # recheck cadence
concurrency = 4
timeout = "10s"

[release]
tag_pattern = "*[0-9]*"         # which tags count as releases
max_subjects = 30               # commit subjects kept per repo
read_changelog = true
changelog_files = ["CHANGELOG.md", "CHANGELOG", "changelog.md"]

[ui]
default_filters = []            # e.g. ["dirty", "unpushed"]
default_sort = "activity"
default_since = ""              # e.g. "1w"
editor_command = ["zed", "{path}"]              # O
git_client_command = []         # t — empty = auto-detect (lazygit, gitui)
file_manager_command = []       # o — empty = auto-detect (superfile, nnn, ranger)
terminal_command = ["..."]      # T and ctrl-o

[[orgs]]                        # repeatable; see §11
provider = "github"             # github | gitlab | gitea
host = "github.com"             # instance hostname
owner = "acme"                  # an organization or a single user
path = "~/dev/github.com/acme"  # checkouts live here; see §11.6
login = ""                      # gitea only: which `tea login` to use
protocol = "ssh"                # ssh | https
include_forks = false
include_archived = false
include_subgroups = false       # gitlab groups only
exclude = []                    # repo-name globs to skip
enabled = true
```

Duration strings accept `ms`, `s`/`sec`, `m`/`min`, `h`, `d`, `w`, `mo`, `y`
(bare numbers = seconds). `{path}` in tool commands is replaced by the repo
root. Unknown `[[orgs]]` fields are a hard error. A config whose orgs have
problems (empty owner/host, unknown host without explicit provider,
duplicate registrations, nested paths) must be reported, never silently
repaired.

---

## 5. Repository discovery

- Walk every root; a directory containing `.git` (file or directory —
  worktrees and submodules included) is a checkout.
- **Stop descending the moment a repo is found** — this single rule keeps
  submodules, vendored trees and nested checkouts out of the list.
- Bare repos (a dir with `HEAD` + `refs`, no `.git`) count as repos: they
  are the containers of bare-plus-worktrees layouts.
- Group = first path segment below the root; name = the rest. A repo
  directly in a root has no group.
- Missing roots are skipped; prune names (`node_modules`, `vendor`,
  `target`, `dist`, `build`, … and config `prune`) are never descended.
- Every sweep re-discovers: repos cloned since the last sweep appear
  automatically; repos deleted from disk are dropped from the dashboard.

---

## 6. The per-repo probe

Two tiers, deliberately split, because reading refs is cheap and scanning
working trees is not.

**Tier 1 (every probe):** HEAD (branch/sha/detached), all branches with
tracking and ahead/behind per branch, newest tag by date, nearest tag
reachable from HEAD (`describe --tags --abbrev=0`), commits since that tag
(merge commits excluded — see §7.2), their subjects (capped by
`release.max_subjects`), whether the newest tag is orphaned, stash count
(line count of `logs/refs/stash` — no process), operation in progress
(merge/rebase/cherry-pick/revert/bisect marker files), index mtime, last
fetch time (mtime of `FETCH_HEAD`; absent = never), remote URL, changelog
version (top heading of the configured changelog files), bare flag, shallow
flag.

**Tier 2 (cached):** `git status --porcelain=v2 --branch
--untracked-files=<mode> --ignore-submodules=dirty` → staged/unstaged/
untracked/conflict counts, changed files (capped at `status.max_files`,
each with its porcelain XY code and mtime), truncation flag. Cached
against a key of HEAD sha + index mtime + index size; re-scanned only when
the key moves or `status.max_age` expires.

**The fingerprint gate (this is the scale contract).** Before any probing,
compute a cheap fingerprint of the checkout: mtimes of `.git/HEAD`,
`.git/index`, `.git/packed-refs`, `.git/FETCH_HEAD`, `.git/config`, plus a
bounded recursive max-mtime over `.git/refs` and `.git/logs` (small trees;
loose refs rewritten in place don't move their parent directory mtime, so
the recursion is required). Hash FNV-1a, stable across processes — the
fingerprint is persisted in the cache. If it matches the cached row:
return the cached row, emit its events, spawn **nothing**. Rules:

- `force` (watcher events) bypasses the gate — the event exists because
  something moved.
- An expired `status.max_age` does **not** override the gate (regression:
  expired age used to re-run `git status` across the whole fleet every
  sweep — the "two git processes running forever" incident).
- The fingerprint is unaffected by unstaged working-tree edits — those are
  the watcher's job. This trade is documented and tested.
- First probes and error rows: an errored row re-probes until it succeeds.

**Fetch phase (optional, on request or timer):** fetch repos with a remote,
bounded by `remote.concurrency`, capped by `remote.timeout`. Tags come
along by git's auto-follow — never `--no-tags` (a tag pushed from another
machine must arrive or the release column lies) and local tags are never
pruned (a tag you cut but haven't pushed is exactly what needs-release
exists to find).

**Visibility phase (opt-in):** `gh repo view <slug> --json visibility`,
cached per interval; results carry an explicit status — known public,
known private/internal, checking disabled, check failed, never checked.
Failures are values, never guesses. `--no-tags`-style drift: see §11 for
provider output-shape hazards.

---

## 7. Git semantics that MUST survive the port

Each of these was a reported bug. The port that re-breaks them re-breaks
user trust.

1. Every git invocation passes `--no-optional-locks` (and the terminal
   prompt/askpass killers). Polling must never take `index.lock` or fight
   the user's editor.
2. "Behind" is claimed only from remote-tracking refs. A repo that has
   never fetched shows `?` (unchecked), never `0` (in sync) — zero there
   is a claim nobody checked. The header counts never-fetched repos.
3. The newest tag by date and the nearest reachable tag are tracked
   separately. Git-flow back-merges (`Merge tag 'x.y.z' into develop`) are
   excluded from the since-tag count; a merge commit counts only when it
   is the sole commit AND changes the tree.
4. Tags orphaned by history rewrites are reported (`tags_orphaned`), not
   silently treated as releases.
5. Fetch auto-follows tags and never prunes local tags. `--no-tags` made
   the release column lie.
6. The index mtime is deliberately not treated as activity: any tool
   running `git status` refreshes it, and an editor sitting open on a repo
   would read as recently active. Activity = newest commit, or newest
   working-file mtime for a dirty repo.
7. Bare repos: no working tree to scan, but branches ahead of their remote
   are still reported. A bare repo holding unpushed branches says so.
8. Stashes are read from `logs/refs/stash` line counts — no process. Bare
   repos get a real zero, not the "not scanned" ellipsis.

---

## 8. Per-repo state (the data model)

Every view — table, detail, JSON — renders this one structure:

```
root, group, name, slug (group/name)
state          — coarse, derived with precedence (see below)
release_state  — unreleased | released | needs-release
refs           — head (branch/sha/detached), branches[] {name, tracking,
                 ahead, behind, age, subject}, last commit, stashes,
                 operation, newest tag, described tag, commits_since_tag,
                 since-tag subjects[], tags_orphaned, index_mtime,
                 fetched_at, remote_url, changelog version + untagged flag,
                 is_bare, is_shallow
work           — staged/unstaged/untracked/conflicts counts, changed files[]
                 {path, code, kind, mtime}, truncated flag, head_sha,
                 index mtime/size
visibility     — known public/private/internal | check failed | disabled |
                 never checked, checked_at, error
error          — probe error, if any
refs_probed_at, work_probed_at, work_key, fingerprint
```

State precedence (first match wins): `error` → `conflict` → the in-progress
operation's label (merge/rebase/cherry-pick/revert) → `dirty` → `unpushed`
→ `bare` → `…` (never scanned) → `clean`. A bare repo holding unpushed
branches reports `unpushed` before the bare check.

Release state: `unreleased` (no tags) → `released` (tagged, nothing since)
→ `needs-release` (tagged, commits or changes past the tag).

---

## 9. Commands

Subcommand-less invocation opens the dashboard. Every other command is
`drydock <command> [flags]`, prints, and exits. All list-shaped commands
share the filter/sort/search flags below.

### `drydock` (dashboard)
The live TUI. See §12.

### `drydock list`
The fleet as a table (or JSON). Flags: `--dirty --unpushed --unreleased
--needs-release --released --behind --conflicted --in-progress --detached
--no-remote --no-upstream --stashed --clean --errored --public --private`,
`--filter <name>` (repeatable), `--match-mode any|all`, `--since <dur>`,
`-g/--group <group>`, `-S/--search <text>`, `--sort <key>`, `-r/--reverse`,
`-n/--limit`, `--json`, `--fast` (skip tier 2), `--no-cache`, `--cached`
(instant, cache only), `--fetch` (network phase first), `--paths` (full
paths instead of group/name). Exit code 0 even when filters match nothing.

### `drydock status <path>`
Everything about one repo, human format: state, release, visibility,
activity (+source), head, remote, fetched, tags, changelog verdict, then
the branch table and the since-tag subjects. Walks up from the path to find
the checkout.

### `drydock releasable`
Repos with commits past their tag, newest activity first.
`--min-commits <n>`, `--include-changelog`, `--json`.

### `drydock scan`
Re-discover and re-probe the fleet, refresh the cache.
`--fetch`, `--no-cache`, `--fast`. Prints a one-line summary (repo count,
dirty/unpushed/needs-release counts, timings per phase) and a note when
repos have never been fetched.

### `drydock groups`
Per-group tallies: repo count, dirty, unpushed, needs-release. `--json`.

### `drydock org add|list|remove|sync`
See §11.

### `drydock config init|show|path`
Starter config with every default explained; effective config; file paths.

---

## 10. JSON contract

`--json` on list/status/groups/org commands emits the per-repo state with
**exactly these field names** (scripts depend on them; they are a public
API):

```
path, group, name, slug, state, release_state, visibility,
visibility_error, branch, upstream, ahead, behind, fetched_at,
never_fetched, staged, unstaged, untracked, conflicts, stashes, operation,
last_tag, newest_tag, tag_off_branch, commits_since_tag,
changelog_version, changelog_untagged, remote_url, activity_at,
activity_source ("last commit" | "file edit"), age, work_scanned, error,
flags[]
```

`age` is the compact relative form (`now`, `4m`, `3h`, `6d`, `5w`, `2mo`,
`2y`). Change counts render as `!n` (conflicts), `+n` (staged), `~n`
(unstaged), `?n` (untracked), joined with spaces; `·` when empty.

---

## 11. Organization sync

### 11.1 Registration
`org add <owner> [--provider] [--host] [--path] [--login] [--protocol]
[--include-forks] [--include-archived] [--root]`. Provider and host are
inferred from the tooling: probe `gh`/`glab`/`tea` for authenticated hosts
(a missing binary or no login contributes nothing); exactly one provider →
inferred; several → error listing them; none → error naming the login
commands. `--host` must be one of the provider's authenticated hosts. The
dashboard form behaves identically: it opens instantly in a "probing tool
auth…" state, provider/host rows cycle only through authenticated options,
and the owner is a pick list fetched from the provider's API (free-typing
stays possible for memberships the API doesn't expose).

**The gate: no auth, no add.** A provider whose CLI is missing or not
logged in cannot be registered; the refusal names the login command that
would unlock it.

### 11.2 Provider listings
- GitHub: `gh repo list <owner> --limit 1000 --json name,sshUrl,url,
  isArchived,isFork` — `<owner>` is a user or an org, same invocation.
- GitLab: `glab api namespaces/<owner>` decides user vs group; then
  `glab repo list --user|--group <owner> [--include-subgroups] --output
  json --per-page 100 --page N` until a short page. `GITLAB_HOST` exported
  for self-hosted.
- Gitea/Forgejo: **`tea api /repos/search?owner=<owner>&limit=100&page=N`**
  (with `-l <login>` when set). Do NOT use `tea repos list --output json`:
  tea 0.15.1 answers that with its own compact schema (`owner`/`name`/
  `type`/`ssh` — four fields, no clone URL, no archived/fork), which
  silently gutted sync. `tea api` prints a chatter line before the JSON;
  parse from the first bracket. The wrapper is `{"ok":..,"data":[…]}` — a
  `data: []` wrapper is a real empty answer, not a parse failure.
- **Owner pinning**: the search endpoint's `owner` parameter is advisory on
  some instances (it also answers with repos from reachable orgs), so
  results are pinned client-side to exactly the registered owner. Fixture
  tests must cover the real captured shapes.

### 11.3 The sync engine
- **Strictly serial.** One repo at a time, sorted by name. No concurrency,
  whatever any config says.
- Clone: `git clone <url> <org-path>/<name>` (URL per the configured
  protocol; empty URL is a loud `Error` row — "the provider listing gave no
  clone URL" — never a doomed clone). A failed clone removes the partial
  directory it created; a pre-existing directory is never deleted.
- Update: `git pull --ff-only` and nothing else. Fast-forward = updated;
  "Already up to date" = current; divergence, dirt, detached HEAD and
  missing upstream are **skipped with reasons** — never merged, never
  rewritten, never lost.
- **Nothing is ever deleted.** Repos on disk the owner no longer lists are
  `orphaned` rows (renamed upstream, deleted, archived-and-filtered,
  private-and-unlisted, or excluded).
- Listing failures degrade to **update-only** with a leading `Error` row —
  an empty-or-failed listing must never read as "the whole org is orphans".
  A successful-but-empty listing is trusted.
- After sync: repos changed during flight are re-issued; a full sweep runs
  so fresh clones appear immediately.

### 11.4 Registration wiring
Registering an org adds its checkout parent to `roots` when the org path
isn't already under one (at `org add`, and on sync for hand-edited
configs) — cloned repos land on the dashboard as a group with no manual
config edit. A repo whose resolved path sits under no root and has none to
derive is a loud config error. Duplicate registrations (same
provider+host+owner) and nested paths are validation errors.

### 11.5 Reports
Dry-run prints the plan (CLONE/UPDATE/ORPHAN/SKIP with names). Real runs
print one row per repo — name, action (cloned/updated/current/skipped/
orphaned/error), detail — plus summary counts. `--json` for both. The CLI
prints a note that fresh checkouts appear on the dashboard's next sweep;
the dashboard's own sync triggers the sweep itself.

---

## 12. The dashboard

Layout: a header (fleet counts, spinner, status message), the table, a
footer (key hints that change when shift/ctrl are held — kitty keyboard
protocol, degrading gracefully), and a status line. Columns, left to right,
each toggleable and reorderable via the `C` picker (persisted per session):
GROUP, REPO, BRANCH (sized to content), STATE, RELEASE, CHANGES (`!c +s ~u
?u`), AHEAD, BEHIND, TAG, +TAG (tag age), AGE. Optional: VISIBILITY
(word/marker variants), STASHES, FETCHED.

Modes: table, detail (`⏎`), help (`?`), column picker (`C`), org manager
(`A`), org form, owner picker, search (`/`). `q` quits.

Keymap (table): `j/k/↑/↓` move · `ctrl-d/ctrl-u` half page · `pgup/pgdn` ·
`home/end` · `⏎` detail · `d u r N b c i x e n` filters · `&` any/all ·
`a` clear · `[ ]` group cycle · `0-4` age presets · `s` sort · `S` reverse
· `/` search · `C` columns · `R`/`ctrl-r` rescan · `f F ctrl-f` fetch ·
`o O t T` hand-off · `w` remote in browser · `y` copy path · `A` org
manager · `?` help · `q` quit.

Org manager: `j/k` · `a` add · `e` edit · `x x` remove · `s` sync selected ·
`S` sync all enabled · `?` help · `esc` close. The form opens instantly in
a "probing tool auth…" state (the probe is a network call and MUST NOT run
on the UI thread), rows cycle through authenticated providers/hosts, the
owner row opens a fetched picker, the path auto-fills the resolved default,
and saving is refused — with the reason drawn inside the overlay — until
the probe lands and validation passes.

**The preemption contract (the prime directive).** Terminal events live on
their own channel and are drained first, every iteration; their effects are
painted before background work resumes. Background events are handled in
bounded batches (32) with a UI drain between batches. Idle, the loop sleeps
on both channels with the UI polled first. No key handler may spawn
processes, touch the network, or walk the filesystem synchronously. A
background burst — however large — costs the UI at most one background
event of latency. Repaints: immediately on user input; on the 250ms tick
while a sweep/sync runs; about once a second idle.

Mouse: click selects, wheel moves the selection and scrolls panes.

---

## 13. Filesystem watching

- **Never register a recursive watch on a scan root.** One inotify watch is
  consumed per directory; a 550-repo fleet with `.git/objects` growth
  exhausts the kernel budget (~59k on Linux) around 1000 repos, the
  failures are silent, and the dashboard degrades to periodic sweeps — the
  "two git processes running forever" incident. Instead: per-repo selective
  watch sets — repo root, working dirs (pruned, capped per repo), `.git`
  non-recursive, `.git/refs` + `.git/logs` + `.git/worktrees` recursive —
  ~10–20 watches per repo, immune to object growth.
- Reconcile the watch set after every sweep (repos appear/vanish); the
  reconcile walk runs on its own thread with a sequence guard; registration
  failures warn exactly once and watching continues.
- Watcher events map to owning repos, then pass the storm gates:
  in-flight dedup (one re-probe per repo; changes mid-probe are pending and
  re-issued), a 30s cooldown, a two-permit fleet-wide semaphore, and a
  64-repo batch cap. While an org sync runs, watcher events are dropped
  (the sync's final sweep is authoritative).
- A path is interesting only if it can change an answer: working files,
  and inside `.git` only HEAD, index, packed-refs, FETCH_HEAD, config,
  refs, logs, worktrees, and the operation markers. Everything else
  (`.git/objects` above all) is dropped on sight.

---

## 14. The event loop and UI preemption

See §12 preemption contract and §14 of `AGENTS.md`. The port MUST preserve:
UI events on a dedicated channel, drained before any background handling;
bounded background batches with UI preemption between; background work
(probes, auth checks, cache writes, reconcile walks, watches) on other
threads/goroutines; tick-driven repaints for background state (250ms while
busy, ~1s idle); an immediate repaint for user input. A background burst,
however large, costs the UI at most one background event of latency.

---

## 15. Caching and state

- `state.json` holds the per-repo probe cache (keyed by repo root), plus
  org sync states keyed by (provider, host, owner). Written atomically
  (temp file + rename). Cache writes happen off the UI thread from a
  snapshot.
- New fields are additive with defaults; old files keep loading. Schema
  version exists but adding fields must not bump it.
- The cache exists so the dashboard paints a full table instantly and a
  restart skips unchanged work. It is never authoritative.

---

## 16. Performance requirements (measured contract, 548-repo mixed fleet)

| Operation | Requirement |
|---|---|
| Open dashboard (paint from cache) | instant |
| Full sweep, every repo moved | ≤ 3s |
| Sweep, idle fleet (fingerprint gate) | ≤ 100ms |
| `list --cached` | ≤ 50ms |
| Watcher re-probe, one repo | ms-scale, ≤ 2 concurrent fleet-wide |
| Key-to-screen latency | ≤ 1 background event, always |
| Steady-state CPU, idle fleet | ~0 |
| Scale target | 1,000+ repos, +500 without degradation |

The port MUST reproduce the gates that produce these numbers (§6 fingerprint,
§13 watch discipline, §12 preemption), or it will re-create the incidents:
inotify exhaustion, re-probe pileups, and sweep-storm CPU pegging.

---

## 17. Failure and degradation semantics

Loud, specific, non-fatal. A failed provider listing degrades to update-only
with a leading error row; a failed check is a stored status, never a guess;
an unwritable cache is a log line; a failed watch registration warns once
and degrades to sweeps; an unwritable config fails the operation that
needed it. The dashboard's status line and overlays surface every refusal
with the fix (e.g. the exact login command to run).

---

## 18. Testing requirements for the port

The Rust tree carries 229 tests; the port should reproduce their coverage,
not their implementation:

- Provider listing parsers: fixture strings for every captured output shape
  (including tea 0.15.1's compact schema, the `repos/search` wrapper with
  chatter, bare arrays, and error paths).
- Engine: `file://`-remote end-to-end tests — clone, second-run current,
  behind→updated, diverged→skipped byte-identical, orphans untouched,
  partial-clone cleanup, empty-URL loud errors, listing-failure degradation.
- Probe gate: fingerprint stability/sensitivity, skip-returns-cached,
  force-bypasses, commit-moves-state, and the max_age-vs-gate precedence
  regression.
- Watcher: watch-set composition per repo (prune, cap, bare), diff
  add/remove, reconcile idempotence, real-edit-to-callback end to end,
  interesting-path filter, owning-repo resolution.
- Re-probe gates: in-flight dedup, cooldown deferral, pending re-issue,
  storm filter (sync drop, batch cap).
- Dashboard form: key-sequence driven save mechanics (no toggle flips, one
  org saved), auth-probe landing, refusal messages, auto-root registration.
- Config: round-trip, unknown-key rejection, org validation problems.

---

## 19. Notes for the Go implementation

Deliberately brief — the spec above is the contract; these are the mapping
hints.

- **Keep shelling out to git/gh/glab/tea.** `os/exec` with
  `cmd.Stdin = nil`, env rails, and `context.WithTimeout` per call maps 1:1
  to the required rails. A Go git library would change answers and pull a
  dependency tree; don't.
- **Watching:** `fsnotify` gives per-directory watches; the per-repo
  selective set design (§13) maps directly. Recursion must be hand-rolled
  (walk + add per dir, prune + cap), exactly as specified.
- **TUI:** the dashboard's needs are a cell grid, diff-based repaints,
  mouse, and raw-mode keys — `tcell` fits directly; Bubble Tea works if the
  event loop keeps the §12 priority contract (its model-update loop must
  not repaint per background event).
- **Concurrency:** goroutines + channels map 1:1 to the two-channel
  priority loop and the serial sync engine; `sync.Mutex` (not
  `sync.RWMutex` ceremony) around the watcher registration state;
  `context` for the timeout rails.
- **JSON:** field names in §10 are the API; use explicit struct tags.
- **Config:** a TOML lib with strict unknown-key errors for the strict
  sections and defaults for the rest.

Ship order that worked for the Rust tree and is recommended here: config +
discovery first (pure logic, fully testable), then the git probes behind
file:// fixtures, then the CLI, then the dashboard, then provider sync —
with each phase's tests landing before the next phase starts.
