package hook

import (
	"fmt"
	"time"

	"github.com/afiskon/promtail-client/promtail"
	"github.com/sirupsen/logrus"
)

var supportedLevels = []logrus.Level{logrus.DebugLevel, logrus.InfoLevel, logrus.WarnLevel, logrus.ErrorLevel}

// Config defines configuration for hook for Loki
type Config struct {
	URL                string
	Labels             string
	BatchWait          time.Duration
	BatchEntriesNumber int
}

func (c *Config) setDefault() {
	if c.URL == "" {
		c.URL = "http://localhost:3100/api/prom/push"
	}
	if c.Labels == "" {
		c.Labels = "{source=\"" + "test" + "\",job=\"" + "job" + "\"}"
	}
	if c.BatchWait == time.Second {
		c.BatchWait = 5 * time.Second
	}
	if c.BatchEntriesNumber == 0 {
		c.BatchEntriesNumber = 10000
	}

}

type Hook struct {
	client promtail.Client
}

// NewHook creates a new hook for Loki
func NewHook(c *Config) (*Hook, error) {
	if c == nil {
		c = &Config{}
	}
	c.setDefault()
	conf := promtail.ClientConfig{
		PushURL:            c.URL,
		Labels:             c.Labels,
		BatchWait:          c.BatchWait,
		BatchEntriesNumber: c.BatchEntriesNumber,
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
