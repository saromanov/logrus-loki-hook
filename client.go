package hook

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// logEntry is a single record queued for delivery to Loki.
type logEntry struct {
	ts     int64
	line   string
	labels map[string]string
}

// dropReason classifies why entries were dropped without reaching Loki.
type dropReason int

const (
	dropQueueFull dropReason = iota
	dropEncode
	dropPush
	dropRateLimited
	dropRejected
	dropReasonCount
)

type pushStream struct {
	Stream map[string]string `json:"stream"`
	Values [][]string        `json:"values"`
}

type pushPayload struct {
	Streams []pushStream `json:"streams"`
}

// Client batches log entries and pushes them to Loki over the native HTTP API.
// It is safe for concurrent use.
type Client struct {
	cfg        Config
	http       *http.Client
	queue      chan logEntry
	flushCh    chan chan struct{}
	done       chan struct{}
	wg         sync.WaitGroup
	closed     atomic.Bool
	sent       atomic.Int64
	dropped    atomic.Int64
	dropCounts [dropReasonCount]atomic.Int64
	errLogger  *log.Logger

	// adaptive is the current effective batch size. It shrinks on repeated
	// rate-limit responses and recovers on success. Guarded by the batcher
	// goroutine only.
	adaptive int
}

// NewClient creates a Loki client and starts the background batcher.
// Call Close on shutdown to flush every queued entry.
func NewClient(cfg Config) (*Client, error) {
	cfg.setDefaults()
	if !strings.HasPrefix(cfg.URL, "http://") && !strings.HasPrefix(cfg.URL, "https://") {
		return nil, fmt.Errorf("loki: invalid URL %q", cfg.URL)
	}
	c := &Client{
		cfg:       cfg,
		http:      &http.Client{Timeout: cfg.Timeout, Transport: &http.Transport{TLSClientConfig: cfg.TLSConfig, ForceAttemptHTTP2: true}},
		queue:     make(chan logEntry, cfg.QueueDepth),
		flushCh:   make(chan chan struct{}),
		done:      make(chan struct{}),
		wg:        sync.WaitGroup{},
		errLogger: log.New(os.Stderr, "logrus-loki-hook: ", log.LstdFlags),
		adaptive:  cfg.BatchSize,
	}
	c.wg.Add(1)
	go c.run()
	return c, nil
}

// Send queues a log entry for delivery. The returned error only reflects
// local delivery problems (closed client, queue overflow); failed pushes are
// reported to stderr and counted in Stats. Lines longer than MaxLineSize are
// truncated instead of being rejected by Loki.
func (c *Client) Send(ts int64, line string, labels map[string]string) error {
	if c.closed.Load() {
		return ErrClientClosed
	}
	if c.cfg.MaxLineSize >= 0 {
		line = truncateLine(line, c.cfg.MaxLineSize)
	}
	entry := logEntry{ts: ts, line: line, labels: labels}
	switch c.cfg.Overflow {
	case Block:
		select {
		case c.queue <- entry:
			return nil
		default:
		}
		timer := time.NewTimer(c.cfg.BlockTimeout)
		defer timer.Stop()
		select {
		case c.queue <- entry:
			return nil
		case <-timer.C:
			c.drop(1, dropQueueFull)
			return ErrQueueFull
		}
	case DropOldest:
		select {
		case c.queue <- entry:
			return nil
		default:
			select {
			case <-c.queue:
				c.drop(1, dropQueueFull)
			default:
			}
			select {
			case c.queue <- entry:
				return nil
			default:
				c.drop(1, dropQueueFull)
				return ErrQueueFull
			}
		}
	default: // DropNewest
		select {
		case c.queue <- entry:
			return nil
		default:
			c.drop(1, dropQueueFull)
			return ErrQueueFull
		}
	}
}

// truncateMarker is appended to lines shortened by MaxLineSize.
const truncateMarker = " [truncated]"

