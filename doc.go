// Package hook provides a logrus hook and an slog handler that ship logs to
// Grafana Loki over the native HTTP push API (/loki/api/v1/push).
//
// The client batches entries in a background goroutine, applies configurable
// overflow policies, retries transient failures with exponential backoff and
// flushes everything on Close, so no logs are lost on shutdown.
//
// Optional hardening for production deployments:
//
//   - SampleRate and MaxLogsPerSecond protect Loki from log floods while
//     keeping intentional loss visible in Counters().
//   - MetricsAddr serves a Prometheus /metrics endpoint with delivery
//     counters and drop reasons.
//   - BufferPath keeps entries that do not fit into the queue on disk and
//     drains them into Loki on the next startup, so logs survive crashes.
package hook
