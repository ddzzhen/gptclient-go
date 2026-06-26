package sentinel

import (
	"errors"
	"math/rand"
	"time"
)

type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

func defaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 3, BaseDelay: 800 * time.Millisecond, MaxDelay: 8 * time.Second}
}

func (p RetryPolicy) normalize() RetryPolicy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 1
	}
	if p.BaseDelay <= 0 {
		p.BaseDelay = 800 * time.Millisecond
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = 8 * time.Second
	}
	return p
}

func (c *Client) doWithRetry(label string, fn func() error) error {
	policy := defaultRetryPolicy().normalize()
	var last error
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		last = fn()
		if last == nil || !shouldRetryUpstream(last) || attempt == policy.MaxAttempts {
			return last
		}
		delay := retryDelay(last, attempt, policy)
		c.logf("[retry] %s attempt=%d err=%v sleep=%s", label, attempt, last, delay)
		time.Sleep(delay)
	}
	return last
}

func shouldRetryUpstream(err error) bool {
	var ue *UpstreamError
	if !errors.As(err, &ue) {
		return false
	}
	return ue.Retryable
}

func retryDelay(err error, attempt int, policy RetryPolicy) time.Duration {
	var ue *UpstreamError
	if errors.As(err, &ue) && ue.RetryAfter > 0 {
		if ue.RetryAfter < policy.MaxDelay {
			return ue.RetryAfter
		}
		return policy.MaxDelay
	}
	d := policy.BaseDelay << (attempt - 1)
	if d > policy.MaxDelay {
		d = policy.MaxDelay
	}
	//nolint:gosec // retry jitter only
	jitter := time.Duration(rand.Int63n(int64(d / 2)))
	return d/2 + jitter
}
