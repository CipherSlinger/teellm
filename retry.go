package teellm

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

// ErrRetryable indicates an error that is eligible for retry.
var ErrRetryable = errors.New("retryable error")

// ExecuteWithRetryContext executes fn with 20% jittered exponential backoff for retryable errors.
//
// The retry loop retries only when the returned error is or wraps ErrRetryable.
// If fn returns nil, execution succeeds and returns nil.
// If fn returns an error that does not wrap ErrRetryable, the loop aborts immediately and returns that error.
// If ctx is cancelled, execution aborts and returns ctx.Err().
func ExecuteWithRetryContext(
	ctx context.Context,
	maxRetries int,
	baseDelay, maxDelay time.Duration,
	fn func(attempt int) error,
) error {
	if maxRetries < 0 {
		maxRetries = 0
	}
	if baseDelay <= 0 {
		baseDelay = 100 * time.Millisecond
	}
	if maxDelay <= 0 {
		maxDelay = 5 * time.Second
	}
	if baseDelay > maxDelay {
		baseDelay = maxDelay
	}

	delay := baseDelay
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}

		lastErr = fn(attempt)
		if lastErr == nil {
			return nil
		}

		// Abort immediately if non-retryable error or context error
		if !errors.Is(lastErr, ErrRetryable) || (ctx != nil && ctx.Err() != nil && errors.Is(lastErr, ctx.Err())) {
			return lastErr
		}

		// If attempts exhausted, break and return lastErr
		if attempt == maxRetries {
			break
		}

		// Calculate 20% random jitter: 0.8x to 1.2x delay
		jitterFactor := 0.8 + 0.4*rand.Float64()
		sleepDuration := time.Duration(float64(delay) * jitterFactor)
		if sleepDuration > maxDelay {
			sleepDuration = maxDelay
		}

		if ctx != nil {
			timer := time.NewTimer(sleepDuration)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		} else {
			time.Sleep(sleepDuration)
		}

		// Exponential backoff
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}

	return lastErr
}
