package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CipherSlinger/teetls"
	"github.com/CipherSlinger/teellm"
)

func TestProbeEndpoint(t *testing.T) {
	mockProv := teetls.NewMockEvidenceProvider()
	tlsCfg := &teetls.Config{
		Mode:             teetls.ModePermissive,
		EvidenceProvider: mockProv,
	}

	backend, err := teellm.NewOllamaBackend(teellm.OllamaBackendConfig{
		Endpoint: "http://127.0.0.1:11434",
	})
	if err != nil {
		t.Fatalf("unexpected backend error: %v", err)
	}

	// Create test server with mock backend that returns 200 on healthz
	server, err := teellm.NewServer(teellm.ServerConfig{
		Addr:    "127.0.0.1:0",
		TEETLS:  tlsCfg,
		Backend: backend,
	})
	if err != nil {
		t.Fatalf("unexpected server creation error: %v", err)
	}

	// Start an HTTP mock server for Ollama so HealthCheck succeeds
	ollamaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer ollamaMock.Close()

	// Update backend endpoint to point to ollamaMock
	mockBackend, _ := teellm.NewOllamaBackend(teellm.OllamaBackendConfig{
		Endpoint: ollamaMock.URL,
	})
	serverWithMock, err := teellm.NewServer(teellm.ServerConfig{
		Addr:    "127.0.0.1:0",
		TEETLS:  tlsCfg,
		Backend: mockBackend,
	})
	if err != nil {
		t.Fatalf("unexpected server error: %v", err)
	}
	defer server.Close()
	defer serverWithMock.Close()

	go func() {
		_ = serverWithMock.Start()
	}()
	time.Sleep(50 * time.Millisecond)

	serverAddr := serverWithMock.Addr()
	targetURL := "https://" + serverAddr

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := runProbe(ctx, targetURL); err != nil {
		t.Fatalf("expected runProbe to succeed, got: %v", err)
	}
}
