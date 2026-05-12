# Spotlight engine integration

This document describes how to wire `SpotlightEngine` (in `spotlight.go`) into
`model.go` and `view.go`. The engine ships as a standalone unit with its own
tests and demo (`cmd/spotlight-demo`); the steps below are everything the host
model needs to do.

## 1. New imports in model.go

No new third-party imports. `SpotlightEngine` already lives in the same
`package main`, so it is directly addressable.

## 2. Fields to add to `model`

Add two fields inside the `// spotlight` block of the `model` struct:

```go
// spotlight
spotlights           []Spotlight
spotlightChat        []ChatMessage
spotlightChatScroll  int
spotlightInput       textarea.Model
spotlightInputActive bool

spotEngine    *SpotlightEngine // new
spotForcePush bool             // new: set true while a forced moment is queued, only used for footer copy
```

`spotForcePush` is optional cosmetic state. The engine itself does not need it.

## 3. Constructing the engine in `newModel()`

In `newModel()`, after the existing seed assignments and before the `return`,
build the engine from a candidate list. The simplest seed reuses the chat
nicks already present in `seedSpotlightChat()` plus the boat-house regulars:

```go
candidates := []Candidate{
    {Nick: "@yamlhater", Bio: "ci whisperer, ships on red"},
    {Nick: "@nullpointer", Bio: "writes pointers, dereferences feelings"},
    {Nick: "@vibe_master", Bio: "the landing page IS the product"},
    {Nick: "@devops_bard", Bio: "the changelog reads like a confession"},
    {Nick: "@junior_dev", Bio: "is this the one where you press a key and it just works"},
}
engine := NewSpotlightEngine(candidates)
```

Then in the returned struct literal:

```go
return model{
    // ...existing fields...
    spotEngine: engine,
}
```

## 4. Starting the engine ticker

`Init()` currently returns `tea.Batch(textarea.Blink, textinput.Blink, tickEvery(), introTickCmd())`.
Add the engine's `Init()` to that batch:

```go
func (m model) Init() tea.Cmd {
    return tea.Batch(
        textarea.Blink,
        textinput.Blink,
        tickEvery(),
        introTickCmd(),
        m.spotEngine.Init(),
    )
}
```

This kicks the round-robin and starts the internal 250ms tick.

## 5. Forwarding messages from `model.Update`

The engine consumes its own `spotlightTickMsg` plus the public event types
(`OptInChosenMsg`, `OptInTimeoutMsg`, `TransitionCompleteMsg`). It also reads
key presses via `HandleKey`. Add forwarding at the top of `model.Update`,
before the existing type switch:

```go
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    // Forward every message to the engine. It only acts on the ones it owns.
    eng, engCmd := m.spotEngine.Update(msg)
    m.spotEngine = eng

    // React to engine events the host cares about.
    switch ev := msg.(type) {
    case SpotlightStartedMsg:
        m.spotlights = append([]Spotlight{ev.Spotlight}, m.spotlights...)
        m.setFlash(ev.Spotlight.Author + " has the floor")
    case SpotlightEndedMsg:
        m.setFlash("spotlight ended: " + ev.Spotlight.Project)
    case OptInTimeoutMsg:
        m.setFlash(string(ev.Candidate.Nick) + " skipped")
    }

    // Existing switch follows.
    switch msg := msg.(type) {
    case tea.WindowSizeMsg:
        // ...unchanged...
    }
    // ...

    return m, tea.Batch(engCmd, /* any cmds produced below */)
}
```

The exact merge with the existing return paths is mechanical: collect both
`engCmd` and whatever the existing path returns into a `tea.Batch`.

## 6. Routing the 1-4 keys during opt-in

The opt-in prompt is global: regardless of which screen the user is looking
at, pressing 1, 2, 3, or 4 while the engine is in `optInPrompt` should be
consumed by the engine and not by the screen handler. In `routeKey`, before
the screen-specific dispatch, add:

```go
func (m model) routeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
    if cmd, consumed := m.spotEngine.HandleKey(msg.String()); consumed {
        return m, cmd
    }
    // ...existing routing...
}
```

This guards against typing "1" into the lobby input bar by accident.
`HandleKey` returns `(nil, false)` whenever the engine is not in
`optInPrompt`, so non-prompt key presses fall through to the existing
dispatch untouched.

If you want the input bar to keep working during opt-in (so the user can type
"1 i" to chat the digit one, for example), guard the call behind
`!m.inputFocused()`. The recommended default is to consume the key
unconditionally because the opt-in window is only 15 seconds.

## 7. Rendering the prompt in `View()`

`SpotlightEngine.RenderPrompt(width)` returns a compact bordered block. It
returns `""` when the engine is not prompting, so it is safe to splice
unconditionally.

In `View()`, after the existing `body` is computed and before joining
header / body / footer, fold the prompt in just above the footer:

```go
body := /* ...existing switch on m.screen... */

prompt := m.spotEngine.RenderPrompt(m.width)
if prompt != "" {
    body = lipgloss.JoinVertical(lipgloss.Left, body, prompt)
}

return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
```

If you want a tighter visual, center the prompt to the same `feedShellWidth`
the rest of the feed uses:

```go
if prompt != "" {
    prompt = lipgloss.PlaceHorizontal(m.width, lipgloss.Center, prompt)
    body = lipgloss.JoinVertical(lipgloss.Left, body, prompt)
}
```

The body height computation (`bodyH := m.height - headerH - footerH`) no
longer accounts for the prompt. Subtract the prompt height from `bodyH`
before passing it to the per-screen renderer:

```go
promptH := lipgloss.Height(prompt)
bodyH := m.height - headerH - footerH - promptH
if bodyH < 6 { bodyH = 6 }
```

## 8. Reacting to engine events (optional surface)

The model can react to any of the engine's public messages. Useful ones:

| Message | Suggested host reaction |
|---|---|
| `SpotlightQueuedMsg` | post a system chat line `--> @nick is up next` |
| `OptInPromptedMsg` | jump the user to `screenSpotlight` if they want, or do nothing |
| `OptInChosenMsg` | post `<nick> chose: <choice label>` |
| `OptInTimeoutMsg` | post `<nick> skipped`, flash message |
| `SpotlightStartedMsg` | prepend the spotlight to `m.spotlights`, switch screen, flash |
| `SpotlightEndedMsg` | flash, optional auto-scroll chat |
| `TransitionStartedMsg` | dim the spotlight surface, lock input |
| `TransitionCompleteMsg` | unlock, ready for the next opt-in |

All of these are pure additions inside the existing `model.Update` switch.
None require new state beyond the engine pointer itself.

## 9. Moderator override hook

The AI moderator stream (separate) calls `m.spotEngine.Force(candidate, t)`
to jump a candidate to the head of the queue with a fixed spotlight type.
The forced candidate is pulled at the next round-robin step; the opt-in
window still runs, but the type is locked.

```go
m.spotEngine.Force(Candidate{Nick: "@guest_speaker", Bio: "..."}, SpotShoutout)
```

This is the only entry point the moderator needs.
