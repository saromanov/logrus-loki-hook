package hook

import (
	"fmt"
	"time"

	"github.com/afiskon/promtail-client/promtail"
	"github.com/sirupsen/logrus"
)

var supportedLevels = []logrus.Level{logrus.DebugLevel, logrus.InfoLevel, logrus.WarnLevel, logrus.ErrorLevel}

type Hook struct {
	client promtail.Client
}

// NewHook creates a new hook for Loki
func NewHook() (*Hook, error) {
	conf := promtail.ClientConfig{
		PushURL:            "http://localhost:3100/api/prom/push",
		Labels:             "{source=\"" + "test" + "\",job=\"" + "job" + "\"}",
		BatchWait:          5 * time.Second,
		BatchEntriesNumber: 10000,
		SendLevel:          promtail.INFO,
		PrintLevel:         promtail.ERROR,
	}
	loki, err := promtail.NewClientJson(conf)
	if err != nil {
		return nil, fmt.Errorf("unable to init promtail client: %v", err)
	}
	return &Hook{
		client: loki,
	}, nil
}

// Fire implements interface for logrus
func (hook *Hook) Fire(entry *logrus.Entry) error {
	switch entry.Level.String() {
	case "debug":
		hook.client.Debugf(entry.Level.String())
	case "info":
		hook.client.Infof(entry.Level.String())
	case "warning":
		hook.client.Warnf(entry.Level.String())
	case "error":
		hook.client.Errorf(entry.Level.String())
	default:
		return fmt.Errorf("unknown log level")
	}
	return nil
}

// Levels retruns supported levels
func (hook *Hook) Levels() []logrus.Level {
	return supportedLevels
}
