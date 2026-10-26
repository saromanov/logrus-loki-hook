package main

import (
	"log/slog"
	"os"

	lokihook "github.com/saromanov/logrus-loki-hook/v2"
)

func main() {
	handler, err := lokihook.NewSlogHandler(lokihook.Config{
		URL:          "http://localhost:3100/loki/api/v1/push",
		StaticLabels: map[string]string{"app": "example", "env": "dev"},
	})
	if err != nil {
		slog.Error("unable to create Loki handler", "error", err)
		os.Exit(1)
	}
	defer func() { _ = handler.Close() }()

	logger := slog.New(handler)
	logger.Info("hello from slog", "user", "bob")
}
