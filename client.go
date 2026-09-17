package teellm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"taa/teetls"
)

var (
	// ErrPayloadTooLarge is returned when the response payload exceeds 1MB.
	ErrPayloadTooLarge = errors.New("response payload exceeds 1MB limit")

	// ErrInvalidStatus is returned when the server returns an unexpected non-success HTTP status.
	ErrInvalidStatus = errors.New("unexpected response status")
)

// MaxResponsePayloadBytes defines the maximum permissible size of a response payload (1MB).
const MaxResponsePayloadBytes = 1 << 20

// Config configures the TEE-LLM client.
type Config struct {
	Endpoint                        string
	Timeout                         time.Duration
	AllowedHosts                    []string
	TEETLS                          *teetls.Config
	CircuitBreakerFailureThreshold  int
	CircuitBreakerCooldown          time.Duration
	MaxRetries                      int
	RetryBaseDelay                  time.Duration
	RetryMaxDelay                   time.Duration
}

// Client defines the interface for communicating with the TEE-LLM inference service.
type Client interface {
	VerifyFinding(ctx context.Context, req *RequestEnvelope) (*ResponseEnvelope, error)
	HealthCheck(ctx context.Context) error
	Close() error
}

type client struct {
	cfg        Config
	httpClient *http.Client
	cb         *CircuitBreaker
	endpoint   string
}

// NewClient creates and validates a new TEE-LLM Client instance.
func NewClient(cfg Config) (Client, error) {
	if err := ValidateEndpoint(cfg.Endpoint, cfg.AllowedHosts); err != nil {
		return nil, fmt.Errorf("invalid endpoint: %w", err)
	}

	if cfg.CircuitBreakerFailureThreshold <= 0 {
		cfg.CircuitBreakerFailureThreshold = 3
	}
	if cfg.CircuitBreakerCooldown <= 0 {
		cfg.CircuitBreakerCooldown = 30 * time.Second
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}
	if cfg.RetryBaseDelay <= 0 {
		cfg.RetryBaseDelay = 100 * time.Millisecond
	}
	if cfg.RetryMaxDelay <= 0 {
		cfg.RetryMaxDelay = 2 * time.Second
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}

	cb := NewCircuitBreaker(cfg.CircuitBreakerFailureThreshold, cfg.CircuitBreakerCooldown)

	var httpClient *http.Client
	if cfg.TEETLS != nil {
		httpClient = teetls.NewHTTPClient(cfg.TEETLS)
		httpClient.Timeout = cfg.Timeout
	} else {
		httpClient = &http.Client{
			Timeout: cfg.Timeout,
		}
	}

	endpoint := strings.TrimRight(cfg.Endpoint, "/")

	return &client{
		cfg:        cfg,
		httpClient: httpClient,
		cb:         cb,
		endpoint:   endpoint,
	}, nil
}

// isTransientStatus reports whether an HTTP status code represents a transient server failure.
func isTransientStatus(code int) bool {
	return code == http.StatusTooManyRequests ||
		code == http.StatusInternalServerError ||
		code == http.StatusBadGateway ||
		code == http.StatusServiceUnavailable ||
		code == http.StatusGatewayTimeout
}

// VerifyFinding sends an audit finding verification request to the TEE-LLM server.
func (c *client) VerifyFinding(ctx context.Context, req *RequestEnvelope) (*ResponseEnvelope, error) {
	if !c.cb.AllowRequest() {
		return nil, ErrCircuitOpen
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request envelope: %w", err)
	}

	var respEnvelope *ResponseEnvelope
	err = ExecuteWithRetryContext(ctx, c.cfg.MaxRetries, c.cfg.RetryBaseDelay, c.cfg.RetryMaxDelay, func(attempt int) error {
		httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/v1/verify", bytes.NewReader(reqBytes))
		if reqErr != nil {
			return reqErr
		}
		httpReq.Header.Set("Content-Type", "application/json")

		resp, doErr := c.httpClient.Do(httpReq)
		if doErr != nil {
			if ctx != nil && ctx.Err() != nil {
				c.cb.ResetProbe()
				return ctx.Err()
			}
			c.cb.RecordFailure()
			return fmt.Errorf("%w: %v", ErrRetryable, doErr)
		}
		defer resp.Body.Close()

		if isTransientStatus(resp.StatusCode) {
			c.cb.RecordFailure()
			return fmt.Errorf("%w: server returned transient status %d", ErrRetryable, resp.StatusCode)
		}

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%w: unexpected HTTP status %d", ErrInvalidStatus, resp.StatusCode)
		}

		// Read response body with 1MB payload limit
		limitedReader := io.LimitReader(resp.Body, int64(MaxResponsePayloadBytes)+1)
		bodyBytes, readErr := io.ReadAll(limitedReader)
		if readErr != nil {
			c.cb.RecordFailure()
			return fmt.Errorf("%w: read response body: %v", ErrRetryable, readErr)
		}
		if len(bodyBytes) > MaxResponsePayloadBytes {
			return ErrPayloadTooLarge
		}

		var parsedEnv ResponseEnvelope
		if jsonErr := json.Unmarshal(bodyBytes, &parsedEnv); jsonErr != nil {
			return fmt.Errorf("decode response envelope: %w", jsonErr)
		}

		respEnvelope = &parsedEnv
		c.cb.RecordSuccess()
		return nil
	})

	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			c.cb.ResetProbe()
		}
		return nil, err
	}

	return respEnvelope, nil
}

// HealthCheck executes a health check against the TEE-LLM inference endpoint.
func (c *client) HealthCheck(ctx context.Context) error {
	if !c.cb.AllowRequest() {
		return ErrCircuitOpen
	}

	err := ExecuteWithRetryContext(ctx, c.cfg.MaxRetries, c.cfg.RetryBaseDelay, c.cfg.RetryMaxDelay, func(attempt int) error {
		httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/healthz", nil)
		if reqErr != nil {
			return reqErr
		}

		resp, doErr := c.httpClient.Do(httpReq)
		if doErr != nil {
			if ctx != nil && ctx.Err() != nil {
				c.cb.ResetProbe()
				return ctx.Err()
			}
			c.cb.RecordFailure()
			return fmt.Errorf("%w: %v", ErrRetryable, doErr)
		}
		defer resp.Body.Close()

		if isTransientStatus(resp.StatusCode) {
			c.cb.RecordFailure()
			return fmt.Errorf("%w: server returned transient status %d", ErrRetryable, resp.StatusCode)
		}

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%w: unexpected HTTP status %d", ErrInvalidStatus, resp.StatusCode)
		}

		limitedReader := io.LimitReader(resp.Body, int64(MaxResponsePayloadBytes)+1)
		bodyBytes, readErr := io.ReadAll(limitedReader)
		if readErr != nil {
			c.cb.RecordFailure()
			return fmt.Errorf("%w: read response body: %v", ErrRetryable, readErr)
		}
		if len(bodyBytes) > MaxResponsePayloadBytes {
			return ErrPayloadTooLarge
		}

		c.cb.RecordSuccess()
		return nil
	})

	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			c.cb.ResetProbe()
		}
		return err
	}

	return nil
}

// Close releases any idle HTTP transport connections.
func (c *client) Close() error {
	if transport, ok := c.httpClient.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
	return nil
}
