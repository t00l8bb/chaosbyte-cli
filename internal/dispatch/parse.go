// Package dispatch bridges chat to sandbox. It subscribes to the room
// broker, recognizes slash-command messages, runs them inside the
// posting actor's sandbox, and streams the output back through the
// same broker as typed events (SandboxCommandIssued, then a series of
// SandboxCommandOutput frames, then SandboxCommandCompleted).
//
// Phase 2 supports two command verbs:
//
//	/run  argv...     run argv[0] with the rest as args
//	/sh   one liner   run `/bin/sh -c "one liner"`
//
// Everything else is left alone: the broker still fans out the
// original ChatPosted as a normal chat line. The dispatcher is purely
// additive.
package dispatch

import (
	"errors"
	"fmt"
	"strings"
)

// Verb is the slash-command name (without the leading '/').
type Verb string

const (
	VerbRun     Verb = "run"
	VerbSh      Verb = "sh"
	VerbPull    Verb = "pull"
	VerbScratch Verb = "scratch"
	VerbServe   Verb = "serve"
	VerbUnserve Verb = "unserve"
)

// ParsedCommand is the structured form of a recognized slash command.
type ParsedCommand struct {
	Verb Verb
	Argv []string
	// Raw is the body after the verb, useful for verbs whose Argv is a
	// single shell line (/sh) or a single path argument (/pull).
	Raw string
}

// ErrNotACommand is returned by Parse when the body does not start
// with a recognized slash-command verb.
var ErrNotACommand = errors.New("dispatch: not a slash command")

// Parse inspects a chat message body and returns the structured
// command if it is one of the recognized verbs.
//
// The body MUST start with '/' and the verb token. Whitespace around
// the verb token and inside the argument list is normalized. Multi-line
// bodies are rejected.
func Parse(body string) (ParsedCommand, error) {
	body = strings.TrimSpace(body)
	if !strings.HasPrefix(body, "/") {
		return ParsedCommand{}, ErrNotACommand
	}
	if strings.ContainsRune(body, '\n') {
		return ParsedCommand{}, fmt.Errorf("dispatch: command body must be one line")
	}

	// Strip leading '/' and split into verb + tail.
	tail := body[1:]
	verbToken, rest, _ := strings.Cut(tail, " ")
	verbToken = strings.ToLower(strings.TrimSpace(verbToken))
	rest = strings.TrimSpace(rest)

	switch Verb(verbToken) {
	case VerbRun:
		argv := splitArgv(rest)
		if len(argv) == 0 {
			return ParsedCommand{}, fmt.Errorf("dispatch: /run requires at least a binary")
		}
		return ParsedCommand{Verb: VerbRun, Argv: argv, Raw: rest}, nil
	case VerbSh:
		if rest == "" {
			return ParsedCommand{}, fmt.Errorf("dispatch: /sh requires a shell line")
		}
		return ParsedCommand{
			Verb: VerbSh,
			Argv: []string{"/bin/sh", "-c", rest},
			Raw:  rest,
		}, nil
	case VerbPull:
		path := strings.TrimSpace(rest)
		if path == "" {
			return ParsedCommand{}, fmt.Errorf("dispatch: /pull requires a path or url")
		}
		return ParsedCommand{
			Verb: VerbPull,
			Argv: []string{path},
			Raw:  path,
		}, nil
	case VerbScratch:
		// /scratch is allowed without an argument; the optional rest is
		// a free-form description we keep for future agent context.
		return ParsedCommand{
			Verb: VerbScratch,
			Argv: []string{rest},
			Raw:  rest,
		}, nil
	case VerbServe:
		// /serve <port> <cmd argv...>. We require an explicit port so
		// the proxy knows where to dial; auto-detect is a follow-up.
		fields := splitArgv(rest)
		if len(fields) < 2 {
			return ParsedCommand{}, fmt.Errorf("dispatch: /serve requires a port and a command, e.g. /serve 3000 npm run dev")
		}
		return ParsedCommand{
			Verb: VerbServe,
			Argv: fields,
			Raw:  rest,
		}, nil
	case VerbUnserve:
		return ParsedCommand{
			Verb: VerbUnserve,
			Argv: nil,
			Raw:  rest,
		}, nil
	default:
		return ParsedCommand{}, ErrNotACommand
	}
}

// splitArgv tokenizes a /run tail. Honors single and double quotes so
// `/run echo "hello world"` becomes ["echo", "hello world"]. Backslash
// escapes a quote inside a quoted token.
//
// Intentionally simple: no command substitution, no env expansion. The
// shell is for /sh.
func splitArgv(s string) []string {
	out := []string{}
	var (
		cur   strings.Builder
		quote rune // 0 = unquoted, '"' or '\''
		esc   bool
	)
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		if esc {
			cur.WriteRune(r)
			esc = false
			continue
		}
		switch {
		case r == '\\' && quote != '\'':
			esc = true
		case quote == 0 && (r == '"' || r == '\''):
			quote = r
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == ' ' || r == '\t'):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}
