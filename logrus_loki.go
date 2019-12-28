package hook

import (
	"github.com/sirupsen/logrus"
)

var supportedLevels = []logrus.Level{logrus.DebugLevel, logrus.InfoLevel, logrus.WarnLevel, logrus.ErrorLevel}

type Hook struct {

}
// NewHook creates a new hook for Loki
func NewHook() (*Hook, error) {
	return &Hook {

	}
}

func (hook *Hook) Fire(entry *logrus.Entry) error {
	
	return nil
}

func (hook *Hook) Levels() []logrus.Level {
	return supportedLevels
}

