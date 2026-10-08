package teellm

import (
	"context"
	"testing"
	"time"

	"github.com/CipherSlinger/teetls"
)

// delayedHealthBackend serves the health endpoint after a fixed delay, standing
// in for the real backend's inference probe: the production health check loads
// the model on a cold service, which takes seconds, not milliseconds.
type delayedHealthBackend struct {
	*mockBackend
	delay time.Duration
}

func (b *delayedHealthBackend) HandleHealthCheck(ctx context.Context) error {
	select {
	case <-time.After(b.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// startHealthTestServer brings up a TEE-TLS server whose health endpoint takes
// delay to answer, and returns a client configured with the given timeouts.
func startHealthTestServer(t *testing.T, delay, requestTimeout, healthTimeout time.Duration) Client {
	t.Helper()

	mockProv := teetls.NewMockEvidenceProvider()
	measHex := mockProv.GetMeasurementHex()

	serverCfg := ServerConfig{
		Addr: "127.0.0.1:0",
		TEETLS: &teetls.Config{
			Mode:             teetls.ModeStrict,
			EvidenceProvider: mockProv,
		},
		Backend: &delayedHealthBackend{mockBackend: &mockBackend{}, delay: delay},
	}

	srv, err := NewServer(serverCfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	go func() { _ = srv.Start() }()
	t.Cleanup(func() { _ = srv.Close() })

	client, err := NewClient(Config{
		Endpoint:           "https://" + srv.Addr(),
		Timeout:            requestTimeout,
		HealthCheckTimeout: healthTimeout,
		TEETLS: &teetls.Config{
			Mode:                 teetls.ModeStrict,
			EvidenceProvider:     mockProv,
			ExpectedMeasurements: []string{measHex},
		},
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// TestHealthCheckOutlastsTheRequestTimeout is the property the readiness gate
// needs. A cold service answers /healthz only after it has loaded the model,
// which takes seconds - longer than a normal request may take. Sharing one
// timeout with finder-verification calls makes the gate unwinnable on a cold
// start: every attempt is cut off mid-load, and because the cutoff cancels the
// load, the retries cannot make progress either.
func TestHealthCheckOutlastsTheRequestTimeout(t *testing.T) {
	client := startHealthTestServer(t, 1200*time.Millisecond, 200*time.Millisecond, 10*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := client.HealthCheck(ctx); err != nil {
		t.Fatalf("HealthCheck must outlast a cold health endpoint: %v", err)
	}
}

// TestHealthCheckRemainsBounded is the other half: the longer budget is a
// budget, not an absence of one. A health endpoint that never answers still
// fails rather than blocking the caller forever.
func TestHealthCheckRemainsBounded(t *testing.T) {
	client := startHealthTestServer(t, 30*time.Second, 200*time.Millisecond, 500*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := client.HealthCheck(ctx); err == nil {
		t.Fatal("HealthCheck must fail when the endpoint outlasts its budget")
	}
}
