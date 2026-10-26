package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
)

// SlogHandler implements slog.Handler backed by a Loki client.
type SlogHandler struct {
	client *Client
	cfg    Config
	attrs  []slog.Attr
	prefix string
}

var _ slog.Handler = (*SlogHandler)(nil)

// NewSlogHandler creates an slog handler with its own Loki client.
func NewSlogHandler(cfg Config) (*SlogHandler, error) {
	cfg.setDefaults()
	client, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return &SlogHandler{client: client, cfg: cfg}, nil
}

// NewSlogHandlerWithClient creates an slog handler sharing an existing client,
// e.g. together with a logrus Hook.
func NewSlogHandlerWithClient(cfg Config, client *Client) *SlogHandler {
	cfg.setDefaults()
	return &SlogHandler{client: client, cfg: cfg}
}

// Enabled reports whether the given level is handled.
func (h *SlogHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.cfg.SlogMinLevel
}

// Handle sends the record to the underlying client.
func (h *SlogHandler) Handle(ctx context.Context, r slog.Record) error {
	fields := make(map[string]any, r.NumAttrs()+len(h.attrs)+4)
	fields["msg"] = r.Message
	fields["level"] = r.Level.String()
	fields["time"] = r.Time
	appendAttrs(fields, h.prefix, h.attrs)
	appendRecordAttrs(fields, h.prefix, r)

	line, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("loki: format record: %w", err)
	}
	out := string(line)
	if h.cfg.InjectTrace {
		out = injectTrace(out, ctx)
	}
	return h.client.Send(r.Time.UnixNano(), out, dynamicSlogLabels(h.cfg.LabelKeys, h.attrs, r))
}

// WithAttrs returns a copy of the handler with the given attributes attached.
func (h *SlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	clone.attrs = append(clone.attrs, h.attrs...)
	clone.attrs = append(clone.attrs, attrs...)
	return &clone
}

// WithGroup returns a copy of the handler with the given group name appended
// to the prefix of subsequent attributes.
func (h *SlogHandler) WithGroup(name string) slog.Handler {
	clone := *h
	if clone.prefix == "" {
		clone.prefix = name
	} else {
		clone.prefix = clone.prefix + "." + name
	}
	return &clone
}

// Close flushes and stops the client if the handler owns it.
func (h *SlogHandler) Close() error {
	return h.client.Close()
}

// Flush pushes all queued entries to Loki immediately.
func (h *SlogHandler) Flush() error {
	return h.client.Flush()
}

// Stats returns the number of successfully sent and dropped entries.
func (h *SlogHandler) Stats() (sent, dropped int64) {
	return h.client.Stats()
}

func appendAttrs(fields map[string]any, prefix string, attrs []slog.Attr) {
	for _, a := range attrs {
		appendAttr(fields, prefix, a)
	}
}

func appendAttr(fields map[string]any, prefix string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		groupPrefix := a.Key
		if prefix != "" {
			groupPrefix = prefix + "." + a.Key
		}
		for _, ga := range a.Value.Group() {
			appendAttr(fields, groupPrefix, ga)
		}
		return
	}
	key := a.Key
	if prefix != "" {
		key = prefix + "." + a.Key
	}
	fields[key] = a.Value.Any()
}

func appendRecordAttrs(fields map[string]any, prefix string, r slog.Record) {
	r.Attrs(func(a slog.Attr) bool {
		appendAttr(fields, prefix, a)
		return true
	})
}

func dynamicSlogLabels(keys []string, attrs []slog.Attr, r slog.Record) map[string]string {
	if len(keys) == 0 {
		return nil
	}
	var out map[string]string
	for _, a := range attrs {
		if kv, ok := attrString(a, keys); ok {
			if out == nil {
				out = make(map[string]string, len(keys))
			}
			out[kv.key] = kv.value
		}
	}
	r.Attrs(func(a slog.Attr) bool {
		if kv, ok := attrString(a, keys); ok {
			if out == nil {
				out = make(map[string]string, len(keys))
			}
			out[kv.key] = kv.value
		}
		return true
	})
	return out
}

type labelKV struct {
	key   string
	value string
}

func attrString(a slog.Attr, keys []string) (labelKV, bool) {
	a.Value = a.Value.Resolve()
	for _, k := range keys {
		if a.Key == k {
			return labelKV{key: k, value: fmt.Sprint(a.Value.Any())}, true
		}
	}
	return labelKV{}, false
}
