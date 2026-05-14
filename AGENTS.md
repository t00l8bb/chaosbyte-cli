# AGENTS.md

## Project

**chaosbyte-cli** is the shared backend for two products by **chaosbyte** (the studio):

- **Vibespace** — the TUI chatroom. Ships first as a community wedge. Lean: chat, themes, presence, AI mod, blitz games. Reached via `ssh vibespace@host`. Flagship runs on vibespace.sh.
- **Monobyte** — the IDE layer on top of Vibespace. Lights up the dispatcher surface: `/run`, `/sh`, `/pull`, `/scratch`, `/serve`, `/unserve`, `/agent`. Reached via `ssh monobyte@host`. Optional native macOS app (separate `monobyte-osx` repo) adds the embedded browser pane + contributor strip for visual co-presence.

One server binary. One broker, one identity layer, one event log. Two surfaces, gated by `Surfaces.Dispatcher` on a per-team `RoomConfig`. Vibespace ships with it off; Monobyte ships with it on; team `.toml` files can opt in.

This repo holds the SSH server (`cmd/vibespace-server`), the single-user dev binary (`cmd/vibespace`), the headless tracer (`cmd/vibespace-trace`), the agent-smoke utility (`cmd/agent-smoke`), and the engines that drive the typographic moments inside the room.

## Build and test

```
go build ./...                       # all three binaries
go test ./...                        # full package suite
go vet ./...
```

Quick local run (no SSH, flagship config baked in):

```
go run ./cmd/vibespace
```

SSH host (Wish daemon, routes the SSH user to its team's room):

```
go run ./cmd/vibespace-server --port 23234
ssh -p 23234 vibespace@localhost
```

Headless tracer that drives `App.Update` with synthetic `tea.Msg` values and prints the final `View()` so multi-step flows can be asserted without a TTY:

```
go run ./cmd/vibespace-trace
```

## Where things live

- `cmd/`: the three entry points (local, server, tracer).
- `internal/platform`: registry that resolves an SSH user to a `(RoomConfig, Broker)` pair and wires the dispatch stack (Orchestrator + Dispatcher) per team.
- `internal/config`: `RoomConfig` and the `.toml` loader.
- `internal/room`: per-room broker that holds the message log in process memory and fans out new posts to subscribed sessions. Tracks presence on `PresenceJoined` / `PresenceLeft`.
- `internal/events`: closed-typed event bus (`ChatPosted`, `PresenceJoined`, `PresenceLeft`, `ModTagged`, `SandboxCommandIssued` / `Output` / `Completed`) with HLC stamping and a JSON envelope.
- `internal/identity`: pubkey-derived `Principal`, allowlist loader.
- `internal/capability`: biscuit issuer; mints per-session tokens. Currently issued but not gated on every event.
- `internal/store`: SQLite-backed event log (`store.Store` interface + `memory` and `sqlite` implementations).
- `internal/sandbox`: per-session sandbox interface, `Orchestrator` that owns the session ↔ sandbox + worktree map.
  - `sandbox/mock`: in-process backend used in tests only.
  - `sandbox/host`: production backend. Wraps every Exec under `sandbox-exec` (Darwin) or `bwrap` (Linux) with a writable-tempdir + mount fence, network denied, and a ulimit-script wrapper for CPU / memory / files / file-size caps.
- `internal/worktree`: per-session git worktree controller. `plain` uses `git worktree add`; `apfs` uses `clonefile` on macOS for O(1) CoW.
- `internal/dispatch`: chat-to-sandbox protocol. Subscribes to the broker, parses `/run`, `/sh`, `/pull`, `/scratch`, executes inside the actor's sandbox, streams output back as typed events. Releases sandboxes on `PresenceLeft`. Panic-safe per-command goroutines.
- `internal/field`: value-noise warped bitmap engine adapted from ertdfgcvb.xyz/js.js. Five intensity tiers, true tier-0 freeze, cascade events as transient foreground overlays with a Decay window.
- `internal/typo`: Pretext-flavored content engine for chat. Layouts hold immutable wrapped text with per-cell coordinates; CellTransforms animate one cell along a PathFn with deterministic per-firing variation; the Choreographer composes Macros into chains with hand-off and reduced-motion support; the Compositor flattens everything into one 2D grid for the lobby to render.
- `internal/mod`: moderator event surface. First live event marks questions with a chat-margin glyph.
- `internal/games`: in-chat blitz round. Paints existing chat with a per-row wave offset on `AnimationState`; there is no parallel game grid.
- `internal/theme`: palettes (registered by name, e.g. `boggy`, `workshop`), logo, shared styles. `/themes` reads `theme.Themes` and `theme.Active`.
- `internal/screens`: `screen.go` is the interface and `Navigate` plumbing; `intro/` is the chaosbyte splash; `lobby/` is the vibespace room; `spotlight/` is the featured-project surface.
- `internal/app`: top-level router, header, footer, `View()`.

## Slash-command grammar (Phase 2)

The lobby has two layers of slash commands.

Lobby-side (handled inline by the screen; never leaves the client):

- `/spotlight`, `/blitz`, `/themes`, `/me`, `/who`, `/clear`, `/help`, `/quit`, `/leave`

Dispatcher-side (recognized by `internal/dispatch`, executed inside the actor's sandbox, output streamed back as `sandbox.command.*` events):

- `/run argv...`   — exec the binary directly
- `/sh one line`   — wraps in `/bin/sh -c "..."`
- `/pull <path>`   — replace the session sandbox with a worktree of a local bare git repo
- `/scratch [desc]` — wipe the workspace and start empty

Output is ANSI-aware chunked, so color codes and UTF-8 codepoints never split mid-sequence.

## Conventions

- Each feature screen implements `screens.Screen` and never imports another feature screen. Navigation goes through `screens.Navigate(target)` messages caught by `internal/app/router.go`. The dependency graph between screens is a star.
- Default to no comments. Only add one when the why is non-obvious, or for a package doc at the top of the file.
- The brand split is firm. `chaosbyte` is the studio and lives on the intro splash and the ASCII logo. `vibespace` is the product the user is inside. Keep them in their lanes.
- Lowercase commit messages with conventional prefixes (`feat`, `fix`, `chore`, `docs`). Short subject; multi-paragraph body when the change needs context.
- Tests live next to the package they cover. Add coverage for non-trivial state (cascade expiry, blitz scoring ladder, theme registry behavior, broker fanout). Skip coverage for pure rendering.
- ANSI color rendering over SSH requires the per-session lipgloss renderer set up in `cmd/vibespace-server`. Don't add code paths that bypass `lipgloss.DefaultRenderer` or fall back to a process-global renderer; SSH sessions would render monochrome.

## Known follow-ups

- Per-session render state. `theme.Apply` and the default-renderer binding are process-global today. Safe for one server, one team. Races when we co-tenant.
- AI moderator. The `internal/mod` event surface is rule-driven today; the spec calls for LLM tuning on spotlight selection and the moment director.
- Capability gate on dispatch verbs. The biscuit issuer mints session tokens but `SandboxCommandIssued` carries no proof, so any room member can `/run`. Phase 3 will require the proof and verify it in the broker.
- PTY winsize propagation. Interactive TTY processes inside `/run --tty` default to 80x24 regardless of the SSH client's actual size.
- Remote `/pull`. Today `/pull <url>` is rejected because the host fence denies network. An out-of-fence fetch path will let users pull from GitHub.
- Repo and module rename. The repo is still `chaosbyte-cli` and the Go module path is still `github.com/bchayka/gitstatus`. After this PR merges, rename the repo and bump the module path to match.
