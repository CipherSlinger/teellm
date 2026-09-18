package teellm

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CipherSlinger/teetls"
)

type mockBackend struct {
	mu          sync.Mutex
	healthErr   error
	verifyResp  *ResponseEnvelope
	verifyErr   error
	largeString string
}

func (m *mockBackend) HandleHealthCheck(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.healthErr
}

func (m *mockBackend) HandleVerifyFinding(ctx context.Context, req *RequestEnvelope) (*ResponseEnvelope, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.verifyErr != nil {
		return nil, m.verifyErr
	}

	if m.largeString != "" {
		return &ResponseEnvelope{
			ProtocolVersion: CurrentProtocolVersion,
			RequestID:       req.RequestID,
			Status:          StatusSuccess,
			Decision: &DecisionResult{
				Verdict:     VerdictSuspicious,
				Confidence:  0.8,
				Explanation: m.largeString,
			},
		}, nil
	}

	if m.verifyResp != nil {
		return m.verifyResp, nil
	}

	return &ResponseEnvelope{
		ProtocolVersion: CurrentProtocolVersion,
		RequestID:       req.RequestID,
		Status:          StatusSuccess,
		Decision: &DecisionResult{
			Verdict:     VerdictBenign,
			Confidence:  0.99,
			ReasonCode:  "NO_MALICIOUS_LOGIC",
			Explanation: "Finding is benign.",
		},
	}, nil
}

