# logrus-loki-hook

[![CI](https://github.com/saromanov/logrus-loki-hook/actions/workflows/ci.yml/badge.svg)](https://github.com/saromanov/logrus-loki-hook/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/saromanov/logrus-loki-hook/v2.svg)](https://pkg.go.dev/github.com/saromanov/logrus-loki-hook/v2)
[![Go Report Card](https://goreportcard.com/badge/github.com/saromanov/logrus-loki-hook/v2)](https://goreportcard.com/report/github.com/saromanov/logrus-loki-hook/v2)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Fast, reliable [Loki](https://github.com/grafana/loki) hook for [logrus](https://github.com/sirupsen/logrus) and bridge for `log/slog` — one client, two APIs.

v2 is a complete rewrite: it talks to the modern Loki API (`/loki/api/v1/push`, JSON + gzip) directly, with zero heavyweight dependencies.

## Features

- **No lost logs** — background batching with `Flush()`/`Close()`: everything queued is delivered on shutdown, including `panic`/`fatal` entries
- **Never blocks your app** — async buffered queue with configurable overflow policies: `DropNewest`, `DropOldest`, `Block` (with timeout)
- **Retries with backoff** — transient failures (network errors, `429`, `421`, `5xx`) are retried with exponential backoff; a `Retry-After` header is honored instead of the fixed backoff; permanent `4xx` fail fast
- **Adaptive batching** — batches rejected as too large are split in half until the offender is isolated; the effective batch size shrinks on repeated rate limiting and recovers after successes; oversized lines are truncated with a ` [truncated]` marker instead of failing the batch
- **Grafana Cloud ready** — `X-Scope-OrgID` multi-tenancy header, basic auth, bearer token, custom headers, TLS config
- **Dynamic labels** — promote any logrus field to a Loki stream label with `LabelKeys` (e.g. `request_id`, `pod`)
- **OpenTelemetry correlation** — `InjectTrace` adds `trace_id`/`span_id` from the OTel context to every JSON log line
- **slog support** — a full `slog.Handler` implementation sharing the same client with the logrus hook
- **In-flight observability** — `Stats()` reports sent vs dropped entries, so silent log loss is visible

## Installation

```bash
go get github.com/saromanov/logrus-loki-hook/v2
```

## Usage

### logrus

```go
package main

import (
	"github.com/saromanov/logrus-loki-hook/v2"
	"github.com/sirupsen/logrus"
)

func main() {
	hook, err := hook.NewHook(hook.Config{
		URL:          "http://localhost:3100/loki/api/v1/push",
		StaticLabels: map[string]string{"app": "example", "env": "dev"},
		LabelKeys:    []string{"request_id"},
		InjectTrace:  true,
	})
	if err != nil {
		logrus.Fatalf("unable to create Loki hook: %v", err)
	}
	logrus.AddHook(hook)
	logrus.WithField("request_id", "42").Warn("something odd happened")

	if err := hook.Close(); err != nil {
		logrus.Fatalf("unable to flush logs: %v", err)
	}
}
```

### slog

```go
handler, err := hook.NewSlogHandler(hook.Config{
	URL:          "http://localhost:3100/loki/api/v1/push",
	StaticLabels: map[string]string{"app": "example"},
})
if err != nil {
	log.Fatal(err)
}
logger := slog.New(handler)
logger.Info("hello from slog", "user", "bob")

defer handler.Close()
```

### Grafana Cloud

```go
hook, err := hook.NewHook(hook.Config{
	URL:         "https://logs-prod-006.grafana.net/loki/api/v1/push",
	OrgID:       "12345",
	BasicAuth:   &hook.BasicAuth{Username: "user", Password: "pass"},
	StaticLabels: map[string]string{"app": "example"},
})
```

## Configuration

| Field | Default | Description |
| --- | --- | --- |
| `URL` | `http://localhost:3100/loki/api/v1/push` | Loki push endpoint |
| `StaticLabels` | `{source="logrus-loki-hook"}` | Labels on every stream; keep cardinality low |
| `LabelKeys` | none | Logrus field names promoted to stream labels |
| `Formatter` | `logrus.JSONFormatter` | Renders the log line |
| `BatchWait` | `5s` | Max time between flushes |
| `BatchSize` | `100` | Max entries per push request |
| `QueueDepth` | `4096` | Internal queue capacity |
| `Overflow` | `DropNewest` | `DropNewest`, `DropOldest` or `Block` |
| `BlockTimeout` | `5s` | How long `Fire` blocks when `Overflow` is `Block` |
| `Retries` | `3` | Retries on network errors, `429`, `421`, `5xx` |
| `RetryBackoff` | `500ms` | Base backoff, doubles per attempt |
| `MaxRetryWait` | `30s` | Cap for sleeps derived from `Retry-After` headers |
| `MaxLineSize` | `512KiB` | Truncate longer lines (UTF-8 safe) instead of failing the batch; negative disables |
| `Timeout` | `10s` | HTTP request timeout |
| `BasicAuth` | none | HTTP basic auth credentials |
| `BearerToken` | none | `Authorization: Bearer` header |
| `OrgID` | none | `X-Scope-OrgID` header (multi-tenant Loki / Grafana Cloud) |
| `Headers` | none | Extra headers on every request |
| `TLSConfig` | default | TLS client configuration |
| `MinLevel` | `debug` | Minimum logrus level forwarded |
| `SlogMinLevel` | `INFO` | Minimum slog level handled |
| `InjectTrace` | `false` | Add `trace_id`/`span_id` from OTel context |

## Migration from v1

```diff
-import "github.com/saromanov/logrus-loki-hook"
+import "github.com/saromanov/logrus-loki-hook/v2"

-hook.NewHook(&hook.Config{URL: "...", Labels: "{app=\"x\"}"})
+hook.NewHook(hook.Config{URL: ".../loki/api/v1/push", StaticLabels: map[string]string{"app": "x"}})
```

v2 also fixes a v1 bug that logged only the level string instead of the message.

## Examples

See [examples/](examples/) for runnable logrus and slog programs.

## License

[MIT](LICENSE)
