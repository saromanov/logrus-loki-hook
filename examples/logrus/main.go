package main

import (
	lokihook "github.com/saromanov/logrus-loki-hook/v2"
	"github.com/sirupsen/logrus"
)

func main() {
	hook, err := lokihook.NewHook(lokihook.Config{
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
	logrus.WithField("request_id", "42").Error("boom")

	if err := hook.Close(); err != nil {
		logrus.Fatalf("unable to flush logs: %v", err)
	}
	logrus.Info("logs flushed")
}
