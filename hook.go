package hook

import (
	"fmt"

	"github.com/sirupsen/logrus"
)

// allLevels contains every logrus level ordered from most to least severe.
var allLevels = []logrus.Level{
	logrus.PanicLevel,
	logrus.FatalLevel,
	logrus.ErrorLevel,
	logrus.WarnLevel,
	logrus.InfoLevel,
	logrus.DebugLevel,
	logrus.TraceLevel,
}

// Hook is a logrus hook that ships entries to Loki through a Client.
type Hook struct {
	cfg        Config
	client     *Client
	ownsClient bool
}

// NewHook creates a hook with its own background client. Call Close on
// shutdown to flush every queued entry.
func NewHook(cfg Config) (*Hook, error) {
	client, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return &Hook{cfg: client.cfg, client: client, ownsClient: true}, nil
}

// NewHookWithClient creates a hook that shares an existing client. The hook
// does not own the client: Close on the hook is a no-op, and the owner must
// call Client.Close instead.
func NewHookWithClient(cfg Config, client *Client) (*Hook, error) {
	cfg.setDefaults()
	if client == nil {
		return nil, fmt.Errorf("loki: client is required")
	}
	return &Hook{cfg: cfg, client: client}, nil
}

// Fire implements logrus.Hook. It is called synchronously by the logger, so it
// must stay cheap: it only formats the entry and enqueues it.
func (h *Hook) Fire(entry *logrus.Entry) error {
	// logrus levels: lower number == more severe. Drop everything less
	// severe than the configured minimum.
	if int(entry.Level) > int(h.cfg.MinLevel) {
		return nil
	}
	line, err := h.cfg.Formatter.Format(entry)
	if err != nil {
		return fmt.Errorf("loki: format entry: %w", err)
	}
	if h.cfg.InjectTrace {
		line = []byte(injectTrace(string(line), entry.Context))
	}
	labels := dynamicLabels(h.cfg.LabelKeys, entry)
	return h.client.Send(entry.Time.UnixNano(), string(line), labels)
}

// Levels returns the levels the hook is registered for, i.e. every level at
// or above the configured minimum severity.
func (h *Hook) Levels() []logrus.Level {
	out := make([]logrus.Level, 0, len(allLevels))
	for _, l := range allLevels {
		if int(l) <= int(h.cfg.MinLevel) {
			out = append(out, l)
		}
	}
	return out
}

// Flush pushes all queued entries immediately, bypassing the batch timer.
func (h *Hook) Flush() error {
	return h.client.Flush()
}

// Stats returns the number of entries the hook's client has successfully sent
// and dropped.
func (h *Hook) Stats() (sent, dropped int64) {
	return h.client.Stats()
}

// Close stops the hook. If the hook owns its client, the client is closed
// (which flushes every pending entry). For hooks sharing a client this is a
// no-op: the owner is responsible for Client.Close.
func (h *Hook) Close() error {
	if !h.ownsClient {
		return nil
	}
	return h.client.Close()
}

// dynamicLabels extracts label values from entry fields for the configured keys.
func dynamicLabels(keys []string, entry *logrus.Entry) map[string]string {
	if len(keys) == 0 {
		return nil
	}
	labels := make(map[string]string, len(keys))
	for _, key := range keys {
		if v, ok := entry.Data[key]; ok {
			labels[key] = fmt.Sprint(v)
		}
	}
	if len(labels) == 0 {
		return nil
	}
	return labels
}
