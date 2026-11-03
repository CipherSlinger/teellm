package teellm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

type stubBackend struct {
	gotReq *GenerateRequest
	resp   *GenerateResponse
	err    error
}

func (b *stubBackend) Generate(_ context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	b.gotReq = req
	return b.resp, b.err
}

func (b *stubBackend) HandleHealthCheck(context.Context) error { return nil }

func newPlainServer(t *testing.T, backend Backend) *Server {
	t.Helper()
	srv, err := NewServer(ServerConfig{Addr: "127.0.0.1:0", Backend: backend})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	go func() { _ = srv.Start() }()
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func TestGenerateHandlerReturnsText(t *testing.T) {
	backend := &stubBackend{resp: &GenerateResponse{
		ProtocolVersion: CurrentProtocolVersion,
		RequestID:       "r1",
		Status:          StatusSuccess,
		Model:           "m",
		Text:            "answer",
	}}
	srv := newPlainServer(t, backend)

	body, _ := json.Marshal(GenerateRequest{ProtocolVersion: CurrentProtocolVersion, RequestID: "r1", Prompt: "hello"})
	resp, err := http.Post("http://"+srv.Addr()+"/v1/llm/generate", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got GenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if got.Text != "answer" {
		t.Fatalf("text = %q", got.Text)
	}
	if backend.gotReq == nil || backend.gotReq.Prompt != "hello" {
		t.Fatalf("backend saw %+v", backend.gotReq)
	}
}

func TestGenerateHandlerRejectsMissingPrompt(t *testing.T) {
	srv := newPlainServer(t, &stubBackend{})
	body := `{"protocolVersion":"teellm-protocol/v1","requestId":"r1"}`
	resp, err := http.Post("http://"+srv.Addr()+"/v1/llm/generate", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(raw, []byte("INVALID_REQUEST")) {
		t.Fatalf("body = %s", raw)
	}
}
