package hook

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func newSlog(t *testing.T, cfg Config) (*slog.Logger, *SlogHandler) {
	t.Helper()
	h, err := NewSlogHandler(cfg)
	if err != nil {
		t.Fatalf("new slog handler: %v", err)
	}
	return slog.New(h), h
}

func TestSlogHandlerSendsRecords(t *testing.T) {
	s := newLokiServer(t, 204)
	logger, handler := newSlog(t, Config{URL: s.srv.URL, BatchWait: time.Hour})
	logger.Info("from slog")
	if err := handler.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	obj := decodeLastLine(t, s)
	if obj["msg"] != "from slog" {
		t.Fatalf("want msg=from slog, got %v", obj["msg"])
	}
	if obj["level"] != "INFO" {
		t.Fatalf("want level=INFO, got %v", obj["level"])
	}
	if err := handler.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestSlogHandlerAttrsAndGroups(t *testing.T) {
	s := newLokiServer(t, 204)
	_, handler := newSlog(t, Config{URL: s.srv.URL, BatchWait: time.Hour})
	grouped := handler.WithGroup("http").WithAttrs([]slog.Attr{
		slog.String("method", "GET"),
		slog.Int("status", 200),
	})
	g := slog.New(grouped)
	g.Info("request handled", slog.String("path", "/health"))
	if err := handler.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	obj := decodeLastLine(t, s)
	if obj["http.method"] != "GET" {
		t.Fatalf("want http.method=GET, got %v", obj["http.method"])
	}
	if obj["http.status"] != float64(200) {
		t.Fatalf("want http.status=200, got %v", obj["http.status"])
	}
	if obj["http.path"] != "/health" {
		t.Fatalf("want http.path=/health, got %v", obj["http.path"])
	}
	if obj["msg"] != "request handled" {
		t.Fatalf("want msg, got %v", obj["msg"])
	}
	if err := handler.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestSlogHandlerRespectsMinLevel(t *testing.T) {
	s := newLokiServer(t, 204)
	logger, handler := newSlog(t, Config{URL: s.srv.URL, BatchWait: time.Hour, SlogMinLevel: slog.LevelWarn})
	logger.Debug("skipped")
	logger.Warn("kept")
	if err := handler.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	sent, _ := handler.Stats()
	if sent != 1 {
		t.Fatalf("want sent=1, got %d", sent)
	}
	if err := handler.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestSlogHandlerSharedClientWithLogrus(t *testing.T) {
	s := newLokiServer(t, 204)
	client, err := NewClient(Config{URL: s.srv.URL, BatchWait: time.Hour})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	hook, err := NewHookWithClient(Config{}, client)
	if err != nil {
		t.Fatalf("new hook with client: %v", err)
	}
	entry := logrus.NewEntry(logrus.New())
	entry.Level = logrus.InfoLevel
	entry.Message = "logrus side"
	if err := hook.Fire(entry); err != nil {
		t.Fatalf("fire logrus: %v", err)
	}

	slogHandler := NewSlogHandlerWithClient(Config{URL: s.srv.URL}, client)
	rec := slog.NewRecord(time.Now(), slog.LevelInfo, "slog side", 0)
	if err := slogHandler.Handle(context.Background(), rec); err != nil {
		t.Fatalf("handle slog: %v", err)
	}

	if err := client.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	sent, _ := client.Stats()
	if sent != 2 {
		t.Fatalf("want sent=2, got %d", sent)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestSlogHandlerSamplesLowSeverityRecords(t *testing.T) {
	s := newLokiServer(t, 204)
	logger, handler := newSlog(t, Config{URL: s.srv.URL, BatchWait: time.Hour, SampleRate: 2})
	for i := 0; i < 4; i++ {
		logger.Info(fmt.Sprintf("info-%d", i))
	}
	logger.Warn("warn-0") // warn and above are never sampled
	if err := handler.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	payload := s.decodedPayload(t)
	total := 0
	for _, st := range payload.Streams {
		total += len(st.Values)
	}
	if total != 3 { // info-0 and info-2 kept by sampling, plus the warn
		t.Fatalf("want 3 delivered records, got %d", total)
	}
	cn := handler.Counters()
	if cn.Sent != 3 || cn.Sampled != 2 {
		t.Fatalf("want sent=3 sampled=2, got %+v", cn)
	}
	if err := handler.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
