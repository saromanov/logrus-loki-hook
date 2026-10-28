package hook

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// writeDiskBuffer seeds a buffer file with the given entries.
func writeDiskBuffer(t *testing.T, path string, entries ...logEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create buffer file: %v", err)
	}
	w := bufio.NewWriter(f)
	for _, e := range entries {
		raw, err := json.Marshal(diskEntry{TS: e.ts, Line: e.line, Labels: e.labels})
		if err != nil {
			t.Fatalf("marshal entry: %v", err)
		}
		if _, err := w.Write(append(raw, '\n')); err != nil {
			t.Fatalf("write entry: %v", err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// bufferLines returns the lines currently stored in the buffer file.
func bufferLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read buffer file: %v", err)
	}
	var out []string
	for _, line := range bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var de diskEntry
		if err := json.Unmarshal(line, &de); err != nil {
			t.Fatalf("unmarshal buffer line: %v", err)
		}
		out = append(out, de.Line)
	}
	return out
}

// deliveredLines returns every line the server received, in arrival order.
func deliveredLines(t *testing.T, s *lokiServer) []string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.reqs {
		zr, err := gzip.NewReader(bytes.NewReader(r.body))
		if err != nil {
			t.Fatalf("open gzip: %v", err)
		}
		data, err := io.ReadAll(zr)
		if err != nil {
			t.Fatalf("read gzip: %v", err)
		}
		var p pushPayload
		if err := json.Unmarshal(data, &p); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		for _, st := range p.Streams {
			for _, v := range st.Values {
				out = append(out, v[1])
			}
		}
	}
	return out
}

func bufferConfig(url, path string) Config {
	cfg := testConfig(url)
	cfg.BufferPath = path
	return cfg
}

