package hook

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// lokiServer is a test helper that captures every push request. The lock is
// held only for short critical sections (never while reading request bodies)
// to avoid deadlocks with concurrent handlers.
type lokiServer struct {
	srv       *httptest.Server
	mu        sync.Mutex
	reqs      []capturedRequest
	failsLeft int
	// gate, when non-nil, makes the handler block until release() closes it.
	// This pins the batcher goroutine in push, so overflow policies can be
	// tested deterministically. Written only before any request is made.
	gate     chan struct{}
	gateOnce sync.Once
	entered  chan struct{}
}

type capturedRequest struct {
	body    []byte
	headers http.Header
}

func newLokiServer(t *testing.T, statusCode int) *lokiServer {
	t.Helper()
	s := &lokiServer{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		_ = r.Body.Close()
		s.mu.Lock()
		fails := s.failsLeft
		if fails > 0 {
			s.failsLeft--
		}
		s.reqs = append(s.reqs, capturedRequest{body: body, headers: r.Header.Clone()})
		s.mu.Unlock()
		if fails > 0 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if s.gate != nil {
			select {
			case s.entered <- struct{}{}:
			default:
			}
			<-s.gate
		}
		w.WriteHeader(statusCode)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// gateBlocks makes the handler wait on the gate before responding.
func (s *lokiServer) gateBlocks() {
	s.gate = make(chan struct{})
	s.entered = make(chan struct{}, 1)
}

// release unblocks every handler waiting on the gate. Idempotent.
func (s *lokiServer) release() {
	if s.gate == nil {
		return
	}
	s.gateOnce.Do(func() { close(s.gate) })
}

// waitEntered blocks until a handler has started waiting on the gate.
func (s *lokiServer) waitEntered() {
	<-s.entered
}

func (s *lokiServer) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

func (s *lokiServer) lastRequest() capturedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.reqs) == 0 {
		return capturedRequest{}
	}
	return s.reqs[len(s.reqs)-1]
}

func (s *lokiServer) decodedPayload(t *testing.T) pushPayload {
	t.Helper()
	req := s.lastRequest()
	zr, err := gzip.NewReader(bytes.NewReader(req.body))
	if err != nil {
		t.Fatalf("open gzip: %v", err)
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read gzip: %v", err)
	}
	var payload pushPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v; raw=%s", err, data)
	}
	return payload
}

func testConfig(url string) Config {
	return Config{
		URL:          url,
		BatchWait:    time.Hour,
		BatchSize:    10,
		QueueDepth:   16,
		Retries:      0,
		RetryBackoff: time.Millisecond,
		Timeout:      time.Second,
		StaticLabels: map[string]string{"app": "test"},
	}
}

// gatedConfig is a config for overflow-policy tests: the batcher pushes
// immediately and blocks in the handler until the test releases it.
func gatedConfig(url string) Config {
	cfg := testConfig(url)
	cfg.BatchSize = 1
	cfg.QueueDepth = 1
	return cfg
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
