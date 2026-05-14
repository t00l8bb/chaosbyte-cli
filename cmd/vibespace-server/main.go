// vibespace-server hosts vibespace over SSH. Each connection spawns its
// own bubbletea program backed by an app.App.
//
// Phase 1 introduces real per-user identity: SSH ed25519 pubkey auth
// gated by an allowlist file. Sessions that pass auth receive a
// Principal carrying their display name, teams, roles, and a session
// biscuit token. The handler refuses sessions whose pubkey is not in
// the allowlist or whose principal is not a member of the requested
// team.
package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/bchayka/gitstatus/internal/agent"
	"github.com/bchayka/gitstatus/internal/agent/tools"
	"github.com/bchayka/gitstatus/internal/app"
	"github.com/bchayka/gitstatus/internal/capability"
	"github.com/bchayka/gitstatus/internal/config"
	"github.com/bchayka/gitstatus/internal/dispatch"
	"github.com/bchayka/gitstatus/internal/events"
	"github.com/bchayka/gitstatus/internal/identity"
	"github.com/bchayka/gitstatus/internal/platform"
	"github.com/bchayka/gitstatus/internal/proxy"
	"github.com/bchayka/gitstatus/internal/sandbox"
	sbhost "github.com/bchayka/gitstatus/internal/sandbox/host"
	"github.com/bchayka/gitstatus/internal/worktree"
	"github.com/bchayka/gitstatus/internal/worktree/plain"
	"github.com/bchayka/gitstatus/internal/store/sqlite"
	"github.com/bchayka/gitstatus/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/log"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/activeterm"
	bm "github.com/charmbracelet/wish/bubbletea"
	"github.com/charmbracelet/wish/logging"
	"github.com/muesli/termenv"
	gossh "golang.org/x/crypto/ssh"
)

