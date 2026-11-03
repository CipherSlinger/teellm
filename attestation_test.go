package teellm

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CipherSlinger/teetls"
	"github.com/CipherSlinger/teetls/pkg/csvattest"
)

type attestationResponse struct {
	Msg    string `json:"msg"`
	Error  int    `json:"error"`
	Result struct {
		RequestID         string `json:"requestId"`
		VerifiedPass      bool   `json:"verifiedPass"`
		Attestation       string `json:"attestation"`
		AttestationValues string `json:"attestationValues"`
		Certificate       string `json:"certificate"`
	} `json:"result"`
}

func newAttestationServer(t *testing.T) *Server {
	t.Helper()
	srv, err := NewServer(ServerConfig{
		Addr: "127.0.0.1:0",
		TEETLS: &teetls.Config{
			Mode:                          teetls.ModePermissive,
			EvidenceProvider:              teetls.NewMockEvidenceProvider(),
			InsecureSkipAttestationVerify: true,
		},
		Backend: &mockBackend{},
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// attestationURL exposes the handler on a plaintext httptest server. Only the
// response bytes are under test, and the transport has no bearing on them;
// TestGetAttestationIsMountedOnTheTEETLSMux checks that the real TEE-TLS mux
// routes the same path.
func attestationURL(t *testing.T, srv *Server) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(srv.handleGetAttestation))
	t.Cleanup(ts.Close)
	return ts.URL + "/v1/llm/getAttestation"
}

func TestGetAttestationReturnsReportBoundToCertificate(t *testing.T) {
	srv := newAttestationServer(t)

	resp, err := http.Post(attestationURL(t, srv),
		"application/json", strings.NewReader(`{"requestId":"req-1"}`))
	if err != nil {
		t.Fatalf("post failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var decoded attestationResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if decoded.Result.RequestID != "req-1" {
		t.Fatalf("requestId = %q, want req-1", decoded.Result.RequestID)
	}
	if !decoded.Result.VerifiedPass {
		t.Fatalf("expected the mock report to self-verify, msg=%q", decoded.Msg)
	}

	report, err := base64.StdEncoding.DecodeString(decoded.Result.Attestation)
	if err != nil {
		t.Fatalf("attestation is not base64: %v", err)
	}
	if len(report) != csvattest.ReportSize {
		t.Fatalf("report size = %d, want %d", len(report), csvattest.ReportSize)
	}
	if _, err := csvattest.ParseReport(report); err != nil {
		t.Fatalf("report does not parse: %v", err)
	}

	cert, err := teetls.ParseCertificatePEM([]byte(decoded.Result.Certificate))
	if err != nil {
		t.Fatalf("returned certificate does not parse: %v", err)
	}

	// The report must bind the key of the certificate returned alongside it.
	parsed, err := csvattest.ParseReport(report)
	if err != nil {
		t.Fatalf("parse report: %v", err)
	}
	want := teetls.ComputePublicKeySM3(cert.RawSubjectPublicKeyInfo)
	if len(parsed.UserData) < 32 || string(parsed.UserData[:32]) != string(want[:]) {
		t.Fatalf("report userdata does not bind the returned certificate")
	}
}

func TestGetAttestationRejectsEmptyRequestID(t *testing.T) {
	srv := newAttestationServer(t)

	resp, err := http.Post(attestationURL(t, srv),
		"application/json", strings.NewReader(`{"requestId":""}`))
	if err != nil {
		t.Fatalf("post failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestGetAttestationIsMountedOnTheTEETLSMux(t *testing.T) {
	srv := newAttestationServer(t)

	mux, ok := srv.httpSrv.Handler.(*http.ServeMux)
	if !ok {
		t.Fatalf("server handler is %T, want *http.ServeMux", srv.httpSrv.Handler)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/llm/getAttestation", nil)
	if _, pattern := mux.Handler(req); pattern != "/v1/llm/getAttestation" {
		t.Fatalf("route pattern = %q, want /v1/llm/getAttestation", pattern)
	}
}