func TestDiskBufferSpillsOnOverflow(t *testing.T) {
	s := newLokiServer(t, 204)
	s.gateBlocks()
	defer s.release()
	path := filepath.Join(t.TempDir(), "buffer.jsonl")
	cfg := gatedConfig(s.srv.URL)
	cfg.BufferPath = path
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = c.Close() }()

	if err := c.Send(1, "first", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	s.waitEntered() // first entry pinned in push
	if err := c.Send(2, "second", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := c.Send(3, "third", nil); err != nil {
		t.Fatalf("overflow send with buffer: %v", err)
	}
	cn := c.Counters()
	if cn.Spilled != 1 || cn.Dropped != 0 {
		t.Fatalf("want spilled=1 dropped=0, got %+v", cn)
	}
	if got := bufferLines(t, path); !slices.Equal(got, []string{"third"}) {
		t.Fatalf("want buffer [third], got %v", got)
	}
	s.release()
	waitUntil(t, func() bool {
		sent, _ := c.Stats()
		return sent == 2
	})
}

func TestDiskBufferDrainsOnStartup(t *testing.T) {
	s := newLokiServer(t, 204)
	path := filepath.Join(t.TempDir(), "buffer.jsonl")
	writeDiskBuffer(t, path,
		logEntry{ts: 1, line: "backlog-1"},
		logEntry{ts: 2, line: "backlog-2"},
		logEntry{ts: 3, line: "backlog-3"},
	)
	c, err := NewClient(bufferConfig(s.srv.URL, path))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	// Drain happens synchronously before the client is returned.
	sent, _ := c.Stats()
	if sent != 3 {
		t.Fatalf("want sent=3 after startup drain, got %d", sent)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("buffer file must be removed after drain, got %v", err)
	}
	got := deliveredLines(t, s)
	if !slices.Equal(got, []string{"backlog-1", "backlog-2", "backlog-3"}) {
		t.Fatalf("want backlog delivered in order, got %v", got)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestDiskBufferSurvivesRestart(t *testing.T) {
	s1 := newLokiServer(t, 204)
	s1.gateBlocks()
	defer s1.release()
	path := filepath.Join(t.TempDir(), "buffer.jsonl")
	cfg := testConfig(s1.srv.URL)
	cfg.BatchSize = 4
	cfg.QueueDepth = 4
	cfg.BufferPath = path
	c1, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	// Fill the first batch; it gets pinned at the gate, so the fate of the
	// following entries is deterministic.
	for i := 1; i <= 4; i++ {
		if err := c1.Send(int64(i), fmt.Sprintf("line-%d", i), nil); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	s1.waitEntered()
	// Four more fill the queue, two spill to disk.
	for i := 5; i <= 10; i++ {
		if err := c1.Send(int64(i), fmt.Sprintf("line-%d", i), nil); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	s1.release()
	waitUntil(t, func() bool {
		sent, _ := c1.Stats()
		return sent == 8 // batch of 4 + queue of 4 on close
	})
	if cn := c1.Counters(); cn.Spilled != 2 {
		t.Fatalf("want spilled=2, got %+v", cn)
	}
	if got := bufferLines(t, path); !slices.Equal(got, []string{"line-9", "line-10"}) {
		t.Fatalf("unexpected buffer content: %v", got)
	}
	if err := c1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	sent, _ := c1.Stats()
	if sent != 8 {
		t.Fatalf("want sent=8 on first client, got %d", sent)
	}

	// Restart with a fresh server: the backlog is delivered first.
	s2 := newLokiServer(t, 204)
	c2, err := NewClient(bufferConfig(s2.srv.URL, path))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	sent2, _ := c2.Stats()
	if sent2 != 2 {
		t.Fatalf("want sent=2 on restart, got %d", sent2)
	}
	got := deliveredLines(t, s2)
	if !slices.Equal(got, []string{"line-9", "line-10"}) {
		t.Fatalf("want backlog in order, got %v", got)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("buffer file must be removed after drain, got %v", err)
	}
	if err := c2.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestDiskBufferRotation(t *testing.T) {
	s1 := newLokiServer(t, 204)
	s1.gateBlocks()
	defer s1.release()
	path := filepath.Join(t.TempDir(), "buffer.jsonl")
	cfg := gatedConfig(s1.srv.URL)
	cfg.BufferPath = path
	cfg.BufferMaxSize = 50 // one entry per segment
	cfg.BufferMaxSegments = 4
	c1, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	// Pin the first entry at the gate so every later Send is deterministic.
	if err := c1.Send(1, "line-1", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	s1.waitEntered()
	// second fills the queue, four spill and rotate
	for i := 2; i <= 6; i++ {
		if err := c1.Send(int64(i), fmt.Sprintf("line-%d", i), nil); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	s1.release()
	waitUntil(t, func() bool {
		sent, _ := c1.Stats()
		return sent == 2
	})
	if cn := c1.Counters(); cn.Spilled != 4 {
		t.Fatalf("want spilled=4, got %+v", cn)
	}
	if err := c1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2 := newLokiServer(t, 204)
	c2, err := NewClient(bufferConfig(s2.srv.URL, path))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	sent, _ := c2.Stats()
	if sent != 4 {
		t.Fatalf("want sent=4 on restart, got %d", sent)
	}
	got := deliveredLines(t, s2)
	if !slices.Equal(got, []string{"line-3", "line-4", "line-5", "line-6"}) {
		t.Fatalf("want rotated backlog in order, got %v", got)
	}
	if err := c2.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestDiskBufferSkipsTornTail(t *testing.T) {
	s := newLokiServer(t, 204)
	path := filepath.Join(t.TempDir(), "buffer.jsonl")
	writeDiskBuffer(t, path,
		logEntry{ts: 1, line: "good-1"},
		logEntry{ts: 2, line: "good-2"},
	)
	// Simulate a crash mid-write: append a partial line without a newline.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString(`{"ts":3,"line":"torn`); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = f.Close()

	c, err := NewClient(bufferConfig(s.srv.URL, path))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	sent, dropped := c.Stats()
	if sent != 2 || dropped != 1 {
		t.Fatalf("want sent=2 dropped=1, got sent=%d dropped=%d", sent, dropped)
	}
	if got := deliveredLines(t, s); !slices.Equal(got, []string{"good-1", "good-2"}) {
		t.Fatalf("want only intact entries delivered, got %v", got)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestDiskBufferPushFailureRespills(t *testing.T) {
	s1 := newLokiServer(t, 204)
	s1.failsLeft = 1000
	path := filepath.Join(t.TempDir(), "buffer.jsonl")
	writeDiskBuffer(t, path,
		logEntry{ts: 1, line: "lost-1"},
		logEntry{ts: 2, line: "lost-2"},
	)
	cfg := bufferConfig(s1.srv.URL, path)
	cfg.Retries = 1
	cfg.RetryBackoff = time.Millisecond
	c1, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	sent, _ := c1.Stats()
	if sent != 0 {
		t.Fatalf("want sent=0 when Loki is down, got %d", sent)
	}
	if got := bufferLines(t, path); !slices.Equal(got, []string{"lost-1", "lost-2"}) {
		t.Fatalf("want backlog preserved on failure, got %v", got)
	}
	if err := c1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2 := newLokiServer(t, 204)
	c2, err := NewClient(bufferConfig(s2.srv.URL, path))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	sent, _ = c2.Stats()
	if sent != 2 {
		t.Fatalf("want sent=2 on restart, got %d", sent)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("buffer file must be removed after drain, got %v", err)
	}
	if err := c2.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestDiskBufferSpillFailureFallsBackToDrop(t *testing.T) {
	s := newLokiServer(t, 204)
	s.gateBlocks()
	defer s.release()
	dir := t.TempDir()
	path := filepath.Join(dir, "buffer.jsonl")
	cfg := gatedConfig(s.srv.URL)
	cfg.BufferPath = path
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
	// Break the buffer location, then overflow again.
	if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove dir: %v", err)
	}
	err = c.Send(3, "third", nil)
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("want ErrQueueFull when spill fails, got %v", err)
	}
	cn := c.Counters()
	if cn.Spilled != 0 || cn.Dropped != 1 {
		t.Fatalf("want spilled=0 dropped=1, got %+v", cn)
	}
}

func TestDiskBufferInitFailsOnBadPath(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	cfg := testConfig("http://localhost:0/loki/api/v1/push")
	cfg.BufferPath = filepath.Join(blocker, "buffer.jsonl") // parent is a file
	if _, err := NewClient(cfg); err == nil {
		t.Fatal("want error for unusable buffer path")
	}
}
