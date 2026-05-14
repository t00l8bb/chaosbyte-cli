// Package proxy is the HTTP reverse proxy that fronts sandboxed dev
// servers. The vibespace daemon runs one Server on a separate port
// alongside the SSH listener. It accepts requests at
// /u/<actorID>/<path> and proxies to 127.0.0.1:<port> for the actor's
// currently-active serving, as recorded in the broker.
//
// The proxy is transparent to the dev server: it sees a normal HTTP
// request from localhost. WebSocket upgrades and SSE both flow
// through because net/http/httputil.ReverseProxy handles hijacking
// and streaming.
//
// Auth is deferred. v1 trusts that the listening interface is on a
// LAN you control or behind a separate auth layer (e.g. Tailscale
// for the chaosbyte team box). Cookie-based session auth lands in a
// follow-up.
package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/bchayka/gitstatus/internal/events"
	"github.com/bchayka/gitstatus/internal/room"
)

// ServingLookup is the subset of the broker / registry the proxy
// needs to resolve actorID → port. Pulled out as an interface so the
// proxy can be tested without a full broker.
type ServingLookup interface {
	// LookupServingByActor returns the active Serving for the given
	// actor id across every room the registry knows about. Returns
	// (zero, false) if nobody is serving under that id.
	LookupServingByActor(actorID string) (room.Serving, bool)
}

// BrokerLookup returns the room.Broker for a given team slug. The
// SSE events endpoint uses it to subscribe to the team's event
// stream. Implementations: the platform Registry.
type BrokerLookup interface {
	BrokerForSlug(slug string) (*room.Broker, bool)
}

// Server is the HTTP reverse-proxy server.
type Server struct {
	lookup  ServingLookup
	brokers BrokerLookup // optional; nil disables the /api/rooms/* endpoints
	apiKey  string       // optional; when non-empty, /api/* requires it
	addr    string
	srv     *http.Server
}

// New returns a proxy Server bound to addr. The server does not
// start listening until Start is called. brokers is optional; when
// nil the /api/rooms/* endpoints return 503. apiKey is optional;
// when set, /api/rooms/* requires Authorization: Bearer or
// X-API-Key matching it.
func New(addr string, lookup ServingLookup, brokers BrokerLookup, apiKey string) *Server {
	return &Server{
		lookup:  lookup,
		brokers: brokers,
		apiKey:  apiKey,
		addr:    addr,
	}
}

// Start begins serving in a goroutine. Returns immediately. Close
// shuts it down.
func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/u/", s.handleUser)
	mux.HandleFunc("/api/rooms/", s.handleAPIRooms)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	s.srv = &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		_ = s.srv.ListenAndServe()
	}()
	return nil
}

// Close stops the server.
func (s *Server) Close() error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Close()
}

// handleUser routes /u/<actorID>/<path> to the actor's active
// sandbox serving. The path remainder is forwarded to the dev server.
func (s *Server) handleUser(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/u/")
	if rest == "" {
		http.Error(w, "expected /u/<actorID>/<path>", http.StatusBadRequest)
		return
	}
	actorID, suffix, _ := strings.Cut(rest, "/")
	if actorID == "" {
		http.Error(w, "missing actor id", http.StatusBadRequest)
		return
	}

	serving, ok := s.lookup.LookupServingByActor(actorID)
	if !ok {
		http.Error(w, fmt.Sprintf("no active serving for %q", actorID), http.StatusNotFound)
		return
	}

	target, err := url.Parse(fmt.Sprintf("%s://127.0.0.1:%d", scheme(serving.Scheme), serving.Port))
	if err != nil {
		http.Error(w, "bad target", http.StatusInternalServerError)
		return
	}

	// Rewrite the request: strip /u/<actorID>/ from the path so the
	// dev server sees its own path namespace.
	r.URL.Path = "/" + suffix
	r.URL.RawPath = ""

	rp := httputil.NewSingleHostReverseProxy(target)
	// Be defensive: ReverseProxy's default ErrorHandler writes the
	// target host to the response body, which leaks our internal
	// loopback layout. Replace with a safe message.
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}
	rp.ServeHTTP(w, r)
}

