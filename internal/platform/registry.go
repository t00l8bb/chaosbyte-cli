// Package platform is the multi-tenant layer. The same engine renders any
// team's room; the platform decides which team's config and broker a given
// SSH session connects to.
//
// The flagship Vibespace registers itself at the slug "vibespace" with the
// DefaultVibespace config. Other teams register their own config under
// their own slug. An SSH session connects with `ssh teamslug@host`, the
// platform reads the user, resolves to that team's config and broker, and
// the engine renders the room.
//
// Per-team isolation:
//   - Each team has its own RoomConfig (brand, theme, moderator personality)
//   - Each team has its own broker (chat history, member list, spotlight)
//   - Cross-team traffic is impossible by construction
//
// Resolution falls back to the flagship if an unknown slug arrives, so a
// random SSH connection lands in Vibespace rather than being refused.
package platform

import (
	"context"
	"sync"

	"github.com/bchayka/gitstatus/internal/capability"
	"github.com/bchayka/gitstatus/internal/config"
	"github.com/bchayka/gitstatus/internal/dispatch"
	"github.com/bchayka/gitstatus/internal/room"
	"github.com/bchayka/gitstatus/internal/sandbox"
	"github.com/bchayka/gitstatus/internal/store"
	"github.com/bchayka/gitstatus/internal/worktree"
)

// AgentFactoryBuilder constructs a dispatch.AgentFactory bound to a
// specific Orchestrator and worktree controller for one team. The
// platform Registry calls this once per Register so the agent's
// tools can see the right per-session sandbox + workspace.
type AgentFactoryBuilder func(orch *sandbox.Orchestrator, wt worktree.Controller, mountPath string) dispatch.AgentFactory

// Registry holds the active set of teams and routes incoming connections
// to the right one. Safe for concurrent use.
type Registry struct {
	mu           sync.RWMutex
	flagshipSlug string
	configs      map[string]config.RoomConfig
	brokers      map[string]*room.Broker
	orchs        map[string]*sandbox.Orchestrator
	dispatchers  map[string]*dispatch.Dispatcher
	verifier     *capability.Issuer
	store        store.Store
	runtime      sandbox.Runtime
	defaultSpec  sandbox.Spec
	worktrees    worktree.Controller
	baseRepo     string
	branch       string
	mountPath    string
	agentBuilder AgentFactoryBuilder
	ctx          context.Context
	cancel       context.CancelFunc
}

// NewRegistry builds a registry seeded with the flagship Vibespace. The
// flagship's slug is whatever DefaultVibespace returns ("vibespace" today).
// Use Register to add more teams. verifier is optional; when set, every
// per-team broker uses it for capability checks. st is optional; when set,
// every per-team broker persists events through it. runtime is optional;
// when set, every per-team broker also gets a sandbox.Orchestrator and a
// dispatch.Dispatcher wired against it so /run and /sh slash commands
// flow into real sandboxes.
func NewRegistry(verifier *capability.Issuer, st store.Store, runtime sandbox.Runtime, defaultSpec sandbox.Spec) *Registry {
	ctx, cancel := context.WithCancel(context.Background())
	r := &Registry{
		configs:     map[string]config.RoomConfig{},
		brokers:     map[string]*room.Broker{},
		orchs:       map[string]*sandbox.Orchestrator{},
		dispatchers: map[string]*dispatch.Dispatcher{},
		verifier:    verifier,
		store:       st,
		runtime:     runtime,
		defaultSpec: defaultSpec,
		ctx:         ctx,
		cancel:      cancel,
	}
	flagship := config.DefaultVibespace()
	r.flagshipSlug = flagship.Slug
	r.Register(flagship)
	// The built-in Monobyte team shares the engine but unlocks the
	// dispatcher surface (/run, /pull, /agent, etc.). One server,
	// two surfaces, one binary.
	r.Register(config.DefaultMonobyte())
	return r
}

