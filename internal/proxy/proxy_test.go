package proxy_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bchayka/gitstatus/internal/events"
	"github.com/bchayka/gitstatus/internal/proxy"
	"github.com/bchayka/gitstatus/internal/room"
)

// fakeLookup is a hand-rolled ServingLookup so tests don't need a
// full broker + registry. It maps actorID -> Serving directly.
type fakeLookup map[string]room.Serving

func (f fakeLookup) LookupServingByActor(actorID string) (room.Serving, bool) {
	s, ok := f[actorID]
	return s, ok
}

func TestProxyForwardsToUpstream(t *testing.T) {
	// Spin up a tiny upstream that echoes the request path and a
	// custom header so we can verify the proxy passed them through.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream-Path", r.URL.Path)
		w.Header().Set("X-Upstream-Header", r.Header.Get("X-Test"))
		_, _ = w.Write([]byte("hello from upstream"))
	}))
	defer upstream.Close()

	// Pull the upstream's port out of its URL so the fake lookup can
	// route the actor to it.
	host := strings.TrimPrefix(upstream.URL, "http://")
	_, portStr, _ := net.SplitHostPort(host)
	port, _ := strconv.Atoi(portStr)

	lookup := fakeLookup{
		"pk:daniel": {Actor: events.Actor{ID: "pk:daniel"}, Port: port, Scheme: "http", Label: "test"},
	}
	// Bind to :0 to grab a free port; have to peek the addr via a
	// listener since proxy.Server doesn't expose it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	p := proxy.New(addr, lookup)
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	// Wait for the listener to come up.
	waitForListen(t, addr, 1*time.Second)

	req, _ := http.NewRequest("GET", "http://"+addr+"/u/pk:daniel/hello/world", nil)
	req.Header.Set("X-Test", "yes")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Upstream-Path"); got != "/hello/world" {
		t.Errorf("upstream saw path %q, want /hello/world", got)
	}
	if got := resp.Header.Get("X-Upstream-Header"); got != "yes" {
		t.Errorf("upstream saw X-Test = %q, want yes", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello from upstream" {
		t.Errorf("body = %q", string(body))
	}
}

func TestProxyMissingActorReturns404(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()

	p := proxy.New(addr, fakeLookup{})
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	waitForListen(t, addr, 1*time.Second)

	resp, err := http.Get("http://" + addr + "/u/pk:nobody/anything")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestProxyHealthz(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	p := proxy.New(addr, fakeLookup{})
	_ = p.Start()
	defer p.Close()
	waitForListen(t, addr, 1*time.Second)
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("/healthz status = %d", resp.StatusCode)
	}
}

func waitForListen(t *testing.T, addr string, max time.Duration) {
	t.Helper()
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("proxy never came up on %s", addr)
}