func main() {
	host := flag.String("host", "0.0.0.0", "SSH listen host")
	port := flag.String("port", "23234", "SSH listen port")
	keyPath := flag.String("hostkey", ".ssh/vibespace_ed25519", "SSH host key path (auto-generated if missing)")
	configsDir := flag.String("configs", "configs", "directory containing per-team .toml config files")
	keyfile := flag.String("keyfile", "configs/keys/allowlist.toml", "path to the pubkey allowlist")
	biscuitKeyPath := flag.String("biscuit-key", "configs/keys/biscuit-root.key", "path to the biscuit root keypair (auto-generated if missing)")
	dbPath := flag.String("db", "vibespace.db", "path to the SQLite event log")
	sandboxRoot := flag.String("sandbox-root", "var/sandboxes", "directory under which per-session sandbox temp dirs live")
	worktreeRoot := flag.String("worktree-root", "var/worktrees", "directory under which per-session worktrees live")
	baseRepo := flag.String("base-repo", "", "absolute path to a bare git repo; each session gets a worktree clone of it mounted into the sandbox (empty = no worktree provisioning)")
	baseBranch := flag.String("base-branch", "", "branch to check out for new worktrees (empty = HEAD)")
	mountPath := flag.String("mount-path", "/workspace", "path inside the sandbox where the worktree is bind-mounted")
	proxyAddr := flag.String("proxy-addr", "127.0.0.1:23291", "listen address for the HTTP proxy that fronts /serve dev servers (empty = disabled)")
	agentBaseURL := flag.String("agent-base-url", os.Getenv("ANTHROPIC_BASE_URL"), "Anthropic API base URL for the /agent backend (e.g. http://127.0.0.1:8317 for CLIProxyAPI; default api.anthropic.com)")
	agentAPIKey := flag.String("agent-api-key", os.Getenv("ANTHROPIC_API_KEY"), "API key for the /agent backend (CLIProxyAPI accepts its configured api-keys[]; Anthropic direct accepts a real sk-ant-... key)")
	agentModel := flag.String("agent-model", os.Getenv("ANTHROPIC_MODEL"), "model identifier for /agent (default claude-sonnet-4-5)")
	flag.Parse()

	allowlist, err := identity.LoadAllowlist(*keyfile)
	if err != nil {
		log.Error("could not load allowlist", "path", *keyfile, "error", err)
		os.Exit(1)
	}
	log.Info("loaded allowlist", "principals", allowlist.Count(), "path", *keyfile)

	// Ensure the biscuit root key file's directory exists; the issuer
	// generates the keypair if the file is absent.
	if err := os.MkdirAll(filepath.Dir(*biscuitKeyPath), 0o700); err != nil {
		log.Error("could not prepare biscuit key dir", "error", err)
		os.Exit(1)
	}
	issuer, err := capability.NewIssuer(*biscuitKeyPath)
	if err != nil {
		log.Error("could not initialize capability issuer", "error", err)
		os.Exit(1)
	}
	log.Info("capability issuer ready")

	st, err := sqlite.Open(*dbPath)
	if err != nil {
		log.Error("could not open event log", "path", *dbPath, "error", err)
		os.Exit(1)
	}
	defer st.Close()
	log.Info("event log open", "path", *dbPath)

	// Host sandbox runtime: wraps every per-session process under
	// sandbox-exec on Darwin or bwrap on Linux. Constructed once and
	// shared across every team's Orchestrator.
	rt, err := sbhost.New(*sandboxRoot)
	if err != nil {
		log.Error("could not initialize sandbox runtime", "root", *sandboxRoot, "error", err)
		os.Exit(1)
	}
	log.Info("sandbox runtime ready", "kind", rt.Kind(), "root", *sandboxRoot)

	registry := platform.NewRegistry(issuer, st, rt, sandbox.Spec{})

	// Always wire a worktree controller. With --base-repo set, every
	// session's Acquire provisions a clone off it and bind-mounts at
	// --mount-path. Without --base-repo, sessions start with an empty
	// sandbox and the user can /pull <path> or /scratch to populate
	// their workspace. The controller is needed for /pull and /scratch
	// even when no default base repo is configured.
	wtCtrl, err := plain.New(*worktreeRoot)
	if err != nil {
		log.Error("could not initialize worktree controller", "root", *worktreeRoot, "error", err)
		os.Exit(1)
	}
	registry.WithWorktrees(wtCtrl, *baseRepo, *baseBranch, *mountPath)
	if *baseRepo != "" {
		log.Info("worktree provisioning enabled", "base-repo", *baseRepo, "branch", *baseBranch, "mount", *mountPath, "root", *worktreeRoot)
	} else {
		log.Info("worktree controller ready; sessions start empty (users can /pull or /scratch)", "mount", *mountPath, "root", *worktreeRoot)
	}

	// Optional /agent backend: if an API key is configured, build a
	// Claude agent for each session lazily. Tools (read_file,
	// write_file, list_dir, run, diff) are bound to the actor's
	// sandbox + worktree at construction time.
	if *agentAPIKey != "" {
		baseURL := *agentBaseURL
		if baseURL == "" {
			baseURL = agent.DefaultClaudeBaseURL
		}
		log.Info("agent backend enabled",
			"base-url", baseURL,
			"model", coalesce(*agentModel, agent.DefaultClaudeModel),
		)
		registry.WithAgentBuilder(func(orch *sandbox.Orchestrator, _ worktree.Controller, _ string) dispatch.AgentFactory {
			return func(p identity.Principal) (agent.Agent, error) {
				// Acquire the actor's sandbox so the tools can call
				// into it. Acquire is idempotent: subsequent /agent
				// calls re-use the same sandbox.
				sb, err := orch.Acquire(context.Background(), p, sandbox.Spec{})
				if err != nil {
					return nil, err
				}
				workspace := orch.WorkspacePath(p.SessionID)
				toolset := tools.DefaultSet(sb, workspace)
				opts := []agent.ClaudeOption{
					agent.WithClaudeBaseURL(baseURL),
				}
				if *agentModel != "" {
					opts = append(opts, agent.WithClaudeModel(*agentModel))
				}
				return agent.NewClaude(*agentAPIKey, toolset, opts...), nil
			}
		})
	} else {
		log.Info("agent backend disabled (set --agent-api-key or ANTHROPIC_API_KEY to enable /agent)")
	}
	if loaded, err := config.LoadFromDir(*configsDir); err != nil {
		log.Warn("could not read configs directory", "dir", *configsDir, "error", err)
	} else {
		for _, cfg := range loaded {
			registry.Register(cfg)
			log.Info("registered team", "slug", cfg.Slug, "brand", cfg.Brand.Name)
		}
	}
	defer registry.Stop()

	srv, err := wish.NewServer(
		wish.WithAddress(net.JoinHostPort(*host, *port)),
		wish.WithHostKeyPath(*keyPath),
		wish.WithPublicKeyAuth(authFn(allowlist)),
		wish.WithMiddleware(
			// TrueColor floor: bm.MakeRenderer downgrades to the session
			// context's minColorProfile, which defaults to Ascii. Raising
			// the floor keeps the team palette intact for any modern
			// terminal client.
			bm.MiddlewareWithColorProfile(handlerFor(registry, allowlist, issuer), termenv.TrueColor),
			activeterm.Middleware(),
			logging.Middleware(),
		),
	)
	if err != nil {
		log.Error("could not start server", "error", err)
		os.Exit(1)
	}

	// Optional HTTP reverse proxy that fronts /serve dev servers. The
	// proxy listens on its own address; URL shape is
	// /u/<actorID>/<path>. Disabled if --proxy-addr is empty.
	var proxySrv *proxy.Server
	if *proxyAddr != "" {
		proxySrv = proxy.New(*proxyAddr, registry)
		if err := proxySrv.Start(); err != nil {
			log.Error("could not start HTTP proxy", "addr", *proxyAddr, "error", err)
			os.Exit(1)
		}
		log.Info("HTTP proxy ready", "addr", *proxyAddr)
		defer proxySrv.Close()
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	log.Info("starting vibespace SSH server", "host", *host, "port", *port)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
			log.Error("could not start server", "error", err)
			done <- nil
		}
	}()

	<-done
	log.Info("stopping vibespace SSH server")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
		log.Error("could not stop server", "error", err)
	}
}

