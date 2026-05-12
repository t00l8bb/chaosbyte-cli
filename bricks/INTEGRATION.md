# bricks blitz integration

A 30-second Pong/Breakout hybrid where falling chat lines are the bricks.
Self-contained widget exposed through the bricks subpackage and re-exported
in package main via `bricks.go` at the repo root.

## Layout

```
bricks.go                       root re-export (package main)
bricks/
  blitz.go                      public API: BricksBlitz, msgs, Init/Update/View
  state.go                      simulation: paddle, bars, floor, scoring
  render.go                     cell grid, HUD, end card
  palette.go                    Tokyo Night colour values (mirrors styles.go)
  blitz_test.go                 collision, clamping, end-condition tests
  headless_test.go              full-round walkthrough via Update messages
cmd/bricks-demo/main.go         standalone runner: `go run ./cmd/bricks-demo`
```

The bricks code lives in a subpackage because `cmd/bricks-demo` is its own
`package main` and cannot import the root `package main`. The root-level
`bricks.go` re-exports the types so the rest of the app can refer to
`BricksBlitz`, `BlitzStartedMsg`, `BlitzEndedMsg`, and `NewBricksBlitz`
without an explicit import.

## Public API

```go
type BricksBlitz struct { /* private */ }

func NewBricksBlitz(width, height int, seedLines []string) *BricksBlitz
func (b *BricksBlitz) Init() tea.Cmd
func (b *BricksBlitz) Update(msg tea.Msg) (*BricksBlitz, tea.Cmd)
func (b *BricksBlitz) View() string
func (b *BricksBlitz) Done() bool
func (b *BricksBlitz) Score() (points int, linesHit int)

type BlitzStartedMsg struct{}
type BlitzEndedMsg struct {
    TopScore int
    Lines    int
}
```

`seedLines` must contain entries that normalise to 5..12 chars. Shorter
entries are dropped, longer entries are truncated. An empty input falls
through to a built-in default pool.

## Wiring into the chaosbyte model

### 1. Fields on `model`

Add to the `model` struct in `model.go` (under the `// games` group, near
`bugHunter bugHunterState`):

```go
blitz       *BricksBlitz
blitzActive bool
```

### 2. Trigger on `RequestGameBlitzMsg`

The AI moderator (or any host code) posts `RequestGameBlitzMsg` to start a
round. Handle it in `model.Update` before the screen-routing switch:

```go
case RequestGameBlitzMsg:
    seeds := recentChatSnippets(m, 15) // pull last N short chat lines
    m.blitz = NewBricksBlitz(m.width, m.height, seeds)
    m.blitzActive = true
    m.screen = screenGames
    return m, m.blitz.Init()
```

`recentChatSnippets` lives next to `seedSpotlightChat()` in
`screens_spotlight.go`; it returns a slice of short chat-line bodies.

### 3. Dispatch Update and View

Routing in `model.Update`:

```go
if m.blitzActive && m.blitz != nil {
    var cmd tea.Cmd
    m.blitz, cmd = m.blitz.Update(msg)
    return m, cmd
}
```

Place this *before* the existing screen-switch so the blitz absorbs all
input while it is running.

View routing inside `screens_games.go renderGames`:

```go
if m.blitzActive && m.blitz != nil {
    return m.blitz.View()
}
// existing list / bugHunter rendering...
```

### 4. Consuming `BlitzEndedMsg`

Add a case to `model.Update`:

```go
case BlitzEndedMsg:
    m.blitzActive = false
    m.blitz = nil
    line := fmt.Sprintf("blitz over: %d points, %d lines", msg.TopScore, msg.Lines)
    m.postChat("@chaosbyte_mod", line, ChatSystem)
    return m.toLobby(), nil
```

`postChat` (or whatever the host uses to append to the active channel)
publishes the shoutout. After that we drop the blitz pointer and route the
user back to the lobby.

### 5. Keyboard intercept

While `blitzActive` is true, the blitz owns arrow keys, `h`, `l`, `H`, `L`.
The host's global Esc handling in `routeKey` should be deferred too:
intercept Esc inside the blitz forwarder if you want a mid-round bail-out
to count as an immediate end. Recommended pattern:

```go
if m.blitzActive {
    if k, ok := msg.(tea.KeyMsg); ok && k.String() == "esc" {
        m.blitzActive = false
        m.blitz = nil
        return m.toLobby(), nil
    }
    var cmd tea.Cmd
    m.blitz, cmd = m.blitz.Update(msg)
    return m, cmd
}
```

`Ctrl+C` continues to quit the whole program because the blitz only handles
movement keys; everything else falls through unconsumed.

## What the blitz does not touch

- `data.go` chat/branch seeders
- `view.go` shell rendering
- `main.go` entrypoint
- Any other `screens_*.go`
- The `bugHunter` stub in `screens_games.go`

The integration above lives entirely in `model.go` (5 small edits) and
`screens_games.go` (1 view-routing branch). No changes to the rendering
shell, no new palette values, no new dependencies.