// TestTEELLM_ClientServerEndToEnd verifies mutual communication over TEE-TLS 1.3 between
// teellm.Client and teellm.Server, including health check and finding verification.
func TestTEELLM_ClientServerEndToEnd(t *testing.T) {
	mockProv := teetls.NewMockEvidenceProvider()
	measHex := mockProv.GetMeasurementHex()

	backend := &mockBackend{
		verifyResp: &ResponseEnvelope{
			ProtocolVersion: CurrentProtocolVersion,
			RequestID:       "req-e2e-1",
			Status:          StatusSuccess,
			Decision: &DecisionResult{
				Verdict:     VerdictBenign,
				Confidence:  0.95,
				RiskLevel:   "LOW",
				ReasonCode:  "SAFE_CODE",
				Explanation: "Static finding is false positive.",
			},
		},
	}

	serverCfg := ServerConfig{
		Addr: "127.0.0.1:0",
		TEETLS: &teetls.Config{
			Mode:             teetls.ModeStrict,
			EvidenceProvider: mockProv,
		},
		Backend: backend,
	}

	srv, err := NewServer(serverCfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	go func() {
		_ = srv.Start()
	}()
	defer srv.Close()

	// Wait briefly for server listener to be ready
	time.Sleep(50 * time.Millisecond)

	clientCfg := Config{
		Endpoint: "https://" + srv.Addr(),
		Timeout:  5 * time.Second,
		TEETLS: &teetls.Config{
			Mode:                 teetls.ModeStrict,
			EvidenceProvider:     mockProv,
			ExpectedMeasurements: []string{measHex},
		},
	}

	client, err := NewClient(clientCfg)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer client.Close()

	// 1. Health check
	if err := client.HealthCheck(context.Background()); err != nil {
		t.Fatalf("HealthCheck failed: %v", err)
	}

	// 2. Finding verification
	req := &RequestEnvelope{
		ProtocolVersion: CurrentProtocolVersion,
		RequestID:       "req-e2e-1",
		Action:          ActionVerifyFinding,
		FindingPayload: &FindingPayload{
			RuleID:   "RULE-SQLI-01",
			Category: "SQL_INJECTION",
			Severity: "HIGH",
			Target: CodeTarget{
				FilePath:    "db/query.go",
				Line:        42,
				CodeSnippet: "db.Query(ctx, query)",
			},
		},
		Policy: PolicyOptions{
			Mode: PolicyModeGate,
		},
	}

	resp, err := client.VerifyFinding(context.Background(), req)
	if err != nil {
		t.Fatalf("VerifyFinding failed: %v", err)
	}
	if resp.Status != StatusSuccess {
		t.Fatalf("expected status %q, got %q", StatusSuccess, resp.Status)
	}
	if resp.Decision == nil || resp.Decision.Verdict != VerdictBenign {
		t.Fatalf("expected verdict %q, got %v", VerdictBenign, resp.Decision)
	}
}

// TestTEELLM_CircuitBreakerTripAndRecovery verifies that consecutive server 500 errors trip
// the circuit breaker to OPEN, and subsequent cooldown allows probe recovery.
func TestTEELLM_CircuitBreakerTripAndRecovery(t *testing.T) {
	mockProv := teetls.NewMockEvidenceProvider()
	measHex := mockProv.GetMeasurementHex()

	backend := &mockBackend{
		verifyErr: errors.New("backend failed internal check"),
	}

	serverCfg := ServerConfig{
		Addr: "127.0.0.1:0",
		TEETLS: &teetls.Config{
			Mode:             teetls.ModeStrict,
			EvidenceProvider: mockProv,
		},
		Backend: backend,
	}

	srv, err := NewServer(serverCfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	go func() {
		_ = srv.Start()
	}()
	defer srv.Close()

	time.Sleep(50 * time.Millisecond)

	// Configure client with threshold 2, 60ms cooldown, 0 retries
	clientCfg := Config{
		Endpoint:                       "https://" + srv.Addr(),
		Timeout:                        5 * time.Second,
		CircuitBreakerFailureThreshold: 2,
		CircuitBreakerCooldown:         60 * time.Millisecond,
		MaxRetries:                     0,
		TEETLS: &teetls.Config{
			Mode:                 teetls.ModeStrict,
			EvidenceProvider:     mockProv,
			ExpectedMeasurements: []string{measHex},
		},
	}

	client, err := NewClient(clientCfg)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer client.Close()

	req := &RequestEnvelope{
		ProtocolVersion: CurrentProtocolVersion,
		RequestID:       "req-cb-fail",
		Action:          ActionVerifyFinding,
	}

	// Request 1: fails with 500
	_, err = client.VerifyFinding(context.Background(), req)
	if err == nil {
		t.Fatal("expected request 1 to fail")
	}

	// Request 2: fails with 500, trips breaker to OPEN
	_, err = client.VerifyFinding(context.Background(), req)
	if err == nil {
		t.Fatal("expected request 2 to fail")
	}

	// Request 3: breaker is now OPEN, fast-fails with ErrCircuitOpen
	_, err = client.VerifyFinding(context.Background(), req)
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got: %v", err)
	}

	// HealthCheck should also be rejected by open breaker
	if err := client.HealthCheck(context.Background()); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected HealthCheck to return ErrCircuitOpen, got: %v", err)
	}

	// Wait for cooldown to expire (>60ms)
	time.Sleep(80 * time.Millisecond)

	// Heal backend
	backend.mu.Lock()
	backend.verifyErr = nil
	backend.mu.Unlock()

	// Request 4: probe in HALF-OPEN state succeeds and closes breaker
	resp, err := client.VerifyFinding(context.Background(), req)
	if err != nil {
		t.Fatalf("probe request in HALF-OPEN failed: %v", err)
	}
	if resp.Status != StatusSuccess {
		t.Fatalf("expected SUCCESS status on probe, got: %s", resp.Status)
	}

	// Subsequent request succeeds in CLOSED state
	if err := client.HealthCheck(context.Background()); err != nil {
		t.Fatalf("expected HealthCheck success after breaker reset, got: %v", err)
	}
}

