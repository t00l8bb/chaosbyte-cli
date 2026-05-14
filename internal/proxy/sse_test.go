package proxy_test

import (
	"bufio"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bchayka/gitstatus/internal/events"
	"github.com/bchayka/gitstatus/internal/proxy"
	"github.com/bchayka/gitstatus/internal/room"
)

// fakeBrokerLookup adapts an in-memory room.Broker to proxy.BrokerLookup.
type fakeBrokerLookup struct {
	slug   string
	broker *room.Broker
}

func (f *fakeBrokerLookup) BrokerForSlug(slug string) (*room.Broker, bool) {
	if slug == f.slug {
		return f.broker, true
	}
	return nil, false
}

func TestSSEStreamsBootstrapThenLiveEvents(t *testing.T) {
	b := room.New("monobyte", nil, nil, nil)
	defer b.Stop()

	// Seed some pre-existing state so the bootstrap frame is non-empty.
	alice := events.Actor{ID: "pk:alice", DisplayName: "@alice", Kind: "human", SessionID: uuid.New()}
	_ = b.PublishEvent(events.NewPresenceJoined("monobyte", alice))

	addr := freeAddr(t)
	p := proxy.New(addr, fakeLookup{}, &fakeBrokerLookup{slug: "monobyte", broker: b}, "")
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	waitForListen(t, addr, 1*time.Second)

	req, _ := http.NewRequest("GET", "http://"+addr+"/api/rooms/monobyte/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content-type = %q", ct)
	}

	// Read the bootstrap frame.
	scanner := bufio.NewScanner(resp.Body)
	bootstrap := readNextDataLine(t, scanner, 2*time.Second)
	if !strings.Contains(bootstrap, `"kind":"bootstrap"`) {
		t.Errorf("first frame should be bootstrap; got %q", bootstrap)
	}
	if !strings.Contains(bootstrap, "alice") {
		t.Errorf("bootstrap should carry pre-existing presence; got %q", bootstrap)
	}

	// Now publish a new event and confirm it flows through.
	bob := events.Actor{ID: "pk:bob", DisplayName: "@bob", Kind: "human", SessionID: uuid.New()}
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = b.PublishEvent(events.NewPresenceJoined("monobyte", bob))
	}()
	live := readNextDataLine(t, scanner, 2*time.Second)
	if !strings.Contains(live, "presence.joined") || !strings.Contains(live, "bob") {
		t.Errorf("expected live PresenceJoined for bob; got %q", live)
	}
	resp.Body.Close()
}

func TestSSERejectsBadAPIKey(t *testing.T) {
	b := room.New("monobyte", nil, nil, nil)
	defer b.Stop()

	addr := freeAddr(t)
	p := proxy.New(addr, fakeLookup{}, &fakeBrokerLookup{slug: "monobyte", broker: b}, "secret-123")
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	waitForListen(t, addr, 1*time.Second)

	// No auth: 401
	resp, _ := http.Get("http://" + addr + "/api/rooms/monobyte/events")
	if resp.StatusCode != 401 {
		t.Errorf("status without key = %d, want 401", resp.StatusCode)
	}
	// Wrong key: 401
	req, _ := http.NewRequest("GET", "http://"+addr+"/api/rooms/monobyte/events", nil)
	req.Header.Set("X-API-Key", "wrong")
	resp2, _ := http.DefaultClient.Do(req)
	if resp2.StatusCode != 401 {
		t.Errorf("status with wrong key = %d, want 401", resp2.StatusCode)
	}
	// Right key via X-API-Key: 200
	req, _ = http.NewRequest("GET", "http://"+addr+"/api/rooms/monobyte/events", nil)
	req.Header.Set("X-API-Key", "secret-123")
	resp3, _ := http.DefaultClient.Do(req)
	if resp3.StatusCode != 200 {
		t.Errorf("status with right key = %d, want 200", resp3.StatusCode)
	}
	resp3.Body.Close()
	// Right key via Bearer: 200
	req, _ = http.NewRequest("GET", "http://"+addr+"/api/rooms/monobyte/events", nil)
	req.Header.Set("Authorization", "Bearer secret-123")
	resp4, _ := http.DefaultClient.Do(req)
	if resp4.StatusCode != 200 {
		t.Errorf("status with bearer = %d, want 200", resp4.StatusCode)
	}
	resp4.Body.Close()
}

func TestSSEUnknownRoomReturns404(t *testing.T) {
	addr := freeAddr(t)
	p := proxy.New(addr, fakeLookup{}, &fakeBrokerLookup{slug: "monobyte", broker: room.New("monobyte", nil, nil, nil)}, "")
	_ = p.Start()
	defer p.Close()
	waitForListen(t, addr, 1*time.Second)
	resp, _ := http.Get("http://" + addr + "/api/rooms/nope/events")
	if resp.StatusCode != 404 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestSnapshotEndpoints(t *testing.T) {
	b := room.New("monobyte", nil, nil, nil)
	defer b.Stop()
	alice := events.Actor{ID: "pk:alice", DisplayName: "@alice", Kind: "human", SessionID: uuid.New()}
	_ = b.PublishEvent(events.NewPresenceJoined("monobyte", alice))
	_ = b.PublishEvent(events.NewSandboxServing("monobyte", alice, alice.SessionID, "next-dev", 3000, "http"))

	addr := freeAddr(t)
	p := proxy.New(addr, fakeLookup{}, &fakeBrokerLookup{slug: "monobyte", broker: b}, "")
	_ = p.Start()
	defer p.Close()
	waitForListen(t, addr, 1*time.Second)

	resp, err := http.Get("http://" + addr + "/api/rooms/monobyte/presence")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("presence status = %d", resp.StatusCode)
	}
	body := readAll(t, resp)
	if !strings.Contains(body, "alice") {
		t.Errorf("presence body missing alice: %q", body)
	}

	resp, err = http.Get("http://" + addr + "/api/rooms/monobyte/servings")
	if err != nil {
		t.Fatal(err)
	}
	body = readAll(t, resp)
	if !strings.Contains(body, "3000") || !strings.Contains(body, "next-dev") {
		t.Errorf("servings body missing serving entries: %q", body)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func readNextDataLine(t *testing.T, sc *bufio.Scanner, max time.Duration) string {
	t.Helper()
	done := make(chan string, 1)
	go func() {
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "data: ") {
				done <- strings.TrimPrefix(line, "data: ")
				return
			}
		}
		done <- ""
	}()
	select {
	case s := <-done:
		return s
	case <-time.After(max):
		t.Fatalf("no SSE data frame within %s", max)
		return ""
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			b.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return b.String()
}
