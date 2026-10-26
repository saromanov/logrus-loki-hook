package hook

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// logEntry is a single record queued for delivery to Loki.
type logEntry struct {
	ts     int64
	line   string
	labels map[string]string
}

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
	cfg       Config
	http      *http.Client
	queue     chan logEntry
	flushCh   chan chan struct{}
	done      chan struct{}
	wg        sync.WaitGroup
	closed    atomic.Bool
	sent      atomic.Int64
	dropped   atomic.Int64
	errLogger *log.Logger
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
	}
	c.wg.Add(1)
	go c.run()
	return c, nil
}

// Send queues a log entry for delivery. The returned error only reflects
// local delivery problems (closed client, queue overflow); failed pushes are
// reported to stderr and counted in Stats.
func (c *Client) Send(ts int64, line string, labels map[string]string) error {
	if c.closed.Load() {
		return ErrClientClosed
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
			c.dropped.Add(1)
			return ErrQueueFull
		}
	case DropOldest:
		select {
		case c.queue <- entry:
			return nil
		default:
			select {
			case <-c.queue:
				c.dropped.Add(1)
			default:
			}
			select {
			case c.queue <- entry:
				return nil
			default:
				c.dropped.Add(1)
				return ErrQueueFull
			}
		}
	default: // DropNewest
		select {
		case c.queue <- entry:
			return nil
		default:
			c.dropped.Add(1)
			return ErrQueueFull
		}
	}
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
				c.push(batch)
			}
			return
		case ch := <-c.flushCh:
			c.drainQueue(&batch)
			if len(batch) > 0 {
				c.push(batch)
				batch = batch[:0]
			}
			close(ch)
		case e := <-c.queue:
			batch = append(batch, e)
			for len(batch) >= c.cfg.BatchSize {
				c.push(batch[:c.cfg.BatchSize])
				batch = batch[c.cfg.BatchSize:]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				c.push(batch)
				batch = batch[:0]
			}
		}
	}
}

// drainQueue moves every queued entry into the batch. It must only be called
// from the batcher goroutine.
func (c *Client) drainQueue(batch *[]logEntry) {
	for {
		select {
		case e := <-c.queue:
			*batch = append(*batch, e)
			if len(*batch) >= c.cfg.BatchSize {
				c.push(*batch)
				*batch = (*batch)[:0]
			}
		default:
			return
		}
	}
}

func (c *Client) push(entries []logEntry) {
	if len(entries) == 0 {
		return
	}
	payload, err := c.encode(entries)
	if err != nil {
		c.dropped.Add(int64(len(entries)))
		c.errLogger.Printf("failed to encode %d entries: %v", len(entries), err)
		return
	}
	if err := c.send(payload); err != nil {
		c.dropped.Add(int64(len(entries)))
		c.errLogger.Printf("failed to push %d entries to Loki: %v", len(entries), err)
		return
	}
	c.sent.Add(int64(len(entries)))
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

// send performs the HTTP request, retrying network errors and retryable
// responses (429, 5xx) with exponential backoff.
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
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(backoff)
			backoff *= 2
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
		data, _ := io.ReadAll(resp.Body)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		lastErr = fmt.Errorf("loki: unexpected status %d: %s", resp.StatusCode, truncate(data))
		if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
			return lastErr
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
