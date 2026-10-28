package hook

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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

func TestClientTruncatesOversizedLines(t *testing.T) {
	s := newLokiServer(t, 204)
	cfg := testConfig(s.srv.URL)
	cfg.MaxLineSize = 100
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if err := c.Send(time.Now().UnixNano(), strings.Repeat("a", 5000), nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	line := s.decodedPayload(t).Streams[0].Values[0][1]
	if len(line) != 100 {
		t.Fatalf("want truncated line of 100 bytes, got %d", len(line))
	}
	if !strings.HasSuffix(line, truncateMarker) {
		t.Fatalf("line is not marked as truncated: %q", line)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestClientTruncationKeepsValidUTF8(t *testing.T) {
	s := newLokiServer(t, 204)
	cfg := testConfig(s.srv.URL)
	cfg.MaxLineSize = 40
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	long := strings.Repeat("ß", 25) // 50 bytes, 2-byte runes
	if err := c.Send(time.Now().UnixNano(), long, nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	line := s.decodedPayload(t).Streams[0].Values[0][1]
	if len(line) != 40 {
		t.Fatalf("want 40 bytes, got %d", len(line))
	}
	if !utf8.ValidString(line) {
		t.Fatalf("truncated line broke UTF-8: %q", line)
	}
	if !strings.HasSuffix(line, truncateMarker) {
		t.Fatalf("line is not marked as truncated: %q", line)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestClientTruncationDisabled(t *testing.T) {
	s := newLokiServer(t, 204)
	cfg := testConfig(s.srv.URL)
	cfg.MaxLineSize = -1
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	long := strings.Repeat("a", 600000)
	if err := c.Send(time.Now().UnixNano(), long, nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	line := s.decodedPayload(t).Streams[0].Values[0][1]
	if len(line) != 600000 {
		t.Fatalf("want untouched line of 600000 bytes, got %d", len(line))
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestClientSplitsBatchRejectedByLoki(t *testing.T) {
	s := newLokiServer(t, 204)
	s.badLeft = 1 // first push (whole batch) is rejected, halves succeed
	cfg := testConfig(s.srv.URL)
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	for i := 0; i < 4; i++ {
		if err := c.Send(time.Now().UnixNano(), fmt.Sprintf("line-%d", i), nil); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if s.requestCount() != 3 {
		t.Fatalf("want 3 requests (batch + 2 halves), got %d", s.requestCount())
	}
	if st := s.statusAt(0); st != http.StatusBadRequest {
		t.Fatalf("want first request rejected, got %d", st)
	}
	for i := 1; i < 3; i++ {
		if s.totalValuesAt(t, i) != 2 {
			t.Fatalf("request %d: want 2 values per half, got %d", i, s.totalValuesAt(t, i))
		}
	}
	sent, dropped := c.Stats()
	if sent != 4 || dropped != 0 {
		t.Fatalf("want sent=4 dropped=0, got sent=%d dropped=%d", sent, dropped)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestClientDropsRejectedSingleEntry(t *testing.T) {
	s := newLokiServer(t, 204)
	s.badLeft = 3 // batch of 2 and both halves fail
	cfg := testConfig(s.srv.URL)
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if err := c.Send(time.Now().UnixNano(), "a", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := c.Send(time.Now().UnixNano(), "b", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if s.requestCount() != 3 {
		t.Fatalf("want 3 requests, got %d", s.requestCount())
	}
	sent, dropped := c.Stats()
	if sent != 0 || dropped != 2 {
		t.Fatalf("want sent=0 dropped=2, got sent=%d dropped=%d", sent, dropped)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// dumpRequests logs every captured request for debugging failed assertions.
func (s *lokiServer) dumpRequests(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.reqs {
		vals := 0
		lines := []string{}
		if zr, err := gzip.NewReader(bytes.NewReader(r.body)); err == nil {
			if data, err := io.ReadAll(zr); err == nil {
				var p pushPayload
				if json.Unmarshal(data, &p) == nil {
					for _, st := range p.Streams {
						vals += len(st.Values)
						for _, v := range st.Values {
							lines = append(lines, v[1])
						}
					}
				}
			}
		}
		t.Logf("request %d: status=%d values=%d bytes=%d lines=%v", i, r.status, vals, len(r.body), lines)
	}
}

func TestClientHonorsRetryAfter(t *testing.T) {
	s := newLokiServer(t, 204)
	s.rateLimitLeft = 1
	s.retryAfterValue = "1"
	cfg := testConfig(s.srv.URL)
	cfg.Retries = 1
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if err := c.Send(time.Now().UnixNano(), "throttled", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	start := time.Now()
	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("Retry-After was not honored, flushed in %v", elapsed)
	}
	if s.requestCount() != 2 {
		t.Fatalf("want 2 requests, got %d", s.requestCount())
	}
	sent, dropped := c.Stats()
	if sent != 1 || dropped != 0 {
		t.Fatalf("want sent=1 dropped=0, got sent=%d dropped=%d", sent, dropped)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestClientCapsRetryAfter(t *testing.T) {
	s := newLokiServer(t, 204)
	s.rateLimitLeft = 1
	s.retryAfterValue = "3600"
	cfg := testConfig(s.srv.URL)
	cfg.Retries = 1
	cfg.MaxRetryWait = 20 * time.Millisecond
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if err := c.Send(time.Now().UnixNano(), "throttled", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if s.requestCount() != 2 {
		t.Fatalf("want 2 requests, got %d", s.requestCount())
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestClientAdaptiveShrinkOnRateLimit(t *testing.T) {
	s := newLokiServer(t, 204)
	s.rateLimitLeft = 2 // first push fails even after its retry
	s.retryAfterValue = "0"
	cfg := testConfig(s.srv.URL)
	cfg.BatchSize = 4
	cfg.QueueDepth = 16
	cfg.Retries = 1
	s.gateBlocks()
	defer s.release()
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = c.Close() }()

	for i := 0; i < 8; i++ {
		if err := c.Send(time.Now().UnixNano(), fmt.Sprintf("line-%d", i), nil); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	s.waitEntered() // the push after the failed batch is pinned at the gate
	s.release()
	waitUntil(t, func() bool { return s.requestCount() >= 3 })
	if s.requestCount() != 3 {
		s.dumpRequests(t)
		t.Fatalf("want 3 requests before close, got %d", s.requestCount())
	}
	if st := s.statusAt(0); st != http.StatusTooManyRequests {
		t.Fatalf("want first request rate limited, got %d", st)
	}
	if st := s.statusAt(1); st != http.StatusTooManyRequests {
		t.Fatalf("want retry rate limited, got %d", st)
	}
	if s.totalValuesAt(t, 2) != 2 {
		s.dumpRequests(t)
		t.Fatalf("want batch shrunk from 4 to 2, got %d values", s.totalValuesAt(t, 2))
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if s.requestCount() != 4 {
		s.dumpRequests(t)
		t.Fatalf("want 4 requests after close, got %d", s.requestCount())
	}
	sent, dropped := c.Stats()
	if sent != 4 || dropped != 4 {
		t.Fatalf("want sent=4 dropped=4, got sent=%d dropped=%d", sent, dropped)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	if d, ok := parseRetryAfter("30", now); !ok || d != 30*time.Second {
		t.Fatalf("seconds form: got %v ok=%v", d, ok)
	}
	if d, ok := parseRetryAfter(now.Add(time.Minute).Format(http.TimeFormat), now); !ok || d != time.Minute {
		t.Fatalf("http-date form: got %v ok=%v", d, ok)
	}
	if d, ok := parseRetryAfter(now.Add(-time.Minute).Format(http.TimeFormat), now); !ok || d != 0 {
		t.Fatalf("past http-date: got %v ok=%v", d, ok)
	}
	if _, ok := parseRetryAfter("", now); ok {
		t.Fatal("empty header must not parse")
	}
	if _, ok := parseRetryAfter("soon", now); ok {
		t.Fatal("garbage must not parse")
	}
}

func TestClientRateLimitsWithTokenBucket(t *testing.T) {
	s := newLokiServer(t, 204)
	cfg := testConfig(s.srv.URL)
	cfg.MaxLogsPerSecond = 2 // burst of 2, sends happen within microseconds
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = c.Close() }()

	allowed := 0
	for i := 0; i < 6; i++ {
		if err := c.Send(time.Now().UnixNano(), fmt.Sprintf("line-%d", i), nil); err != nil {
			if !errors.Is(err, ErrRateLimited) {
				t.Fatalf("send: %v", err)
			}
			continue
		}
		allowed++
	}
	if allowed != 2 {
		t.Fatalf("want 2 entries admitted by the limiter, got %d", allowed)
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	cn := c.Counters()
	if cn.Sent != 2 || cn.RateLimited != 4 || cn.Dropped != 0 {
		t.Fatalf("want sent=2 rate_limited=4 dropped=0, got %+v", cn)
	}
}

func TestClientTokenBucketRefillsOverTime(t *testing.T) {
	s := newLokiServer(t, 204)
	cfg := testConfig(s.srv.URL)
	cfg.MaxLogsPerSecond = 10
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = c.Close() }()

	for i := 0; i < 10; i++ { // drain the burst
		if err := c.Send(time.Now().UnixNano(), "burst", nil); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	if err := c.Send(time.Now().UnixNano(), "over", nil); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("want ErrRateLimited, got %v", err)
	}
	time.Sleep(150 * time.Millisecond) // ~1.5 tokens refill
	if err := c.Send(time.Now().UnixNano(), "after-refill", nil); err != nil {
		t.Fatalf("send after refill: %v", err)
	}
}
