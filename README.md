# coldstorage

A terminal dashboard for a fleet of git repositories — what's dirty, what's
unpushed, and what's worth releasing, across every checkout you own, live.

```
coldstorage repos 556 dirty 3 unpushed 1 needs-release 125 unfetched 5     ⠙ sync crueber on github.com 41/96
sorted by activity · 556 repos
GROUP   REPO                       BRANCH        STATE     RELEASE        CHANGES  AHEAD BEHIND TAG            +TAG  AGE
crueber walhub                      feat/issues   dirty     unreleased     ~16 ?11  ·     5     ·               ·     now
crueber n8n-workflows               main          clean     needs-release  ·        ·     ·     v1.2.3          2d    2d
decisiv integration-framework-api   main          clean     needs-release  ·        ·     2     v5.34.0         3w    3w
```

**coldstorage is the Go reimplementation of
[drydock](https://github.com/yetidevworks/drydock)**, Andy Miller's Rust
dashboard for a fleet of git repos. drydock's ideas — the dashboard grammar,
the git semantics, the honest reporting — are the foundation this project is
built on; `GO-PORT-SPEC.md` is drydock's behavioral specification, adapted
for the port, and this repository follows it feature for feature. If you use
coldstorage and like it, go star
[drydock](https://github.com/yetidevworks/drydock) — it is the original.

---

## Why

The owner runs 500–1,000+ checkouts — personal repos and large
organizational trees side by side — and asks four things of the tool:

1. **The UI always wins.** Every keypress preempts whatever the background
   is doing. A 550-repo sweep runs in bounded batches with a UI drain
   between them; a background burst costs the interface at most one event of
   latency. No key handler ever spawns a process, touches the network, or
   walks the filesystem synchronously.
2. **Scale is a correctness requirement.** A repo whose git state hasn't
   changed since the last sweep is skipped **without spawning a single
   process** (an mtime/size fingerprint over `.git` internals). Watching is
   per-repo and selective — never a recursive watch on a scan root — so a
   1,000-repo fleet costs tens of thousands of inotify watches, not the
   kernel's whole budget.
3. **Organization sync that never lies.** GitHub, GitLab, and Gitea/Forgejo
   registrations are listed through the provider's own CLI (`gh`, `glab`,
   `tea`), diffed against disk, and synced **strictly serially** with
   `pull --ff-only` only. Nothing is ever deleted: orphans are reported,
   divergent/dirty/detached checkouts are skipped with the reason shown, and
   a failed provider listing degrades to update-only with a loud error row —
   an empty listing must never read as "the whole org is gone."
4. **Honest verdicts.** `?` means nobody checked. A release verdict is
   placed against the repo's own tagging history. An unfetched repo says
   `unfetched` instead of inventing an ahead/behind count.

## Install

Requirements: Go 1.22+, `git`. Optional: [`gh`](https://cli.github.com/),
[`glab`](https://gitlab.com/gitlab-org/cli), or
[`tea`](https://gitea.com/gitea/tea) for org sync — coldstorage rides your
existing CLI logins and never reads or stores provider tokens.

```sh
go install github.com/crueber/coldstorage/cmd/coldstorage@latest
```

Or from a clone:

```sh
git clone git@github.com:crueber/coldstorage.git
cd coldstorage && go install ./cmd/coldstorage
```

Run `coldstorage` — there are no subcommands. First launch scans
`~/Projects` (the default root), and the config below is created at
`~/.config/coldstorage/config.toml` on Linux,
`~/Library/Application Support/coldstorage/config.toml` on macOS.

## Configuration

A minimal config is two lines:

```toml
roots = ["~/Projects", "~/dev/github.com"]
```

A full reference (every key, every default) lives in `GO-PORT-SPEC.md` §4.
The shape:

```toml
roots = ["~/Projects", "~/dev/github.com"]
max_depth = 4                        # how deep discovery descends
exclude = ["**/archived/**"]         # doublestar globs; `x/**` excludes x
prune = ["node_modules", "vendor"]   # directory names never descended

[refresh]
interval = "5m"                      # background resweep cadence
debounce = "1s"                      # watcher quiet window
watch = true                         # filesystem watching (per-repo, selective)

[remote]
fetch = true                         # fetch before computing ahead/behind
concurrency = 4
timeout = "20s"

[release]
tag_pattern = "*[0-9]*"              # which tags count as releases
max_subjects = 30

[ui]
default_filters = []                 # e.g. ["dirty", "unpushed"]
default_sort = "activity"
theme = "auto"                       # auto | dark | light — see Theming

[[orgs]]
provider = "github"                  # github | gitlab | gitea
host = "github.com"
owner = "acme"
path = "~/dev/github.com/acme"       # where checkouts live; created on first sync
# login = "work"                     # gitea/tea only: which login to use
# protocol = "https"                 # ssh (default) | https
# include_forks = false
# include_subgroups = false
# exclude = ["archived-*"]
```

Durations are suffix-rich (`500ms`, `30s`, `5m`, `2h`, `7d`, `2w`, `3mo`,
`1y`; bare numbers are seconds).

## The dashboard

Columns, left to right: **GROUP · REPO · BRANCH · STATE · RELEASE · CHANGES
· AHEAD · BEHIND · TAG · +TAG · AGE**, with VISIBILITY, STASHES, and FETCHED
toggleable. The table always spans the full terminal width — REPO and BRANCH
are flexible and absorb the space — and column widths never reflow while you
scroll or move the cursor.

**Keys** (table): `j/k` move · `ctrl-d/ctrl-u` half page · `pgup/pgdn` ·
`home/end` · `⏎` detail · `d u r N b c i x e n` filters (dirty, unpushed,
released, needs-release, bare, conflicts, needs-attention, excluded,
errors, never-fetched) · `&` any/all · `a` clear all · `[` `]` cycle group ·
`0-4` age presets · `s` cycle sort · `S` reverse · `/` fuzzy search ·
`C` column picker · `A` org manager · `R` rescan · `?` help · `q` quit.

**Detail view** (`⏎`): everything about one repo — state, release placement,
visibility, activity and its source, head, remote, fetch age, tags,
changelog verdict, the full branch table with ahead/behind per upstream, and
the repo's recent **commit history** (titles, paged in as you scroll).
`j/k` scrolls, `esc`/`⏎` returns.

**Org manager** (`A`): register, edit, remove, and sync organization
checkouts. The add form probes `gh`/`glab`/`tea` auth first (off the UI
thread), cycles only through providers you're logged into, and auto-fills
the checkout path. `s` syncs the selected org, `S` syncs every enabled one;
progress streams into the header's operation widget, the summary lands on
the status line, and a full sweep afterward makes fresh clones appear
immediately.

## Theming

Colors follow your shell, not a palette this tool invented. With `theme =
"auto"` (the default) coldstorage checks, in order:

1. Omarchy's active theme — `~/.config/omarchy/current/theme`, read from its
   `alacritty.toml` or `kitty.conf` — for your exact accents
2. the terminal's own background color (works in Alacritty, kitty, iTerm2,
   Terminal.app, and anything that answers OSC 11; on macOS the system
   appearance refines the verdict)
3. `COLORFGBG`, then a dark default

Set `[ui] theme = "dark"|"light"` to pin it. The verdict grammar — yellow
dirty, cyan unpushed, magenta needs-release, red conflicts, dim clean — is
the same in every theme.

## What stays fast

- **Fingerprint gate**: one FNV-1a fingerprint over `.git` mtimes/sizes per
  repo; unchanged checkouts return cached rows with zero process spawns.
- **Selective watches**: ~10–20 inotify watches per repo (root, working
  dirs, `.git` non-recursive; `refs`/`logs` recursive), reconciled on a
  background thread; `.git/objects` is never watched.
- **Bounded batches**: probe results arrive in batches of 32 with a UI drain
  between them; repaints tick at 250ms while work runs, ~1s idle.

## Development

```sh
gofmt -l .            # must print nothing
go vet ./...
go test ./...         # network-free: tempdirs and fixtures only
```

`GO-PORT-SPEC.md` is the behavioral contract — the code follows the spec,
and when they disagree the spec wins or is amended deliberately. `AGENTS.md`
carries the invariants (each with the incident that produced it) and the
dependency policy: tiny, no cgo, every new dependency justified in its
commit.

## Credits

- **[drydock](https://github.com/yetidevworks/drydock)** by
  [Andy Miller](https://github.com/yetidevworks) (`yetidevworks`) — the
  original Rust implementation and the source of every idea here: the
  dashboard, the git semantics, the release logic, the org sync, and the
  spec this port was built against. coldstorage exists because of it.
- Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea) and
  [Lip Gloss](https://github.com/charmbracelet/lipgloss) by the Charm
  team.

## License

MIT — see [LICENSE](LICENSE). drydock is also MIT, © Andy Miller; this
reimplementation carries both copyrights with gratitude.
