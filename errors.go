package hook

import "errors"

var (
	// ErrQueueFull is returned by Send when the internal queue is full and
	// the overflow policy could not make room for the entry.
	ErrQueueFull = errors.New("loki: queue is full")

	// ErrClientClosed is returned by Send after the client has been closed.
	ErrClientClosed = errors.New("loki: client is closed")
)