// handleAPIRooms routes /api/rooms/<slug>/... requests. v1 supports:
//
//	GET /api/rooms/<slug>/events      Server-Sent Events stream of
//	                                  room events (PresenceJoined,
//	                                  PresenceLeft, SandboxServing,
//	                                  SandboxServingGone, ChatPosted).
//	                                  Each event arrives as one SSE
//	                                  "data: <json>" frame.
//	GET /api/rooms/<slug>/presence    JSON snapshot of current room
//	                                  members.
//	GET /api/rooms/<slug>/servings    JSON snapshot of current servings.
//
// Used by the monobyte-osx native app to maintain a live RoomState
// without needing its own SSH client.
func (s *Server) handleAPIRooms(w http.ResponseWriter, r *http.Request) {
	if s.brokers == nil {
		http.Error(w, "events endpoint disabled", http.StatusServiceUnavailable)
		return
	}
	if !s.checkAPIKey(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="vibespace"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/rooms/")
	slug, subpath, _ := strings.Cut(rest, "/")
	if slug == "" {
		http.Error(w, "expected /api/rooms/<slug>/...", http.StatusBadRequest)
		return
	}
	broker, ok := s.brokers.BrokerForSlug(slug)
	if !ok {
		http.Error(w, fmt.Sprintf("unknown room %q", slug), http.StatusNotFound)
		return
	}
	switch subpath {
	case "events":
		s.streamRoomEvents(w, r, broker)
	case "presence":
		serveJSON(w, broker.Presence())
	case "servings":
		serveJSON(w, broker.Servings())
	default:
		http.Error(w, "unknown subpath", http.StatusNotFound)
	}
}

// checkAPIKey returns true if no apiKey is configured (open mode) or
// the request supplies the matching key via Authorization: Bearer or
// X-API-Key.
func (s *Server) checkAPIKey(r *http.Request) bool {
	if s.apiKey == "" {
		return true
	}
	if got := r.Header.Get("X-API-Key"); got != "" && got == s.apiKey {
		return true
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		if strings.TrimPrefix(auth, "Bearer ") == s.apiKey {
			return true
		}
	}
	return false
}

// streamRoomEvents subscribes to the broker and forwards each event
// as an SSE frame until the client disconnects.
func (s *Server) streamRoomEvents(w http.ResponseWriter, r *http.Request, broker brokerSubscriber) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// First frame: a "hello" with the current presence + servings
	// snapshot so the client does not need to make two requests to
	// bootstrap its state.
	type bootstrap struct {
		Kind     string      `json:"kind"`
		Presence interface{} `json:"presence"`
		Servings interface{} `json:"servings"`
	}
	if snap, ok := broker.(snapshotSource); ok {
		raw, _ := json.Marshal(bootstrap{
			Kind:     "bootstrap",
			Presence: snap.Presence(),
			Servings: snap.Servings(),
		})
		fmt.Fprintf(w, "data: %s\n\n", raw)
		flusher.Flush()
	}

	subID, ch := broker.Subscribe()
	defer broker.Unsubscribe(subID)

	// Periodic keepalive so intermediaries (proxies, browser SSE
	// implementations) do not time the connection out during idle
	// rooms.
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case evt, open := <-ch:
			if !open {
				return
			}
			raw, err := events.Marshal(evt)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.EventKind(), raw)
			flusher.Flush()
		}
	}
}

// brokerSubscriber is the narrowest broker surface the SSE handler
// needs.
type brokerSubscriber interface {
	Subscribe() (room.SubscriberID, <-chan events.Event)
	Unsubscribe(room.SubscriberID)
}

// snapshotSource exposes the presence + servings snapshots so the
// SSE handler can send a bootstrap frame before live events flow.
type snapshotSource interface {
	Presence() []events.Actor
	Servings() []room.Serving
}

// serveJSON writes v as a JSON response. Used by the snapshot
// endpoints.
func serveJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	raw, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(raw)
}

// scheme returns a sane http scheme for the proxied URL. The broker
// records the scheme the user reported in /serve (today always
// "http"); we honor it.
func scheme(s string) string {
	switch s {
	case "https":
		return "https"
	default:
		return "http"
	}
}
