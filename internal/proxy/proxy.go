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
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

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

// Server is the HTTP reverse-proxy server.
type Server struct {
	lookup ServingLookup
	addr   string
	srv    *http.Server
}

// New returns a proxy Server bound to addr. The server does not
// start listening until Start is called.
func New(addr string, lookup ServingLookup) *Server {
	return &Server{lookup: lookup, addr: addr}
}

// Start begins serving in a goroutine. Returns immediately. Close
// shuts it down.
func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/u/", s.handleUser)
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
