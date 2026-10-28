package hook_test

import (
	"log/slog"
	"time"

	hook "github.com/saromanov/logrus-loki-hook/v2"
	"github.com/sirupsen/logrus"
)

// Using the hook with logrus.
func ExampleNewHook() {
	h, err := hook.NewHook(hook.Config{
		URL:          "http://localhost:3100/loki/api/v1/push",
		StaticLabels: map[string]string{"app": "example"},
		LabelKeys:    []string{"request_id"},
	})
	if err != nil {
		logrus.Fatal(err)
	}
	logrus.AddHook(h)
	logrus.WithField("request_id", "42").Warn("something odd happened")

	// Flush everything queued before exiting.
	if err := h.Close(); err != nil {
		logrus.Fatal(err)
	}
}

// Using the slog handler, optionally alongside the logrus hook on the
// same client.
func ExampleNewSlogHandler() {
	client, err := hook.NewClient(hook.Config{
		URL:          "http://localhost:3100/loki/api/v1/push",
		StaticLabels: map[string]string{"app": "example"},
	})
	if err != nil {
		panic(err)
	}
	defer func() { _ = client.Close() }()

	logrusHook, err := hook.NewHookWithClient(hook.Config{}, client)
	if err != nil {
		panic(err)
	}
	logrus.AddHook(logrusHook)

	logger := slog.New(hook.NewSlogHandlerWithClient(hook.Config{}, client))
	logger.Info("hello from slog", "duration", time.Second)
}
