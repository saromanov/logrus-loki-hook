package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/trace"
)

func entryFor(t *testing.T, h *Hook, level logrus.Level, msg string, fields logrus.Fields) error {
	t.Helper()
	logger := logrus.New()
	entry := logrus.NewEntry(logger).WithFields(fields)
	entry.Level = level
	entry.Message = msg
	return h.Fire(entry)
}

func decodeLastLine(t *testing.T, s *lokiServer) map[string]any {
	t.Helper()
	payload := s.decodedPayload(t)
	if len(payload.Streams) == 0 || len(payload.Streams[0].Values) == 0 {
		t.Fatalf("no values in payload")
	}
	line := payload.Streams[0].Values[0][1]
	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		t.Fatalf("unmarshal line %q: %v", line, err)
	}
	return obj
}

func TestHookSendsJSONLineWithMessage(t *testing.T) {
	s := newLokiServer(t, 204)
	h, err := NewHook(Config{URL: s.srv.URL, BatchWait: time.Hour})
	if err != nil {
		t.Fatalf("new lokihook: %v", err)
	}
	if err := entryFor(t, h, logrus.InfoLevel, "hello world", nil); err != nil {
		t.Fatalf("fire: %v", err)
	}
	if err := h.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	obj := decodeLastLine(t, s)
	if obj["msg"] != "hello world" {
		t.Fatalf("want msg=hello world, got %v", obj["msg"])
	}
	if obj["level"] != "info" {
		t.Fatalf("want level=info, got %v", obj["level"])
	}
	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestHookIncludesFatalAndPanicLevels(t *testing.T) {
	s := newLokiServer(t, 204)
	h, err := NewHook(Config{URL: s.srv.URL, BatchWait: time.Hour})
	if err != nil {
		t.Fatalf("new lokihook: %v", err)
	}
	for _, l := range []logrus.Level{logrus.PanicLevel, logrus.FatalLevel} {
		if err := entryFor(t, h, l, "severe", nil); err != nil {
			t.Fatalf("fire %s: %v", l, err)
		}
	}
	if err := h.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	sent, _ := h.Stats()
	if sent != 2 {
		t.Fatalf("want sent=2, got %d", sent)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestHookRespectsMinLevel(t *testing.T) {
	s := newLokiServer(t, 204)
	h, err := NewHook(Config{URL: s.srv.URL, BatchWait: time.Hour, MinLevel: logrus.WarnLevel})
	if err != nil {
		t.Fatalf("new lokihook: %v", err)
	}
	defer func() { _ = h.Close() }()
	levels := h.Levels()
	for _, l := range []logrus.Level{logrus.DebugLevel, logrus.InfoLevel} {
		if containsLevel(levels, l) {
			t.Fatalf("level %s must not be handled", l)
		}
	}
	for _, l := range []logrus.Level{logrus.WarnLevel, logrus.ErrorLevel, logrus.FatalLevel, logrus.PanicLevel} {
		if !containsLevel(levels, l) {
			t.Fatalf("level %s must be handled", l)
		}
	}
	if err := entryFor(t, h, logrus.DebugLevel, "skipped", nil); err != nil {
		t.Fatalf("fire: %v", err)
	}
	if err := entryFor(t, h, logrus.WarnLevel, "kept", nil); err != nil {
		t.Fatalf("fire: %v", err)
	}
	if err := h.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	sent, _ := h.Stats()
	if sent != 1 {
		t.Fatalf("want sent=1, got %d", sent)
	}
}

func containsLevel(levels []logrus.Level, l logrus.Level) bool {
	for _, x := range levels {
		if x == l {
			return true
		}
	}
	return false
}

func TestHookPromotesLabelKeys(t *testing.T) {
	s := newLokiServer(t, 204)
	h, err := NewHook(Config{
		URL:       s.srv.URL,
		BatchWait: time.Hour,
		LabelKeys: []string{"request_id", "app"},
	})
	if err != nil {
		t.Fatalf("new lokihook: %v", err)
	}
	if err := entryFor(t, h, logrus.InfoLevel, "labeled", logrus.Fields{"request_id": "42", "user": "bob"}); err != nil {
		t.Fatalf("fire: %v", err)
	}
	if err := h.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	payload := s.decodedPayload(t)
	if len(payload.Streams) != 1 {
		t.Fatalf("want 1 stream, got %d", len(payload.Streams))
	}
	st := payload.Streams[0]
	if st.Stream["request_id"] != "42" {
		t.Fatalf("want request_id=42, got %q", st.Stream["request_id"])
	}
	obj := decodeLastLine(t, s)
	if obj["user"] != "bob" {
		t.Fatalf("want user=bob, got %v", obj["user"])
	}
	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestHookInjectsTraceContext(t *testing.T) {
	s := newLokiServer(t, 204)
	h, err := NewHook(Config{URL: s.srv.URL, BatchWait: time.Hour, InjectTrace: true})
	if err != nil {
		t.Fatalf("new lokihook: %v", err)
	}
	traceID, _ := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	spanID, _ := trace.SpanIDFromHex("0102030405060708")
	spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  spanID,
		Remote:  false,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), spanCtx)

	logger := logrus.New()
	entry := logrus.NewEntry(logger).WithContext(ctx)
	entry.Level = logrus.InfoLevel
	entry.Message = "traced"
	if err := h.Fire(entry); err != nil {
		t.Fatalf("fire: %v", err)
	}
	if err := h.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	obj := decodeLastLine(t, s)
	if obj["trace_id"] != "0102030405060708090a0b0c0d0e0f10" {
		t.Fatalf("unexpected trace_id: %v", obj["trace_id"])
	}
	if obj["span_id"] != "0102030405060708" {
		t.Fatalf("unexpected span_id: %v", obj["span_id"])
	}
	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestHookWithoutTraceContextKeepsLine(t *testing.T) {
	s := newLokiServer(t, 204)
	h, err := NewHook(Config{URL: s.srv.URL, BatchWait: time.Hour, InjectTrace: true})
	if err != nil {
		t.Fatalf("new lokihook: %v", err)
	}
	if err := entryFor(t, h, logrus.InfoLevel, "plain", nil); err != nil {
		t.Fatalf("fire: %v", err)
	}
	if err := h.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	obj := decodeLastLine(t, s)
	if _, ok := obj["trace_id"]; ok {
		t.Fatalf("trace_id must be absent: %v", obj)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestHookFormatterError(t *testing.T) {
	s := newLokiServer(t, 204)
	h, err := NewHook(Config{
		URL:       s.srv.URL,
		BatchWait: time.Hour,
		Formatter: failingFormatter{},
	})
	if err != nil {
		t.Fatalf("new lokihook: %v", err)
	}
	defer func() { _ = h.Close() }()
	if err := entryFor(t, h, logrus.InfoLevel, "boom", nil); err == nil {
		t.Fatal("want error from formatter")
	}
}

type failingFormatter struct{}

func (failingFormatter) Format(_ *logrus.Entry) ([]byte, error) {
	return nil, context.Canceled
}

func TestHookSamplesLowSeverityEntries(t *testing.T) {
	s := newLokiServer(t, 204)
	h, err := NewHook(Config{URL: s.srv.URL, BatchWait: time.Hour, SampleRate: 3})
	if err != nil {
		t.Fatalf("new lokihook: %v", err)
	}
	for i := 0; i < 6; i++ {
		if err := entryFor(t, h, logrus.InfoLevel, fmt.Sprintf("info-%d", i), nil); err != nil {
			t.Fatalf("fire: %v", err)
		}
	}
	if err := entryFor(t, h, logrus.WarnLevel, "warn-0", nil); err != nil {
		t.Fatalf("fire warn: %v", err)
	}
	if err := entryFor(t, h, logrus.ErrorLevel, "error-0", nil); err != nil {
		t.Fatalf("fire error: %v", err)
	}
	if err := h.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	payload := s.decodedPayload(t)
	total := 0
	for _, st := range payload.Streams {
		total += len(st.Values)
	}
	if total != 4 { // info-0 and info-3 kept by sampling, plus warn and error
		t.Fatalf("want 4 delivered entries, got %d", total)
	}
	cn := h.Counters()
	if cn.Sent != 4 || cn.Sampled != 4 || cn.Dropped != 0 {
		t.Fatalf("want sent=4 sampled=4 dropped=0, got %+v", cn)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestHookSampleRateOneKeepsEverything(t *testing.T) {
	s := newLokiServer(t, 204)
	h, err := NewHook(Config{URL: s.srv.URL, BatchWait: time.Hour, SampleRate: 1})
	if err != nil {
		t.Fatalf("new lokihook: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := entryFor(t, h, logrus.InfoLevel, fmt.Sprintf("info-%d", i), nil); err != nil {
			t.Fatalf("fire: %v", err)
		}
	}
	if err := h.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	payload := s.decodedPayload(t)
	total := 0
	for _, st := range payload.Streams {
		total += len(st.Values)
	}
	if total != 5 {
		t.Fatalf("want 5 delivered entries, got %d", total)
	}
	if cn := h.Counters(); cn.Sampled != 0 {
		t.Fatalf("want sampled=0, got %+v", cn)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
