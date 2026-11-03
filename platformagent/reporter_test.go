package platformagent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CipherSlinger/teellm"
)

// fakeSnapshot returns a fixed snapshot so payload shape can be asserted
// without any TEE hardware or certificate machinery.
func fakeSnapshot() *teellm.AttestationSnapshot {
	return &teellm.AttestationSnapshot{
		CertificatePEM: []byte("-----BEGIN CERTIFICATE-----\nFAKE\n-----END CERTIFICATE-----\n"),
		PublicKeyPEM:   "-----BEGIN PUBLIC KEY-----\nFAKE\n-----END PUBLIC KEY-----\n",
		Report:         []byte{0x01, 0x02, 0x03},
		Values: teellm.AttestationValues{
			UserData: "aa",
			MNonce:   "bb",
			Digest:   "cc",
			ChipID:   "CHIP",
		},
		VerifiedPass: true,
	}
}

func newTestAgent(t *testing.T, platformURL string) *Agent {
	t.Helper()
	agent, err := New(Config{
		PlatformAddr: platformURL,
		DockerID:     "taa-teellm",
		Snapshot: func(context.Context) (*teellm.AttestationSnapshot, error) {
			return fakeSnapshot(), nil
		},
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	return agent
}

func TestRegisterPayloadMatchesTAAFieldNames(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/llm/register" {
			t.Errorf("path = %q, want /v1/llm/register", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Errorf("register body is not JSON: %v", err)
		}
		_, _ = w.Write([]byte(`{"msg":"success","result":{},"error":0}`))
	}))
	defer server.Close()

	agent := newTestAgent(t, server.URL)
	if err := agent.registerOnce(context.Background()); err != nil {
		t.Fatalf("registerOnce failed: %v", err)
	}

	for _, key := range []string{
		"dockerId", "authInfo", "taaPublicKey", "attestationValues",
		"timestamp", "verifiedPass", "attestation",
	} {
		if _, ok := got[key]; !ok {
			t.Errorf("register payload is missing %q", key)
		}
	}
	if _, ok := got["taaSecretKey"]; ok {
		t.Error("register payload must not carry taaSecretKey")
	}
	if got["dockerId"] != "taa-teellm" {
		t.Errorf("dockerId = %v", got["dockerId"])
	}
	if got["verifiedPass"] != true {
		t.Errorf("verifiedPass = %v", got["verifiedPass"])
	}
	decoded, err := base64.StdEncoding.DecodeString(got["attestation"].(string))
	if err != nil || string(decoded) != string([]byte{0x01, 0x02, 0x03}) {
		t.Errorf("attestation = %q (decoded %v)", got["attestation"], err)
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(got["attestationValues"].(string)), &values); err != nil {
		t.Fatalf("attestationValues is not a JSON object string: %v", err)
	}
	if values["chipId"] != "CHIP" || values["userdata"] != "aa" {
		t.Errorf("attestationValues = %v", values)
	}
}

func TestHeartbeatPayload(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/llm/heartbeat" {
			t.Errorf("path = %q, want /v1/llm/heartbeat", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		_, _ = w.Write([]byte(`{"msg":"success","result":{},"error":0}`))
	}))
	defer server.Close()

	agent := newTestAgent(t, server.URL)
	if err := agent.sendHeartbeat(context.Background()); err != nil {
		t.Fatalf("sendHeartbeat failed: %v", err)
	}
	if got["dockerId"] != "taa-teellm" {
		t.Errorf("dockerId = %v", got["dockerId"])
	}
	if _, ok := got["timestamp"].(string); !ok {
		t.Errorf("timestamp must be a string, got %T", got["timestamp"])
	}
}

func TestLogPayloadShape(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/llm/log" {
			t.Errorf("path = %q, want /v1/llm/log", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		_, _ = w.Write([]byte(`{"msg":"success","result":{"received":true},"error":0}`))
	}))
	defer server.Close()

	agent := newTestAgent(t, server.URL)
	agent.logs.Add(LevelInfo, "teellm", "ollama ready")
	agent.flushLogs(context.Background())

	if got["requestId"] != "system" {
		t.Errorf("requestId = %v, want system", got["requestId"])
	}
	if got["dockerId"] != "taa-teellm" {
		t.Errorf("dockerId = %v", got["dockerId"])
	}
	entries, ok := got["entries"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("entries = %v", got["entries"])
	}
	first := entries[0].(map[string]any)
	if first["seq"].(float64) != 1 {
		t.Errorf("first seq = %v, want 1", first["seq"])
	}
	if !strings.Contains(first["message"].(string), "[INFO] [teellm] ollama ready") {
		t.Errorf("message = %v", first["message"])
	}
}

func TestFlushLogsKeepsBatchOnFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	agent := newTestAgent(t, server.URL)
	for i := 0; i < 3; i++ {
		agent.logs.Add(LevelInfo, "teellm", "line")
	}
	agent.flushLogs(context.Background())

	if got := agent.pendingCount(); got != 3 {
		t.Fatalf("pending = %d after a failed flush, want 3", got)
	}

	// A second attempt must re-send the same seqs from the head.
	agent.flushLogs(context.Background())
	agent.mu.Lock()
	first := agent.pending[0]
	agent.mu.Unlock()
	if first.Seq != 1 {
		t.Fatalf("pending head seq = %d, want 1", first.Seq)
	}
	if calls != 2 {
		t.Fatalf("server saw %d calls, want 2", calls)
	}
}