// authFn returns the Wish PublicKey auth callback. Returns true only
// for pubkeys present in the allowlist. The verified key is recovered
// later in handlerFor via s.PublicKey().
func authFn(allowlist *identity.Allowlist) func(ctx ssh.Context, key ssh.PublicKey) bool {
	return func(_ ssh.Context, key ssh.PublicKey) bool {
		ed, ok := sshKeyToEd25519(key)
		if !ok {
			return false
		}
		_, found := allowlist.Lookup(ed)
		return found
	}
}

// handlerFor returns the Wish bubbletea handler that routes every
// incoming SSH session to the team the user is asking for. The SSH
// user (the part before @ in `ssh user@host`) is the team slug.
//
// At this point the session is past PublicKeyAuth so s.PublicKey() is
// a key we trust. We look it up in the allowlist (cheap, in-memory),
// build a Principal, mint a session biscuit, and pass both into the
// app.
func handlerFor(reg *platform.Registry, allowlist *identity.Allowlist, issuer *capability.Issuer) bm.Handler {
	return func(s ssh.Session) (tea.Model, []tea.ProgramOption) {
		if _, _, active := s.Pty(); !active {
			wish.Fatalln(s, "vibespace requires an interactive terminal")
			return nil, nil
		}
		lipgloss.SetDefaultRenderer(bm.MakeRenderer(s))

		ed, ok := sshKeyToEd25519(s.PublicKey())
		if !ok {
			wish.Fatalln(s, "vibespace requires an ed25519 SSH key")
			return nil, nil
		}
		entry, ok := allowlist.Lookup(ed)
		if !ok {
			// Should never happen, since PublicKeyAuth already gated this.
			wish.Fatalln(s, "your key is not on the allowlist")
			return nil, nil
		}

		principal := allowlist.PrincipalFor(entry, uuid.New())

		slug := s.User()
		if slug == "" {
			slug = "vibespace"
		}
		if !principal.IsMemberOf(slug) {
			wish.Fatalln(s, "not a member of team \""+slug+"\"")
			return nil, nil
		}

		// Mint a session biscuit. Phase 1 does not yet attach it to
		// every event; the broker's verifier check is a no-op when
		// CapabilityProof is nil. Phase 5 changes the lobby's publish
		// path to stamp the proof on every event.
		if _, err := issuer.IssueSession(principal, time.Hour); err != nil {
			log.Warn("could not mint session biscuit", "error", err)
		}

		cfg, broker := reg.Resolve(slug)
		theme.Apply(theme.Palette{
			Bg:       cfg.Theme.Bg,
			Fg:       cfg.Theme.Fg,
			Muted:    cfg.Theme.Muted,
			Accent:   cfg.Theme.Accent,
			Accent2:  cfg.Theme.Accent2,
			BorderHi: cfg.Theme.BorderHi,
			BorderLo: cfg.Theme.BorderLo,
		})

		// Broadcast that this session joined so /who and any future
		// presence panes know the user is here. Mirrored by the
		// goroutine below that fires PresenceLeft on disconnect.
		if broker != nil {
			actor := events.Actor{
				ID:          principal.ID,
				DisplayName: principal.DisplayName,
				Kind:        principal.Kind.String(),
				SessionID:   principal.SessionID,
			}
			_ = broker.PublishEvent(events.NewPresenceJoined(slug, actor))
		}

		// Watch the SSH session for end-of-life so we can publish a
		// PresenceLeft on the broker. The dispatcher subscribes to
		// PresenceLeft to release the sandbox + worktree. Without this
		// hook, abrupt disconnects (Ctrl+C, network drop) would leak
		// per-session state until shutdown.
		go func() {
			<-s.Context().Done()
			if broker == nil {
				return
			}
			actor := events.Actor{
				ID:          principal.ID,
				DisplayName: principal.DisplayName,
				Kind:        principal.Kind.String(),
				SessionID:   principal.SessionID,
			}
			_ = broker.PublishEvent(events.NewPresenceLeft(slug, actor, "disconnect"))
		}()

		return app.New(principal, broker, cfg), []tea.ProgramOption{
			tea.WithAltScreen(),
			tea.WithMouseCellMotion(),
		}
	}
}

// coalesce returns the first non-empty string from its arguments.
func coalesce(strs ...string) string {
	for _, s := range strs {
		if s != "" {
			return s
		}
	}
	return ""
}

// sshKeyToEd25519 extracts the underlying ed25519.PublicKey from a Wish
// ssh.PublicKey. The Wish API exposes only the gliderlabs/ssh interface,
// so we round-trip through ssh.MarshalAuthorizedKey and the
// golang.org/x/crypto/ssh parser to recover the typed key. Returns
// false if the key is not ed25519.
func sshKeyToEd25519(key ssh.PublicKey) (ed25519.PublicKey, bool) {
	if key == nil {
		return nil, false
	}
	authorized := gossh.MarshalAuthorizedKey(key)
	parsed, _, _, _, err := gossh.ParseAuthorizedKey(authorized)
	if err != nil {
		return nil, false
	}
	cp, ok := parsed.(gossh.CryptoPublicKey)
	if !ok {
		return nil, false
	}
	ed, ok := cp.CryptoPublicKey().(ed25519.PublicKey)
	return ed, ok
}
