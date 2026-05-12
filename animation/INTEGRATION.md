# animation engine integration

This package is a tick-driven scene engine for the chaosbyte TUI. It produces
the welcome-spotlight and card-transition moments described in the demo
brief, in pure Go. No Node sidecar yet; see the v2 section at the bottom for
what swaps in when `chenglou/pretext` is wired.

`cmd/animation-demo` is the standalone smoke test. Run it with
`go run ./cmd/animation-demo` to see both scenes play and exit.

## How the engine plugs into the root model

### 1. Model fields

Add to the `model` struct in `model.go`:

```go
import "github.com/bchayka/gitstatus/animation"

type model struct {
    // ...existing fields...

    anim *animation.Player // nil when no animation is playing
}
```

Keep the field unexported and a pointer. A nil `anim` means "no overlay";
that is the steady state. Only one player runs at a time; if a new scene
arrives while one is playing, replace the pointer and call `Init()` again.

### 2. Triggering a scene

Define request messages (one per trigger source is fine):

```go
type RequestWelcomeSpotlightMsg struct{ Nick string }
type RequestCardTransitionMsg struct{ From, To string }
```

Handle them in `Update`. Compute the available grid from the current width
and the height budget the overlay should occupy (mirror what `view.go` does
for popups), then construct the player:

```go
case RequestWelcomeSpotlightMsg:
    w, h := overlayGrid(m.width, m.height)
    p, err := animation.NewPlayer(
        animation.NewWelcomeScene(),
        w, h,
        map[string]any{"nick": msg.Nick},
    )
    if err != nil {
        m.setFlash("animation: " + err.Error())
        return m, nil
    }
    m.anim = p
    return m, p.Init()
```

`p.Init()` returns the `tea.Cmd` that schedules the first frame tick. You
must return that command from `Update`. Skipping it leaves the player
frozen on frame 0.

### 3. Driving ticks

In the top of `Update`, route frame messages to the player before screen
dispatch:

```go
case animation.FrameTickMsg:
    if m.anim == nil {
        return m, nil
    }
    var cmd tea.Cmd
    m.anim, cmd = m.anim.Update(msg)
    if m.anim.Done() {
        m.anim = nil
        // optional: post a follow-up msg so callers know the moment ended
    }
    return m, cmd
```

The player ignores ticks that do not match its own `PlayerID`, so stale
messages from a previous scene are harmless.

### 4. Rendering

In `View()`, when `m.anim != nil` and not done, render the overlay over the
current screen body. Wrap the player output with `lipgloss.Place` so it
centers in the available area:

```go
if m.anim != nil && !m.anim.Done() {
    overlay := lipgloss.Place(
        m.width, bodyH,
        lipgloss.Center, lipgloss.Center,
        m.anim.Render(),
    )
    return lipgloss.JoinVertical(lipgloss.Left, header, overlay, footer)
}
```

The player produces a styled string with embedded newlines sized to the
grid you passed to `NewPlayer`. No padding, no border. Borders, if any,
are the caller's job.

### 5. Knowing when the animation ends

Two equivalent options:

- Poll `m.anim.Done()` in `Update` after every tick (cheapest, what step 3
  above does).
- Post a completion message from `Update` when `Done()` flips true:

  ```go
  if m.anim.Done() {
      m.anim = nil
      return m, func() tea.Msg { return AnimationFinishedMsg{} }
  }
  ```

  Then any caller waiting on the moment can react in its own `Update`
  branch. This is the cleaner pattern when one scene chains into the next
  (welcome → drop user into lobby, or card transition → swap spotlight).

### 6. Adding a third scene later

Implement the `animation.Scene` interface in `animation/scenes.go` or a new
file alongside it:

```go
type MyScene struct { /* private */ }
func NewMyScene() *MyScene { return &MyScene{} }
func (s *MyScene) Init(width, height int, params map[string]any) error { ... }
func (s *MyScene) FrameCount() int { ... }
func (s *MyScene) Frame(i int) FrameData { ... }
func (s *MyScene) DurationMs() int { ... }
```

Use `blankFrame`, `putRune`, `easeInOutCubic`, `lerp`, `centerText`, and
`hashSeed` from the package; they cover most cases. Style keys must be one
of `bright`, `dim`, `accent`, `accent2`, `muted` so the renderer maps them
to the Tokyo Night palette. New keys are a renderer change, not a scene
change.

Wire the new scene into `model.go` with its own request message; no engine
changes needed.

## v2: when we wire real pretext

The `Scene` contract stays the same. What changes:

1. A Node sidecar runs `chenglou/pretext` and exposes a JSON-RPC line
   protocol over stdio (or a Unix socket). The Go process spawns it on
   startup.
2. A new scene implementation, say `PretextScene`, calls into the sidecar
   on `Init` to measure target glyph positions and on `Frame(i)` to fetch
   the interpolated layout. The Go side still owns the cell grid and
   renders it, so all the styling and the renderer code in `render.go`
   stays.
3. The pure-Go scenes in `scenes.go` remain as a fallback when the sidecar
   is unavailable, and as the reference for what "good" looks like.
4. Frame timing stays at 30fps; pretext is fast enough to compute a layout
   per frame, but we will cache when the layout is steady to keep the
   sidecar quiet during hold and settle phases.

The `Player` does not need to know which scene type it drives, so the
runtime in `model.go` and `view.go` is unaffected by the swap.
