// Package hook provides a logrus hook and an slog handler that ship logs to
// Grafana Loki over the native HTTP push API (/loki/api/v1/push).
//
// The client batches entries in a background goroutine, applies configurable
// overflow policies, retries transient failures with exponential backoff and
// flushes everything on Close, so no logs are lost on shutdown.
package hook

import (
	"crypto/tls"
	"log/slog"
	"time"

	"github.com/sirupsen/logrus"
)

// OverflowPolicy determines what happens when the internal queue is full.
type OverflowPolicy int

const (
	// DropNewest discards the entry that does not fit into the queue (default).
	DropNewest OverflowPolicy = iota
	// DropOldest discards the oldest queued entry to make room for the new one.
	DropOldest
	// Block blocks the caller until there is room or BlockTimeout expires.
	Block
)

// BasicAuth holds credentials for HTTP basic authentication.
type BasicAuth struct {
	Username string
	Password string
}

// Config defines the configuration for the Loki client, the logrus hook and
// the slog handler.
type Config struct {
	// URL is the Loki push endpoint, for example:
	//   http://localhost:3100/loki/api/v1/push
	//   https://logs-prod-006.grafana.net/loki/api/v1/push
	URL string

	// StaticLabels are added to every stream. Keep the cardinality low.
	StaticLabels map[string]string

	// LabelKeys lists log field names that must be promoted to stream labels.
	// Promoted fields are also kept in the log line.
	LabelKeys []string

	// Formatter renders the log line. Defaults to logrus.JSONFormatter.
	Formatter logrus.Formatter

	// BatchWait is the maximum time between flushes.
	BatchWait time.Duration

	// BatchSize is the maximum number of entries pushed in a single request.
	BatchSize int

	// QueueDepth is the capacity of the internal queue.
	QueueDepth int

	// Overflow selects the policy applied when the queue is full.
	Overflow OverflowPolicy

	// BlockTimeout limits how long Fire blocks when Overflow is Block.
	BlockTimeout time.Duration

	// Retries is the number of retries for retryable responses
	// (network errors, 429, 421 and 5xx).
	Retries int

	// RetryBackoff is the base backoff between retries; it doubles on
	// every attempt.
	RetryBackoff time.Duration

	// MaxRetryWait caps the sleep derived from a Retry-After response
	// header. Defaults to 30s.
	MaxRetryWait time.Duration

	// MaxLineSize truncates log lines longer than this many bytes (at a
	// UTF-8 sequence boundary) and appends a " [truncated]" marker, so a
	// single oversized line no longer fails the whole batch. Zero means the
	// Loki default (512 KiB); a negative value disables truncation.
	MaxLineSize int

	// Timeout is the HTTP request timeout.
	Timeout time.Duration

	// BasicAuth enables HTTP basic authentication.
	BasicAuth *BasicAuth

	// BearerToken adds an "Authorization: Bearer <token>" header.
	BearerToken string

	// OrgID sets the X-Scope-OrgID header for multi-tenant Loki setups,
	// e.g. Grafana Cloud.
	OrgID string

	// Headers are added to every request.
	Headers map[string]string

	// TLSConfig configures the TLS client. Nil means default settings.
	TLSConfig *tls.Config

	// MinLevel filters logrus entries less severe than the given level.
	// Defaults to DebugLevel (only TraceLevel is dropped) when left at the
	// zero value.
	MinLevel logrus.Level

	// SlogMinLevel filters slog records less severe than the given level.
	// Defaults to slog.LevelInfo when left at the zero value.
	SlogMinLevel slog.Level

	// InjectTrace adds trace_id and span_id fields from the OpenTelemetry
	// context to JSON-formatted log lines.
	InjectTrace bool

	// SampleRate enables 1-in-N sampling for low-severity entries: logrus
	// levels at or below Info and slog levels at or below INFO. Warn and
	// above always pass. Zero or one disables sampling.
	SampleRate int

	// MaxLogsPerSecond caps the delivery rate with a token bucket whose
	// burst equals the rate. Faster entries are skipped and counted as
	// rate limited. Zero disables the limit.
	MaxLogsPerSecond float64

	// MetricsAddr, when set (for example ":9080"), serves a Prometheus
	// text-exposition endpoint at /metrics with the delivery counters:
	// sent, dropped by reason, sampled, rate limited, queue depth, push
	// errors and push duration. Use a dedicated address and keep one
	// metrics endpoint per process.
	MetricsAddr string
}

const defaultURL = "http://localhost:3100/loki/api/v1/push"

// defaultMaxLineSize matches the default Loki `max_line_size` limit.
const defaultMaxLineSize = 512 * 1024

func (c *Config) setDefaults() {
	if c.URL == "" {
		c.URL = defaultURL
	}
	if len(c.StaticLabels) == 0 {
		c.StaticLabels = map[string]string{"source": "logrus-loki-hook"}
	}
	if c.Formatter == nil {
		c.Formatter = &logrus.JSONFormatter{}
	}
	if c.BatchWait == 0 {
		c.BatchWait = 5 * time.Second
	}
	if c.BatchSize == 0 {
		c.BatchSize = 100
	}
	if c.QueueDepth == 0 {
		c.QueueDepth = 4096
	}
	if c.BlockTimeout == 0 {
		c.BlockTimeout = 5 * time.Second
	}
	if c.Retries == 0 {
		c.Retries = 3
	}
	if c.RetryBackoff == 0 {
		c.RetryBackoff = 500 * time.Millisecond
	}
	if c.MaxRetryWait == 0 {
		c.MaxRetryWait = 30 * time.Second
	}
	if c.MaxLineSize == 0 {
		c.MaxLineSize = defaultMaxLineSize
	}
	if c.Timeout == 0 {
		c.Timeout = 10 * time.Second
	}
	// The zero value of logrus.Level equals PanicLevel, so it doubles as the
	// "unset" sentinel here. Explicitly requesting panic-only logging is not
	// supported: any lower value is treated as unset.
	if c.MinLevel == 0 {
		c.MinLevel = logrus.DebugLevel
	}
	if c.SlogMinLevel == 0 {
		c.SlogMinLevel = slog.LevelInfo
	}
}
