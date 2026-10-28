package hook

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// diskEntry is the JSON representation of a buffered log line. The buffer
// file is JSON lines: one entry per line, newline-terminated, so a crash can
// only tear the final line and everything before it stays parseable.
type diskEntry struct {
	TS     int64             `json:"ts"`
	Line   string            `json:"line"`
	Labels map[string]string `json:"labels,omitempty"`
}

// diskBuffer is an append-only, segmented on-disk overflow buffer. Entries
// spilled here survive SIGKILL (every line reaches the OS with a Flush) and
// are drained into Loki on the next startup, oldest segment first.
//
// Files: <path> is the active segment, <path>.1 ... <path>.N are rotated
// segments ordered from newest to oldest.
type diskBuffer struct {
	mu          sync.Mutex
	path        string
	maxSize     int64
	maxSegments int
	file        *os.File
	w           *bufio.Writer
	size        int64
	errLogger   *log.Logger
}

func newDiskBuffer(path string, maxSize int64, maxSegments int, errLogger *log.Logger) (*diskBuffer, error) {
	if maxSize <= 0 {
		maxSize = 64 << 20
	}
	if maxSegments < 1 {
		maxSegments = 1
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("loki: create buffer dir: %w", err)
		}
	}
	b := &diskBuffer{path: path, maxSize: maxSize, maxSegments: maxSegments, errLogger: errLogger}
	// A crash mid-rewrite can leave a stale temp file behind.
	_ = os.Remove(path + ".tmp")
	return b, nil
}

// spill appends one entry, rotating the active segment when it would exceed
// the size cap. The line is flushed to the OS before returning, so a
// SIGKILL cannot lose it.
func (b *diskBuffer) spill(e logEntry) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ensureOpen(); err != nil {
		return err
	}
	raw, err := json.Marshal(diskEntry{TS: e.ts, Line: e.line, Labels: e.labels})
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if b.size+int64(len(raw)) > b.maxSize {
		if err := b.rotateLocked(); err != nil {
			return err
		}
		if err := b.ensureOpen(); err != nil {
			return err
		}
	}
	n, err := b.w.Write(raw)
	b.size += int64(n)
	if err != nil {
		return err
	}
	return b.w.Flush()
}

// close flushes and closes the active segment. The on-disk backlog is kept:
// it is drained on the next startup.
func (b *diskBuffer) close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.w != nil {
		if err := b.w.Flush(); err != nil {
			_ = b.file.Close()
			b.w, b.file = nil, nil
			return err
		}
	}
	if b.file != nil {
		err := b.file.Close()
		b.w, b.file = nil, nil
		return err
	}
	return nil
}

func (b *diskBuffer) ensureOpen() error {
	if b.w != nil {
		return nil
	}
	f, err := os.OpenFile(b.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	b.file = f
	b.w = bufio.NewWriter(f)
	b.size = st.Size()
	return nil
}

// rotateLocked closes the active segment and shifts rotated segments by one,
// deleting the oldest. A crash between Remove and Rename can lose the target
// of a single rename, never the entries already flushed.
func (b *diskBuffer) rotateLocked() error {
	if err := b.closeLocked(); err != nil {
		return err
	}
	for i := b.maxSegments - 1; i >= 1; i-- {
		src := b.segmentPath(i)
		dst := b.segmentPath(i + 1)
		_ = os.Remove(dst)
		if _, err := os.Stat(src); err == nil {
			if err := os.Rename(src, dst); err != nil {
				return err
			}
		}
	}
	_ = os.Remove(b.segmentPath(1))
	if _, err := os.Stat(b.path); err == nil {
		if err := os.Rename(b.path, b.segmentPath(1)); err != nil {
			return err
		}
	}
	b.size = 0
	return nil
}

func (b *diskBuffer) closeLocked() error {
	if b.w != nil {
		if err := b.w.Flush(); err != nil {
			return err
		}
	}
	if b.file != nil {
		if err := b.file.Close(); err != nil {
			return err
		}
	}
	b.w, b.file = nil, nil
	return nil
}

func (b *diskBuffer) segmentPath(i int) string {
	return fmt.Sprintf("%s.%d", b.path, i)
}

// segments lists the existing buffer files oldest first: rotated segments
// from .maxSegments down to .1, then the active one.
func (b *diskBuffer) segments() []string {
	var out []string
	for i := b.maxSegments; i >= 1; i-- {
		p := b.segmentPath(i)
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	if _, err := os.Stat(b.path); err == nil {
		out = append(out, b.path)
	}
	return out
}

// drain pushes every buffered entry through the client, oldest first. On the
// first failed push the undelivered tail of the current segment is rewritten
// atomically (temp file + rename) and draining stops: later segments keep
// their order for the next startup.
func (b *diskBuffer) drain(c *Client) error {
	var firstErr error
	for _, seg := range b.segments() {
		if err := b.drainSegment(c, seg); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			break
		}
	}
	return firstErr
}

// drainSegment delivers one segment in chunks. Segments hold raw JSON lines;
// raw bytes are preserved on failure so nothing is parsed twice into a
// different form. A partially delivered chunk (split halves) may duplicate
// entries on re-spill: the buffer is at-least-once, not exactly-once.
func (b *diskBuffer) drainSegment(c *Client, seg string) error {
	f, err := os.Open(seg)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		var chunk []logEntry
		var chunkRaw [][]byte
		for len(chunk) < c.cfg.BatchSize {
			raw, rerr := r.ReadBytes('\n')
			if len(raw) > 0 {
				chunkRaw = append(chunkRaw, raw)
				if e, ok := parseDiskEntry(raw); ok {
					chunk = append(chunk, e)
				} else {
					c.drop(1, dropCorrupt)
					c.errLogger.Printf("loki: skipping corrupted buffer entry in %s", seg)
				}
			}
			if rerr != nil {
				break
			}
		}
		if len(chunkRaw) == 0 {
			break
		}
		if len(chunk) > 0 {
			if err := c.deliver(chunk); err != nil {
				if werr := b.rewriteSegment(seg, chunkRaw, r); werr != nil {
					c.errLogger.Printf("loki: failed to preserve buffer tail of %s: %v", seg, werr)
				}
				return err
			}
		}
		if len(chunkRaw) < c.cfg.BatchSize {
			break // end of segment reached
		}
	}
	return os.Remove(seg)
}

// rewriteSegment atomically replaces seg with chunkRaw followed by the
// remaining bytes of r.
func (b *diskBuffer) rewriteSegment(seg string, chunkRaw [][]byte, rest io.Reader) error {
	tmp := seg + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	var werr error
	for _, raw := range chunkRaw {
		if _, werr = w.Write(raw); werr != nil {
			break
		}
	}
	if werr == nil && rest != nil {
		_, werr = io.Copy(w, rest)
	}
	if werr == nil {
		werr = w.Flush()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return werr
	}
	return os.Rename(tmp, seg)
}

func parseDiskEntry(raw []byte) (logEntry, bool) {
	raw = bytes.TrimRight(raw, "\n")
	if len(bytes.TrimSpace(raw)) == 0 {
		return logEntry{}, false
	}
	var de diskEntry
	if err := json.Unmarshal(raw, &de); err != nil {
		return logEntry{}, false
	}
	return logEntry{ts: de.TS, line: de.Line, labels: de.Labels}, true
}
