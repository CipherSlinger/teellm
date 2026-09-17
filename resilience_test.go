package teellm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCircuitBreaker_StateTransitionsAndProbe(t *testing.T) {
	cb := NewCircuitBreaker(2, 50*time.Millisecond)
	if !cb.AllowRequest() {
		t.Fatalf("expected request allowed initially")
	}
	if state := cb.State(); state != StateClosed {
		t.Fatalf("expected state CLOSED, got %v", state)
	}

	// Trigger OPEN after 2 failures
	cb.RecordFailure()
	if !cb.AllowRequest() {
		t.Fatalf("expected request allowed before threshold reached")
	}
	cb.RecordFailure()

	if cb.AllowRequest() {
		t.Fatalf("expected circuit breaker OPEN after 2 failures")
	}
	if state := cb.State(); state != StateOpen {
		t.Fatalf("expected state OPEN, got %v", state)
	}

	// Wait for cooldown to transition to HALF-OPEN
	time.Sleep(60 * time.Millisecond)
	if state := cb.State(); state != StateHalfOpen {
		t.Fatalf("expected state HALF-OPEN after cooldown, got %v", state)
	}

	// In HALF-OPEN: only single probe allowed
	if !cb.AllowRequest() {
		t.Fatalf("expected single probe allowed in HALF-OPEN")
	}
	if cb.AllowRequest() {
		t.Fatalf("expected second concurrent probe rejected in HALF-OPEN")
	}

	// Probe success restores CLOSED
	cb.RecordSuccess()
	if state := cb.State(); state != StateClosed {
		t.Fatalf("expected state CLOSED after probe success, got %v", state)
	}
	if !cb.AllowRequest() {
		t.Fatalf("expected circuit breaker CLOSED allowing requests")
	}
}

func TestCircuitBreaker_ResetProbe(t *testing.T) {
	cb := NewCircuitBreaker(1, 40*time.Millisecond)
	cb.RecordFailure()

	if cb.AllowRequest() {
		t.Fatalf("expected circuit breaker to be OPEN")
	}

	time.Sleep(50 * time.Millisecond)

	// Acquire single probe in HALF-OPEN
	if !cb.AllowRequest() {
		t.Fatalf("expected first probe allowed in HALF-OPEN")
	}
	if cb.AllowRequest() {
		t.Fatalf("expected second probe blocked while probe active")
	}

	// Reset probe on non-service abort (e.g. client cancellation before dial)
	cb.ResetProbe()

	// Another probe should now be allowed
	if !cb.AllowRequest() {
		t.Fatalf("expected probe allowed again after ResetProbe()")
	}
}

func TestCircuitBreaker_HalfOpenFailure(t *testing.T) {
	cb := NewCircuitBreaker(1, 40*time.Millisecond)
	cb.RecordFailure()

	time.Sleep(50 * time.Millisecond)

	if !cb.AllowRequest() {
		t.Fatalf("expected probe allowed in HALF-OPEN")
	}

	// Failure in HALF-OPEN should immediately trip back to OPEN
	cb.RecordFailure()
	if cb.AllowRequest() {
		t.Fatalf("expected circuit breaker OPEN after probe failure")
	}
	if state := cb.State(); state != StateOpen {
		t.Fatalf("expected state OPEN, got %v", state)
	}
}

func TestCircuitBreaker_ConcurrentAccess(t *testing.T) {
	cb := NewCircuitBreaker(5, 50*time.Millisecond)
	var wg sync.WaitGroup
	var allowedCount int64

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if cb.AllowRequest() {
				atomic.AddInt64(&allowedCount, 1)
			}
		}()
	}
	wg.Wait()

	if allowedCount == 0 {
		t.Fatalf("expected at least some requests allowed concurrently")
	}
}

