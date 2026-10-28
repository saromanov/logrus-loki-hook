package hook

// Counters reports delivery counters, including entries skipped on purpose
// by sampling or rate limiting.
type Counters struct {
	// Sent is the number of entries delivered to Loki.
	Sent int64

	// Dropped is the number of entries lost to queue overflow, push
	// failures or Loki rejections.
	Dropped int64

	// Sampled is the number of low-severity entries skipped by SampleRate.
	Sampled int64

	// RateLimited is the number of entries skipped by MaxLogsPerSecond.
	RateLimited int64

	// Spilled is the number of entries written to the durable disk buffer
	// instead of being dropped.
	Spilled int64
}