// WithAgentBuilder configures the per-room agent backend. The
// supplied builder is invoked once per Register and the resulting
// AgentFactory is wired into that team's Dispatcher. Without this,
// /agent in chat surfaces "no agent backend configured."
func (r *Registry) WithAgentBuilder(b AgentFactoryBuilder) *Registry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agentBuilder = b
	// Apply to existing dispatchers (only the flagship at this
	// point in practice).
	for slug, d := range r.dispatchers {
		if orch, ok := r.orchs[slug]; ok {
			d.WithAgentFactory(b(orch, r.worktrees, r.mountPath))
		}
	}
	return r
}

// WithWorktrees configures the Registry's per-team Orchestrators to
// provision a worktree per Acquire from baseRepo, mounted at
// mountPath. Must be called before Register if it should affect the
// flagship; subsequent Register calls also pick it up.
func (r *Registry) WithWorktrees(ctrl worktree.Controller, baseRepo, branch, mountPath string) *Registry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.worktrees = ctrl
	r.baseRepo = baseRepo
	r.branch = branch
	r.mountPath = mountPath
	// Apply to any orchestrators that already exist (only the flagship
	// at this point in practice).
	for _, o := range r.orchs {
		o.WithWorktrees(ctrl, baseRepo, branch, mountPath)
	}
	return r
}

// Register adds a team or replaces an existing one. If the team is new, a
// broker is spun up for it. Re-registering an existing team keeps its
// broker alive so connected users do not see their room reset.
func (r *Registry) Register(cfg config.RoomConfig) {
	cfg = config.MergeWithDefaults(cfg)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.configs[cfg.Slug] = cfg
	if _, ok := r.brokers[cfg.Slug]; !ok {
		broker := room.New(cfg.Slug, nil, r.verifier, r.store)
		r.brokers[cfg.Slug] = broker

		// If a sandbox runtime is configured, spin up the dispatch stack
		// for this team. Each team gets its own Orchestrator so a
		// session's sandbox is scoped to the room it joins.
		if r.runtime != nil {
			orch := sandbox.NewOrchestrator(r.runtime, r.defaultSpec)
			if r.worktrees != nil && r.baseRepo != "" {
				orch.WithWorktrees(r.worktrees, r.baseRepo, r.branch, r.mountPath)
			}
			r.orchs[cfg.Slug] = orch
			d := dispatch.New(broker, orch, cfg.Slug)
			if r.agentBuilder != nil {
				d.WithAgentFactory(r.agentBuilder(orch, r.worktrees, r.mountPath))
			}
			if err := d.Start(r.ctx); err == nil {
				r.dispatchers[cfg.Slug] = d
			}
		}
	}
}

// Resolve maps an SSH user to the right team. Unknown slugs land on the
// flagship so an arrival who types the host without thinking still ends
// up somewhere.
func (r *Registry) Resolve(slug string) (config.RoomConfig, *room.Broker) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if cfg, ok := r.configs[slug]; ok {
		return cfg, r.brokers[slug]
	}
	return r.configs[r.flagshipSlug], r.brokers[r.flagshipSlug]
}

// LookupServingByActor returns the active dev-server Serving for the
// given actor id across every registered team. Returns (zero, false)
// if no team has a serving for that id. Used by the HTTP proxy to
// route /u/<actorID>/* to the right sandbox port.
func (r *Registry) LookupServingByActor(actorID string) (room.Serving, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, b := range r.brokers {
		if s, ok := b.LookupServing(actorID); ok {
			return s, true
		}
	}
	return room.Serving{}, false
}

// Teams returns the registered slugs in no particular order. Used by the
// provisioning surface and by admin tools.
func (r *Registry) Teams() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.configs))
	for slug := range r.configs {
		out = append(out, slug)
	}
	return out
}

// Stop tears down every team's broker, dispatcher, and orchestrator.
// Used on orderly server shutdown.
func (r *Registry) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
	for _, d := range r.dispatchers {
		d.Stop()
	}
	for _, o := range r.orchs {
		_ = o.Close(context.Background())
	}
	for _, b := range r.brokers {
		b.Stop()
	}
}
