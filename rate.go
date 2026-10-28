package hook

import (
	"sync"
	"time"
)

// tokenBucket is a minimal token-bucket rate limiter with a burst equal to
// the rate, so short bursts within one second worth of logs are allowed.
type tokenBucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newTokenBucket(perSecond float64) *tokenBucket {
	return &tokenBucket{rate: perSecond, burst: perSecond, tokens: perSecond, last: time.Now()}
}

// allow reports whether one token is available at time now.
func (b *tokenBucket) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = min(b.burst, b.tokens+elapsed*b.rate)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}