// truncateLine shortens s to at most max bytes without splitting a UTF-8
// sequence, appending a marker so the loss stays visible in Loki.
func truncateLine(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	keep := max - len(truncateMarker)
	if keep <= 0 {
		return truncateMarker[:max]
	}
	cut := keep
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + truncateMarker
}

// Flush pushes all queued entries immediately, bypassing the batch timer.
// It blocks until the background batcher has delivered everything queued at
// the moment of the call.
func (c *Client) Flush() error {
	ch := make(chan struct{})
	select {
	case c.flushCh <- ch:
		<-ch
		return nil
	case <-c.done:
		return ErrClientClosed
	}
}

// Close stops the client, drains the queue and flushes every pending entry.
func (c *Client) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(c.done)
	c.wg.Wait()
	return nil
}

// Stats returns the number of successfully sent and dropped entries.
func (c *Client) Stats() (sent, dropped int64) {
	return c.sent.Load(), c.dropped.Load()
}

// drop accounts for n entries that never reached Loki, by reason.
func (c *Client) drop(n int, r dropReason) {
	c.dropped.Add(int64(n))
	c.dropCounts[r].Add(int64(n))
}

func (c *Client) run() {
	defer c.wg.Done()
	ticker := time.NewTicker(c.cfg.BatchWait)
	defer ticker.Stop()
	batch := make([]logEntry, 0, c.cfg.BatchSize)
	for {
		select {
		case <-c.done:
			c.drainQueue(&batch)
			if len(batch) > 0 {
				_ = c.push(batch)
			}
			return
		case ch := <-c.flushCh:
			c.drainQueue(&batch)
			if len(batch) > 0 {
				_ = c.push(batch)
				batch = batch[:0]
			}
			close(ch)
		case e := <-c.queue:
			batch = append(batch, e)
			for len(batch) >= c.adaptive {
				chunk := batch[:c.adaptive]
				err := c.push(chunk)
				c.tuneAdaptive(err)
				batch = batch[len(chunk):]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				err := c.push(batch)
				c.tuneAdaptive(err)
				batch = batch[:0]
			}
		}
	}
}

// tuneAdaptive adjusts the effective batch size: halve it when Loki keeps
// rate-limiting pushes, grow back geometrically after a success.
func (c *Client) tuneAdaptive(err error) {
	if isRateLimited(err) {
		c.adaptive = max(1, c.adaptive/2)
		return
	}
	if err == nil {
		c.adaptive = min(c.cfg.BatchSize, max(1, c.adaptive*2))
	}
}

// drainQueue moves every queued entry into the batch. It must only be called
// from the batcher goroutine.
func (c *Client) drainQueue(batch *[]logEntry) {
	for {
		select {
		case e := <-c.queue:
			*batch = append(*batch, e)
			if len(*batch) >= c.adaptive {
				chunk := (*batch)[:c.adaptive]
				err := c.push(chunk)
				c.tuneAdaptive(err)
				*batch = (*batch)[len(chunk):]
			}
		default:
			return
		}
	}
}

// push delivers a batch, dropping it on unrecoverable failure. Oversized or
// rejected batches are split in half until the failing entries are isolated.
func (c *Client) push(entries []logEntry) error {
	return c.pushBatch(entries)
}

func (c *Client) pushBatch(entries []logEntry) error {
	if len(entries) == 0 {
		return nil
	}
	payload, err := c.encode(entries)
	if err != nil {
		c.drop(len(entries), dropEncode)
		c.errLogger.Printf("failed to encode %d entries: %v", len(entries), err)
		return err
	}
	if err := c.send(payload); err != nil {
		var se *httpStatusError
		if errors.As(err, &se) && se.code == http.StatusBadRequest && len(entries) > 1 {
			mid := len(entries) / 2
			err1 := c.pushBatch(entries[:mid])
			err2 := c.pushBatch(entries[mid:])
			if err1 != nil {
				return err1
			}
			return err2
		}
		reason := dropPush
		if se != nil {
			if isRateLimitedCode(se.code) {
				reason = dropRateLimited
			} else if se.code == http.StatusBadRequest || se.code == http.StatusRequestEntityTooLarge {
				reason = dropRejected
			}
		}
		c.drop(len(entries), reason)
		c.errLogger.Printf("failed to push %d entries to Loki: %v", len(entries), err)
		return err
	}
	c.sent.Add(int64(len(entries)))
	return nil
}

