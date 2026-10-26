package hook

import (
	"context"
	"encoding/json"

	"go.opentelemetry.io/otel/trace"
)

// injectTrace adds trace_id and span_id fields from the OpenTelemetry context
// to a JSON-formatted log line. If the line is not a JSON object or the
// context carries no valid span, the line is returned unchanged.
func injectTrace(line string, ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return line
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		return line
	}
	obj["trace_id"] = sc.TraceID().String()
	obj["span_id"] = sc.SpanID().String()
	out, err := json.Marshal(obj)
	if err != nil {
		return line
	}
	return string(out)
}