func TestExecuteWithRetryContext_SuccessAfterRetry(t *testing.T) {
	ctx := context.Background()
	attempts := 0

	err := ExecuteWithRetryContext(ctx, 3, 10*time.Millisecond, 100*time.Millisecond, func(attempt int) error {
		attempts++
		if attempt < 2 {
			return fmt.Errorf("%w: temporary failure on attempt %d", ErrRetryable, attempt)
		}
		return nil
	})

	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if attempts != 3 { // attempt 0, 1, 2
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

func TestExecuteWithRetryContext_NonRetryableError(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	nonRetryableErr := errors.New("permanent fatal error")

	err := ExecuteWithRetryContext(ctx, 3, 10*time.Millisecond, 100*time.Millisecond, func(attempt int) error {
		attempts++
		return nonRetryableErr
	})

	if !errors.Is(err, nonRetryableErr) {
		t.Fatalf("expected %v, got %v", nonRetryableErr, err)
	}
	if attempts != 1 {
		t.Fatalf("expected exactly 1 attempt for non-retryable error, got %d", attempts)
	}
}

func TestExecuteWithRetryContext_MaxRetriesExceeded(t *testing.T) {
	ctx := context.Background()
	attempts := 0

	err := ExecuteWithRetryContext(ctx, 2, 5*time.Millisecond, 20*time.Millisecond, func(attempt int) error {
		attempts++
		return fmt.Errorf("%w: failure %d", ErrRetryable, attempt)
	})

	if err == nil {
		t.Fatalf("expected error after max retries exceeded")
	}
	if !errors.Is(err, ErrRetryable) {
		t.Fatalf("expected ErrRetryable, got: %v", err)
	}
	if attempts != 3 { // attempt 0, 1, 2
		t.Fatalf("expected 3 attempts (1 initial + 2 retries), got %d", attempts)
	}
}

func TestExecuteWithRetryContext_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0

	err := ExecuteWithRetryContext(ctx, 5, 50*time.Millisecond, 200*time.Millisecond, func(attempt int) error {
		attempts++
		if attempt == 1 {
			cancel()
		}
		return fmt.Errorf("%w: transient error", ErrRetryable)
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

func TestExecuteWithRetryContext_JitterBoundaries(t *testing.T) {
	baseDelay := 20 * time.Millisecond
	maxDelay := 100 * time.Millisecond

	start := time.Now()
	_ = ExecuteWithRetryContext(context.Background(), 1, baseDelay, maxDelay, func(attempt int) error {
		if attempt == 0 {
			return ErrRetryable
		}
		return nil
	})
	elapsed := time.Since(start)

	// Base delay is 20ms, with 20% jitter (0.8 - 1.2), delay should be between 16ms and 30ms (allowing small scheduler drift)
	if elapsed < 14*time.Millisecond {
		t.Fatalf("backoff too short: %v (expected >= 16ms with jitter)", elapsed)
	}
}

func TestValidateEndpoint_StrictHTTPS(t *testing.T) {
	// Rejects http://
	if err := ValidateEndpoint("http://127.0.0.1:8443", nil); err == nil {
		t.Errorf("expected error for plain http://")
	}
	// Rejects unix://
	if err := ValidateEndpoint("unix:///var/run/llm.sock", nil); err == nil {
		t.Errorf("expected error for unix://")
	}
	// Rejects ftp://
	if err := ValidateEndpoint("ftp://127.0.0.1:21", nil); err == nil {
		t.Errorf("expected error for ftp://")
	}
	// Rejects empty
	if err := ValidateEndpoint("", nil); err == nil {
		t.Errorf("expected error for empty endpoint")
	}
}

func TestValidateEndpoint_RestrictedAddresses(t *testing.T) {
	blockedEndpoints := []string{
		"https://169.254.169.254:8443",
		"https://169.254.169.254/latest/meta-data",
		"https://169.254.1.1:8443", // IPv4 Link-local
		"https://0.0.0.0:8443",
		"https://0.0.0.0",
		"https://[::]:8443",
		"https://[fe80::1]:8443", // IPv6 Link-local
	}

	for _, ep := range blockedEndpoints {
		if err := ValidateEndpoint(ep, nil); err == nil {
			t.Errorf("expected endpoint %q to be blocked", ep)
		}
	}
}

func TestValidateEndpoint_AllowedHosts(t *testing.T) {
	allowed := []string{"llm-service.trusted.cluster", "localhost", "127.0.0.1"}

	// Valid endpoints within whitelist
	valid := []string{
		"https://llm-service.trusted.cluster:8443",
		"https://llm-service.trusted.cluster",
		"https://localhost:8443/v1/verify",
		"https://127.0.0.1:9443",
	}
	for _, ep := range valid {
		if err := ValidateEndpoint(ep, allowed); err != nil {
			t.Errorf("expected %q to be allowed, got: %v", ep, err)
		}
	}

	// Blocked endpoints not in whitelist
	unauthorized := []string{
		"https://evil.attacker.com:8443",
		"https://10.0.0.1:8443",
	}
	for _, ep := range unauthorized {
		if err := ValidateEndpoint(ep, allowed); err == nil {
			t.Errorf("expected %q to be rejected by allowedHosts whitelist", ep)
		}
	}
}

func TestValidateEndpoint_WithoutAllowedHosts(t *testing.T) {
	// If allowedHosts is nil/empty, any valid external or internal HTTPS endpoint not in the restricted address list is accepted
	if err := ValidateEndpoint("https://any-valid-domain.com:8443", nil); err != nil {
		t.Errorf("expected valid domain to pass when allowedHosts is nil, got: %v", err)
	}
}
