# Rewriting drydock in Go — why this fork exists

[coldstorage](https://github.com/crueber/coldstorage) is a ground-up Go
reimplementation of [drydock](https://github.com/yetidevworks/drydock), Andy
Miller's Rust dashboard for a fleet of git repositories. The debt is real and
specific: the dashboard's column grammar, the git semantics, the release
logic, the org sync, the failure philosophy — all of it is drydock's, and
`GO-PORT-SPEC.md` in the main repo is drydock's behavioral specification,
adapted for the port and followed feature for feature. If coldstorage is
useful to you, [star drydock](https://github.com/yetidevworks/drydock). This
document only explains why the rewrite happened, not what the tool does.

## What the tool owes its owner

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

drydock already delivers all of this in Rust. The rewrite is about who
maintains it, not what it does.

## Why Go, and why that is really about agents

This codebase is written and maintained primarily by AI agents under human
direction — the port itself was built agent-first, end to end. That changes
what "maintainable" means: the language is not just an implementation
choice, it is the terrain a coding agent has to reason about on every
single edit. Go's terrain is small, flat, and legible; that is the whole
argument.

- **The language fits in the agent's head.** Go is a deliberately small
  language with one obvious way to write most things. There are no lifetime
  annotations to satisfy, no borrow-checker negotiations, no trait-resolution
  puzzles, no macro expansions to mentally unwrap. An agent reading a Go
  function is reasoning about *the function*; an agent reading Rust is
  reasoning about the function *and* the ownership graph behind it. For a
  tool whose core loop is "spawn `git`, parse the answer, draw a row," the
  second reasoning chain buys nothing and costs context on every change.
- **The failure modes are legible.** When Go code is wrong, the compiler or
  `go vet` says so in a sentence that points at the fix. When Rust code
  fights the borrow checker, the error is a lesson in regional polymorphism —
  and an agent without the full surrounding type history will often "fix" it
  by restructuring correct code. Go's worst case is a test failure with a
  diff; Rust's worst case is an agent inventing a new architecture to please
  a lifetime.
- **The feedback loop is short.** Go compiles this entire tree in seconds,
  `gofmt` ends every formatting question before it starts, and
  `go vet` + `go test` are gates an agent can run after every edit without a
  PhD in the toolchain. Fast, boring, deterministic loops are exactly what
  agents are good at.
- **User-land is the tell.** coldstorage spends its life waiting on `git`
  child processes and drawing rows in a terminal. It holds no untrusted
  memory-safety surface, drives no kernel resources beyond inotify, and
  would gain nothing measurable from zero-cost abstractions or the absence
  of a garbage collector — a GC pause is invisible next to a 15ms `git`
  spawn. Rust is a magnificent tool for kernels, parsers of hostile input,
  embedded targets, and latency-critical systems; a personal dashboard is
  none of those. Choosing Rust for a user-land application like this is a
  little like commuting in a rally car: genuinely impressive engineering,
  occasionally spectacular, and mostly paying for capabilities the road
  never asks for. Go is the sensible hatchback — and its trunk opens.
- **It is still a single static binary.** Everything Rust gives a
  distribution story — one file, no runtime, cross-compile — Go gives too,
  with CGO off and a `goreleaser` config (§ README). The user notices no
  difference; the maintainer-agent notices all of it.

None of this is a criticism of drydock, which is excellent software, or of
Rust, which is excellent at the things it is excellent at. It is a judgment
that for a user-land dashboard maintained by agents, Go's constraints are
the helpful kind — guardrails instead of gates — and that the fastest way
for one owner to keep evolving a fleet dashboard with his agent crew was to
rebuild it where his agents already run fast.

## What the port changed

- **TUI only.** The Rust original ships CLI subcommands (`list`, `status`,
  `scan`, …); coldstorage deliberately drops them and delivers everything
  through the dashboard. One interface, one thing to keep honest.
- **A spec instead of a port.** `GO-PORT-SPEC.md` is the contract: the
  behavior is specified section by section, the code follows the spec, and
  when they disagree the spec wins or is amended deliberately, with the
  section cited in the commit message.
- **Agent-first engineering norms.** `AGENTS.md` carries the invariants —
  each with the incident that produced it — the dependency policy, and the
  gates (`gofmt`, `go vet`, `go test -count=1 ./...`). Tests are
  network-free by rule. Releases are automated: a tag publishes the package.

## Upstream

- Original: [yetidevworks/drydock](https://github.com/yetidevworks/drydock)
  (Rust, MIT, © Andy Miller)
- This implementation:
  [crueber/coldstorage](https://github.com/crueber/coldstorage) (Go, MIT)
- The upstream pull request that started all of this was closed by its
  author, so coldstorage is the going-forward implementation of the idea —
  credited, not merged.