// encode groups entries by label set and renders the Loki push payload.
func (c *Client) encode(entries []logEntry) ([]byte, error) {
	index := make(map[string]*pushStream, len(entries))
	for _, e := range entries {
		labels := mergeLabels(c.cfg.StaticLabels, e.labels)
		key := labelsKey(labels)
		s, ok := index[key]
		if !ok {
			s = &pushStream{Stream: labels}
			index[key] = s
		}
		s.Values = append(s.Values, []string{fmt.Sprint(e.ts), e.line})
	}
	payload := pushPayload{Streams: make([]pushStream, 0, len(index))}
	for _, s := range index {
		payload.Streams = append(payload.Streams, *s)
	}
	return json.Marshal(payload)
}

func mergeLabels(static map[string]string, dynamic map[string]string) map[string]string {
	if len(dynamic) == 0 {
		return static
	}
	out := make(map[string]string, len(static)+len(dynamic))
	for k, v := range static {
		out[k] = v
	}
	for k, v := range dynamic {
		out[k] = v
	}
	return out
}

// labelsKey builds a stable key for a label set.
func labelsKey(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
		b.WriteByte(0)
	}
	return b.String()
}

// httpStatusError reports a non-2xx Loki response.
type httpStatusError struct {
	code int
	body string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("loki: unexpected status %d: %s", e.code, e.body)
}

// isRateLimitedCode reports whether the status means Loki throttled the push
// (429) or redirected the tenant (421, Grafana Cloud).
func isRateLimitedCode(code int) bool {
	return code == http.StatusTooManyRequests || code == http.StatusMisdirectedRequest
}

func isRateLimited(err error) bool {
	var se *httpStatusError
	return errors.As(err, &se) && isRateLimitedCode(se.code)
}

// parseRetryAfter understands both forms of the Retry-After header:
// delay-seconds and HTTP-date.
func parseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		d := t.Sub(now)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

// send performs the HTTP request, retrying network errors and retryable
// responses (429, 421, 5xx). Retry-After headers are honored in place of the
// exponential backoff, capped at MaxRetryWait.
func (c *Client) send(payload []byte) error {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		return fmt.Errorf("loki: gzip: %w", err)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("loki: gzip close: %w", err)
	}
	body := buf.Bytes()

	attempts := c.cfg.Retries + 1
	backoff := c.cfg.RetryBackoff
	retryWait := time.Duration(0)
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			if retryWait > 0 {
				time.Sleep(retryWait)
				retryWait = 0
			} else {
				time.Sleep(backoff)
				backoff *= 2
			}
		}
		req, err := http.NewRequest(http.MethodPost, c.cfg.URL, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("loki: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Content-Encoding", "gzip")
		if c.cfg.OrgID != "" {
			req.Header.Set("X-Scope-OrgID", c.cfg.OrgID)
		}
		if c.cfg.BasicAuth != nil {
			req.SetBasicAuth(c.cfg.BasicAuth.Username, c.cfg.BasicAuth.Password)
		} else if c.cfg.BearerToken != "" {
			req.Header.Set("Authorization", "Bearer "+c.cfg.BearerToken)
		}
		for k, v := range c.cfg.Headers {
			req.Header.Set(k, v)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		se := &httpStatusError{code: resp.StatusCode, body: truncate(data)}
		lastErr = se
		retryable := isRateLimitedCode(resp.StatusCode) || resp.StatusCode >= 500
		if !retryable {
			return se
		}
		if ra, ok := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()); ok {
			retryWait = min(ra, c.cfg.MaxRetryWait)
		}
	}
	return lastErr
}

func truncate(data []byte) string {
	const max = 256
	if len(data) <= max {
		return string(data)
	}
	return string(data[:max]) + "..."
}
