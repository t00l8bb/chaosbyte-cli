package monobyte

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// dispatcherResultMsg lands when a /verb is handled. v1 ships
// minimal /help and /quit; the rest is best-served by talking to
// the agent (which now has every dispatcher verb as a Tool). When
// the full broker chain comes back in v4, real /run /sh /pull etc.
// at the lobby level reappear.
type dispatcherResultMsg struct {
	text string
}

// runDispatcher handles the few power-user verbs the v1 monobyte
// understands directly. Everything else routes through the agent
// (whose tools cover the same surface).
func (m *Model) runDispatcher(text string) tea.Cmd {
	parts := strings.Fields(strings.TrimSpace(text))
	if len(parts) == 0 {
		return nil
	}
	verb := strings.ToLower(parts[0])
	return func() tea.Msg {
		switch verb {
		case "/help":
			return dispatcherResultMsg{text: helpText()}
		case "/quit", "/exit":
			return tea.Quit()
		default:
			return dispatcherResultMsg{
				text: verb + " is not a v1 lobby verb. just say it in plain language — the agent has every verb as a tool.",
			}
		}
	}
}

func (m *Model) handleDispatcherResult(r dispatcherResultMsg) {
	m.append(line{kind: lineSystem, text: r.text})
}

func helpText() string {
	return strings.Join([]string{
		"how to use monobyte:",
		"",
		"  type anything in plain language        claude listens, runs tools, replies",
		"  !cmd ...                                shell exec in your workspace (non-interactive)",
		"  /help                                   this list",
		"  /quit                                   exit monobyte",
		"",
		"the agent has read_file, write_file, list_dir, run, diff, and (soon)",
		"scratch / pull / serve / open_browser as tools. just describe what you",
		"want — \"serve this on 3000\", \"read the readme\", \"refactor parse.go\" —",
		"and claude will reach for the right tool.",
	}, "\n")
}