// TestTEELLM_ResponseSizeLimit verifies that a response exceeding 1MB fails with ErrPayloadTooLarge.
func TestTEELLM_ResponseSizeLimit(t *testing.T) {
	mockProv := teetls.NewMockEvidenceProvider()
	measHex := mockProv.GetMeasurementHex()

	// 1.2 MB response payload
	largeData := strings.Repeat("A", 1200000)
	backend := &mockBackend{
		largeString: largeData,
	}

	serverCfg := ServerConfig{
		Addr: "127.0.0.1:0",
		TEETLS: &teetls.Config{
			Mode:             teetls.ModeStrict,
			EvidenceProvider: mockProv,
		},
		Backend: backend,
	}

	srv, err := NewServer(serverCfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	go func() {
		_ = srv.Start()
	}()
	defer srv.Close()

	time.Sleep(50 * time.Millisecond)

	clientCfg := Config{
		Endpoint: "https://" + srv.Addr(),
		Timeout:  5 * time.Second,
		TEETLS: &teetls.Config{
			Mode:                 teetls.ModeStrict,
			EvidenceProvider:     mockProv,
			ExpectedMeasurements: []string{measHex},
		},
	}

	client, err := NewClient(clientCfg)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer client.Close()

	req := &RequestEnvelope{
		ProtocolVersion: CurrentProtocolVersion,
		RequestID:       "req-large",
		Action:          ActionVerifyFinding,
	}

	_, err = client.VerifyFinding(context.Background(), req)
	if !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("expected ErrPayloadTooLarge, got: %v", err)
	}
}

// TestTEELLM_SSRFDefenses verifies that NewClient strictly enforces endpoint security.
func TestTEELLM_SSRFDefenses(t *testing.T) {
	tests := []struct {
		name         string
		endpoint     string
		allowedHosts []string
		wantErr      bool
		errSubstring string
	}{
		{
			name:         "plain http rejected",
			endpoint:     "http://127.0.0.1:8443",
			wantErr:      true,
			errSubstring: "strictly https is required",
		},
		{
			name:         "unix socket rejected",
			endpoint:     "unix:///tmp/test.sock",
			wantErr:      true,
			errSubstring: "strictly https is required",
		},
		{
			name:         "cloud metadata ip rejected",
			endpoint:     "https://169.254.169.254:8443",
			wantErr:      true,
			errSubstring: "restricted address",
		},
		{
			name:         "any address 0.0.0.0 rejected",
			endpoint:     "https://0.0.0.0:8443",
			wantErr:      true,
			errSubstring: "restricted address",
		},
		{
			name:         "disallowed host rejected",
			endpoint:     "https://evil.corp:8443",
			allowedHosts: []string{"trusted.corp"},
			wantErr:      true,
			errSubstring: "not in the allowed endpoint whitelist",
		},
		{
			name:         "allowed host accepted",
			endpoint:     "https://trusted.corp:8443",
			allowedHosts: []string{"trusted.corp"},
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewClient(Config{
				Endpoint:     tt.endpoint,
				AllowedHosts: tt.allowedHosts,
			})
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewClient() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr && tt.errSubstring != "" {
				if !strings.Contains(err.Error(), tt.errSubstring) {
					t.Fatalf("expected error containing %q, got: %v", tt.errSubstring, err)
				}
			}
		})
	}
}

// TestTEELLM_ServerPayloadTooLarge verifies that server rejects request payloads > 1MB.
func TestTEELLM_ServerPayloadTooLarge(t *testing.T) {
	backend := &mockBackend{}
	serverCfg := ServerConfig{
		Addr:    "127.0.0.1:0",
		Backend: backend,
	}

	srv, err := NewServer(serverCfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	go func() {
		_ = srv.Start()
	}()
	defer srv.Close()

	time.Sleep(50 * time.Millisecond)

	// Send >1MB request body directly to plain HTTP server
	hugeBody := bytes.Repeat([]byte("X"), 1048576+10)
	resp, err := http.Post("http://"+srv.Addr()+"/v1/verify", "application/json", bytes.NewReader(hugeBody))
	if err != nil {
		t.Fatalf("post failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected status 413, got %d", resp.StatusCode)
	}
}
