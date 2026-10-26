package hook

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestClientPushesEntriesGroupedByStream(t *testing.T) {
	s := newLokiServer(t, 204)
	cfg := testConfig(s.srv.URL)
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := c.Send(time.Now().UnixNano(), fmt.Sprintf("line-%d", i), nil); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	payload := s.decodedPayload(t)
	if len(payload.Streams) != 1 {
		t.Fatalf("want 1 stream, got %d", len(payload.Streams))
	}
	st := payload.Streams[0]
	if st.Stream["app"] != "test" {
		t.Fatalf("want app=test, got %q", st.Stream["app"])
	}
	if len(st.Values) != 3 {
		t.Fatalf("want 3 values, got %d", len(st.Values))
	}
	if st.Values[0][1] != "line-0" || st.Values[2][1] != "line-2" {
		t.Fatalf("unexpected values order: %v", st.Values)
	}
	sent, dropped := c.Stats()
	if sent != 3 || dropped != 0 {
		t.Fatalf("want sent=3 dropped=0, got sent=%d dropped=%d", sent, dropped)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestClientSeparatesStreamsWithDifferentDynamicLabels(t *testing.T) {
	s := newLokiServer(t, 204)
	c, err := NewClient(testConfig(s.srv.URL))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if err := c.Send(time.Now().UnixNano(), "a", map[string]string{"pod": "p1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := c.Send(time.Now().UnixNano(), "b", map[string]string{"pod": "p2"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	payload := s.decodedPayload(t)
	if len(payload.Streams) != 2 {
		t.Fatalf("want 2 streams, got %d", len(payload.Streams))
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestClientRetriesOnServerError(t *testing.T) {
	s := newLokiServer(t, 204)
	s.mu.Lock()
	s.failsLeft = 1
	s.mu.Unlock()

	cfg := testConfig(s.srv.URL)
	cfg.Retries = 2
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if err := c.Send(time.Now().UnixNano(), "retry-me", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if s.requestCount() != 2 {
		t.Fatalf("want 2 requests (1 failed, 1 ok), got %d", s.requestCount())
	}
	sent, dropped := c.Stats()
	if sent != 1 || dropped != 0 {
		t.Fatalf("want sent=1 dropped=0, got sent=%d dropped=%d", sent, dropped)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestClientDoesNotRetryOn4xx(t *testing.T) {
	s := newLokiServer(t, http.StatusBadRequest)
	cfg := testConfig(s.srv.URL)
	cfg.Retries = 3
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if err := c.Send(time.Now().UnixNano(), "gone", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if s.requestCount() != 1 {
		t.Fatalf("want exactly 1 request, got %d", s.requestCount())
	}
	sent, dropped := c.Stats()
	if sent != 0 || dropped != 1 {
		t.Fatalf("want sent=0 dropped=1, got sent=%d dropped=%d", sent, dropped)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestClientAuthHeaders(t *testing.T) {
	s := newLokiServer(t, 204)
	cfg := testConfig(s.srv.URL)
	cfg.BasicAuth = &BasicAuth{Username: "user", Password: "pass"}
	cfg.OrgID = "tenant-1"
	cfg.Headers = map[string]string{"X-Custom": "yes"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if err := c.Send(time.Now().UnixNano(), "auth", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	req := s.lastRequest()
	want := map[string]string{
		"Authorization": "Basic dXNlcjpwYXNz",
		"X-Scope-Orgid": "tenant-1",
		"X-Custom":      "yes",
	}
	for k, v := range want {
		if got := req.headers.Get(k); got != v {
			t.Fatalf("header %s: want %q, got %q", k, v, got)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestClientBearerTokenAlone(t *testing.T) {
	s := newLokiServer(t, 204)
	cfg := testConfig(s.srv.URL)
	cfg.BearerToken = "tok"
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if err := c.Send(time.Now().UnixNano(), "bearer", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := s.lastRequest().headers.Get("Authorization"); got != "Bearer tok" {
		t.Fatalf("want Bearer tok, got %q", got)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestClientDropNewestPolicy(t *testing.T) {
	s := newLokiServer(t, 204)
	s.gateBlocks()
	defer s.release()
	c, err := NewClient(gatedConfig(s.srv.URL))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = c.Close() }()

	if err := c.Send(1, "first", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	s.waitEntered()
	if err := c.Send(2, "second", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	err = c.Send(3, "third", nil)
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("want ErrQueueFull, got %v", err)
	}
	s.release()
	waitUntil(t, func() bool {
		sent, _ := c.Stats()
		return sent == 2
	})
	sent, dropped := c.Stats()
	if sent != 2 || dropped != 1 {
		t.Fatalf("want sent=2 dropped=1, got sent=%d dropped=%d", sent, dropped)
	}
}

func TestClientDropOldestPolicy(t *testing.T) {
	s := newLokiServer(t, 204)
	s.gateBlocks()
	defer s.release()
	cfg := gatedConfig(s.srv.URL)
	cfg.Overflow = DropOldest
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = c.Close() }()

	if err := c.Send(1, "first", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	s.waitEntered()
	if err := c.Send(2, "second", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := c.Send(3, "third", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	s.release()
	waitUntil(t, func() bool {
		sent, _ := c.Stats()
		return sent == 2
	})
	sent, dropped := c.Stats()
	if sent != 2 || dropped != 1 {
		t.Fatalf("want sent=2 dropped=1, got sent=%d dropped=%d", sent, dropped)
	}
}

func TestClientBlockPolicy(t *testing.T) {
	s := newLokiServer(t, 204)
	s.gateBlocks()
	defer s.release()
	cfg := gatedConfig(s.srv.URL)
	cfg.Overflow = Block
	cfg.BlockTimeout = 50 * time.Millisecond
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = c.Close() }()

	if err := c.Send(1, "first", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	s.waitEntered()
	if err := c.Send(2, "second", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	start := time.Now()
	err = c.Send(3, "third", nil)
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("want ErrQueueFull, got %v", err)
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("send returned too early: %v", elapsed)
	}
	s.release()
	waitUntil(t, func() bool {
		sent, _ := c.Stats()
		return sent == 2
	})
}

func TestClientSendAfterClose(t *testing.T) {
	c, err := NewClient(testConfig("http://localhost:0/loki/api/v1/push"))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := c.Send(1, "late", nil); !errors.Is(err, ErrClientClosed) {
		t.Fatalf("want ErrClientClosed, got %v", err)
	}
}

func TestClientInvalidURL(t *testing.T) {
	if _, err := NewClient(testConfig("localhost:3100")); err == nil {
		t.Fatal("want error for invalid URL")
	}
}

func TestClientCloseFlushesRemainingEntries(t *testing.T) {
	s := newLokiServer(t, 204)
	cfg := testConfig(s.srv.URL)
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := c.Send(int64(i), fmt.Sprintf("m-%d", i), nil); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if s.requestCount() < 1 {
		t.Fatal("close did not flush remaining entries")
	}
	sent, _ := c.Stats()
	if sent != 5 {
		t.Fatalf("want sent=5, got %d", sent)
	}
}
