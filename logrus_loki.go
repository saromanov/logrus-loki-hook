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
	hook.client.Debugf(entry.String())
	return nil
}

// Levels retruns supported levels
func (hook *Hook) Levels() []logrus.Level {
	return supportedLevels
}
