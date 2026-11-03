package platformagent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func TestStartRegistersThenHeartbeatsAndFlushesLogs(t *testing.T) {
	var (
		mu       sync.Mutex
		register int
		beat     int
		logs     int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		switch r.URL.Path {
		case "/v1/llm/register":
			register++
		case "/v1/llm/heartbeat":
			beat++
		case "/v1/llm/log":
			logs++
		}
		mu.Unlock()
		_, _ = w.Write([]byte(`{"msg":"success","result":{},"error":0}`))
	}))
	defer server.Close()

	agent, err := New(Config{
		PlatformAddr:      server.URL,
		DockerID:          "taa-teellm",
		HeartbeatInterval: 20 * time.Millisecond,
		LogFlushInterval:  20 * time.Millisecond,
		Snapshot: func(context.Context) (*teellm.AttestationSnapshot, error) {
			return fakeSnapshot(), nil
		},
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	agent.Start(ctx)
	defer func() {
		cancel()
		agent.Stop()
	}()

	agent.Logs().Add(LevelInfo, "teellm", "started")
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if register < 1 {
		t.Errorf("register calls = %d, want at least 1", register)
	}
	if beat < 1 {
		t.Errorf("heartbeat calls = %d, want at least 1", beat)
	}
	if logs < 1 {
		t.Errorf("log calls = %d, want at least 1", logs)
	}
}

func TestStartRetriesRegistrationUntilItSucceeds(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/llm/register" {
			if atomic.AddInt32(&attempts, 1) < 3 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
		_, _ = w.Write([]byte(`{"msg":"success","result":{},"error":0}`))
	}))
	defer server.Close()

	agent, err := New(Config{
		PlatformAddr:      server.URL,
		DockerID:          "taa-teellm",
		HeartbeatInterval: 50 * time.Millisecond,
		LogFlushInterval:  50 * time.Millisecond,
		Snapshot: func(context.Context) (*teellm.AttestationSnapshot, error) {
			return fakeSnapshot(), nil
		},
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	agent.Start(ctx)
	defer func() {
		cancel()
		agent.Stop()
	}()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&attempts) >= 3 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("register attempts = %d, want at least 3", atomic.LoadInt32(&attempts))
}

func TestStopDoesNotBlockWhenPlatformIsDown(t *testing.T) {
	agent, err := New(Config{
		PlatformAddr:      "http://127.0.0.1:1",
		DockerID:          "taa-teellm",
		HeartbeatInterval: 20 * time.Millisecond,
		LogFlushInterval:  20 * time.Millisecond,
		Snapshot: func(context.Context) (*teellm.AttestationSnapshot, error) {
			return fakeSnapshot(), nil
		},
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	agent.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		cancel()
		agent.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop blocked while the platform was unreachable")
	}
}

func TestReregistersWhenPlatformBecomesUnreachable(t *testing.T) {
	var (
		mu        sync.Mutex
		registers int
		unreach   bool
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if r.URL.Path == "/v1/llm/register" {
			registers++
		}
		down := unreach
		mu.Unlock()

		if down {
			// Hold past the client's deadline: a probe then fails the way it
			// does against a platform that is gone, without depending on which
			// kernel-level error a closed port happens to produce.
			time.Sleep(200 * time.Millisecond)
			return
		}
		_, _ = w.Write([]byte(`{"msg":"success","result":{},"error":0}`))
	}))
	defer server.Close()

	agent, err := New(Config{
		PlatformAddr:      server.URL,
		DockerID:          "taa-teellm",
		HeartbeatInterval: 30 * time.Millisecond,
		LogFlushInterval:  30 * time.Millisecond,
		HTTPClient:        &http.Client{Timeout: 80 * time.Millisecond},
		Snapshot: func(context.Context) (*teellm.AttestationSnapshot, error) {
			return fakeSnapshot(), nil
		},
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	agent.Start(ctx)
	defer func() {
		cancel()
		agent.Stop()
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		first := registers >= 1
		mu.Unlock()
		if first {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no registration happened")
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	unreach = true
	mu.Unlock()

	for {
		mu.Lock()
		n := registers
		mu.Unlock()
		if n >= 2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("register calls = %d, want a second registration once the platform stopped answering", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestIsConnectivityError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "dial refused",
			err:  &url.Error{Op: "Post", URL: "http://p", Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}},
			want: true,
		},
		{
			name: "dns failure",
			err:  &url.Error{Op: "Post", URL: "http://p", Err: &net.DNSError{Err: "no such host", Name: "p"}},
			want: true,
		},
		{
			name: "http status is not a connectivity failure",
			err:  errors.New("/v1/llm/heartbeat returned status 500: boom"),
			want: false,
		},
		{name: "nil", err: nil, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isConnectivityError(tc.err); got != tc.want {
				t.Fatalf("isConnectivityError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
