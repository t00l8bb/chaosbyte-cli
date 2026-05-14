package monobyte

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// shellResultMsg lands in Update when a !cmd shell run finishes.
type shellResultMsg struct {
	cmd    string
	output string
	exit   int
	err    error
}

// runShell executes the supplied command via the user's $SHELL with
// the workspace as the working dir. v1 captures output rather than
// handing the user's terminal over to the child; interactive children
// (vim, htop) are out of scope until v1.5 reimplements this via
// Bubbletea's ReleaseTerminal hook.
func (m *Model) runShell(cmdline string) tea.Cmd {
	return func() tea.Msg {
		cmdline = strings.TrimSpace(cmdline)
		if cmdline == "" {
			return shellResultMsg{cmd: cmdline, err: nil, output: "(empty command)"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		c := exec.CommandContext(ctx, shell, "-c", cmdline)
		c.Dir = m.cfg.Workspace
		c.Env = os.Environ()
		out, err := c.CombinedOutput()
		exit := 0
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
			err = nil
		}
		return shellResultMsg{
			cmd:    cmdline,
			output: strings.TrimRight(string(out), "\n"),
			exit:   exit,
			err:    err,
		}
	}
}

// handleShellResult renders the captured output as scrollback lines.
func (m *Model) handleShellResult(r shellResultMsg) {
	if r.err != nil {
		m.append(line{kind: lineError, text: "shell: " + r.err.Error()})
		return
	}
	if r.output == "" {
		m.append(line{kind: lineSystem, text: "(no output, exit " + itoa(r.exit) + ")"})
		return
	}
	// Split multi-line output into individual lineShell entries so
	// long captures wrap cleanly inside the viewport.
	for _, ln := range strings.Split(r.output, "\n") {
		m.append(line{kind: lineShell, text: ln})
	}
	if r.exit != 0 {
		m.append(line{kind: lineSystem, text: "(exit " + itoa(r.exit) + ")"})
	}
}

// itoa is a tiny no-import int → string helper.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
