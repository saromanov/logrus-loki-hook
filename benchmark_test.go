package hook

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// BenchmarkHookFire measures the synchronous cost of a logrus Fire call:
// JSON formatting plus enqueueing into the client queue. The batcher keeps
// up with a local server, so the queue stays near empty.
func BenchmarkHookFire(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	h, err := NewHook(Config{
		URL:        srv.URL,
		BatchWait:  time.Hour,
		BatchSize:  10000,
		QueueDepth: 1 << 17,
	})
	if err != nil {
		b.Fatalf("new hook: %v", err)
	}
	defer func() { _ = h.Close() }()

	logger := logrus.New()
	logger.SetOutput(io.Discard)
	logger.AddHook(h)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Info("benchmark message with a few fields", "seq", i)
	}
}

// BenchmarkEncode measures rendering of a 100-entry push payload.
func BenchmarkEncode(b *testing.B) {
	c, err := NewClient(testConfig("http://localhost:0/loki/api/v1/push"))
	if err != nil {
		b.Fatalf("new client: %v", err)
	}
	entries := make([]logEntry, 100)
	for i := range entries {
		entries[i] = logEntry{
			ts:   time.Now().UnixNano(),
			line: `{"level":"info","msg":"benchmark message","seq":` + strconv.Itoa(i) + `}`,
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.encode(entries); err != nil {
			b.Fatalf("encode: %v", err)
		}
	}
}

// BenchmarkTruncateLine measures the no-op path of the line truncation.
func BenchmarkTruncateLine(b *testing.B) {
	line := `{"level":"info","msg":"a reasonably sized log line"}` + strings.Repeat("x", 300)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = truncateLine(line, 512*1024)
	}
}
