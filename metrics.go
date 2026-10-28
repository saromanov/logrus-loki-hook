package hook

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const metricsPath = "/metrics"

// startMetricsServer serves the Prometheus text exposition of the client
// counters on the given address. The listener is bound synchronously, so
// bind errors surface from NewClient; serving continues in the background.
func startMetricsServer(addr string, c *Client) (*http.Server, string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", fmt.Errorf("loki: metrics listener on %q: %w", addr, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc(metricsPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		c.renderMetrics(w)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return srv, ln.Addr().String(), nil
}

// renderMetrics writes the counters in the Prometheus text-based format
// without pulling in the prometheus client dependency.
func (c *Client) renderMetrics(w io.Writer) {
	lines := make([]string, 0, 32)
	add := func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}

	add("# HELP loki_client_sent_total Entries delivered to Loki.")
	add("# TYPE loki_client_sent_total counter")
	add("loki_client_sent_total %d", c.sent.Load())

	add("# HELP loki_client_dropped_total Entries lost, by reason.")
	add("# TYPE loki_client_dropped_total counter")
	var dropped int64
	for r, name := range dropReasonNames {
		n := c.dropCounts[r].Load()
		dropped += n
		add("loki_client_dropped_total{reason=%q} %d", name, n)
	}
	add("loki_client_dropped_total %d", dropped)

	add("# HELP loki_client_sampled_total Low-severity entries skipped by SampleRate.")
	add("# TYPE loki_client_sampled_total counter")
	add("loki_client_sampled_total %d", c.sampled.Load())

	add("# HELP loki_client_rate_limited_total Entries skipped by MaxLogsPerSecond.")
	add("# TYPE loki_client_rate_limited_total counter")
	add("loki_client_rate_limited_total %d", c.rateLimited.Load())

	add("# HELP loki_client_queue_depth Entries currently waiting in the queue.")
	add("# TYPE loki_client_queue_depth gauge")
	add("loki_client_queue_depth %d", len(c.queue))

	add("# HELP loki_client_push_errors_total Pushes that failed after all retries.")
	add("# TYPE loki_client_push_errors_total counter")
	add("loki_client_push_errors_total %d", c.pushErrors.Load())

	add("# HELP loki_client_push_duration_seconds Time spent pushing batches to Loki.")
	add("# TYPE loki_client_push_duration_seconds summary")
	add("loki_client_push_duration_seconds_sum %f", float64(c.pushDuration.Load())/float64(time.Second))
	add("loki_client_push_duration_seconds_count %d", c.pushCount.Load())

	_, _ = io.WriteString(w, strings.Join(lines, "\n")+"\n")
}
