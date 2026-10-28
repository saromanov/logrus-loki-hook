# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/) and the
project adheres to [Semantic Versioning](https://semver.org/).

## [2.1.0] - 2026-10-28

### Added

- Adaptive batch handling: `Retry-After` headers (delay-seconds and
  HTTP-date) are honored on 429/421/5xx and capped by `Config.MaxRetryWait`;
  batches rejected with 400/413 are split in half until the offending entry
  is isolated; `Config.MaxLineSize` (512 KiB by default) truncates oversized
  lines at a UTF-8 boundary with a ` [truncated]` marker; the effective
  batch size shrinks on repeated rate limiting and recovers after successes. (#9)
- 1-in-N sampling via `Config.SampleRate` for logrus Info/Debug and slog
  levels at or below INFO (warn and above always pass), plus a token-bucket
  delivery cap via `Config.MaxLogsPerSecond` returning `ErrRateLimited`; the
  new `Counters()` API reports sent, dropped, sampled, rate-limited and
  spilled counts. (#10)
- Optional Prometheus text-exposition endpoint via `Config.MetricsAddr`
  serving `/metrics` without the prometheus client dependency: sent, dropped
  by reason, sampled, rate limited, spilled, queue depth, push errors and
  push duration. (#11)
- Durable disk-backed queue via `Config.BufferPath`: overflow entries are
  appended as JSON lines and survive `SIGKILL`; the backlog is drained into
  Loki on the next startup, torn tails from crashes are skipped and counted,
  failed pushes preserve the undelivered tail atomically; `BufferMaxSize`
  and `BufferMaxSegments` control rotation. (#12)

### Fixed

- Dropped entries no longer linger in the batch after a failed push and get
  re-sent on the next attempt.

## [2.0.0] - 2026-10-26

### Changed

- Complete rewrite: native Loki client for the modern push API
  (`/loki/api/v1/push`, JSON + gzip), a background batching goroutine,
  configurable overflow policies (`DropNewest`, `DropOldest`, `Block`),
  retries with exponential backoff, basic auth, bearer token,
  `X-Scope-OrgID`, custom headers and TLS.
- A full `slog.Handler` implementation sharing the same client.
- Dynamic stream labels from logrus fields via `LabelKeys`.
- OpenTelemetry trace correlation via `InjectTrace`.
- `Flush()`/`Close()` guarantee delivery of everything queued.

### Fixed

- v1 logged only the level string instead of the message.

[2.1.0]: https://github.com/saromanov/logrus-loki-hook/compare/v2.0.0...v2.1.0
[2.0.0]: https://github.com/saromanov/logrus-loki-hook/compare/v1.0.0...v2.0.0
