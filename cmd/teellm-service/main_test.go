package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/CipherSlinger/teellm"
	"github.com/CipherSlinger/teetls"
)

func TestLoadServiceConfig(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "teellm-cfg-*.json")
	if err != nil {
		t.Fatalf("failed to create temp config: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	content := `{
		"server": {"addr": ":9443"},
		"backend": {"endpoint": "http://127.0.0.1:11435", "defaultModel": "qwen2.5-coder:7b"},
		"attestation": {"mode": "strict", "mock": true}
	}`
	if _, err := tmpFile.WriteString(content); err != nil {
		t.Fatalf("failed to write config content: %v", err)
	}
	_ = tmpFile.Close()

	cfg, err := loadServiceConfig(tmpFile.Name())
	if err != nil {
		t.Fatalf("loadServiceConfig failed: %v", err)
	}
	if cfg.Server.Addr != ":9443" {
		t.Errorf("expected addr :9443, got %s", cfg.Server.Addr)
	}
	if cfg.Backend.DefaultModel != "qwen2.5-coder:7b" {
		t.Errorf("expected model qwen2.5-coder:7b, got %s", cfg.Backend.DefaultModel)
	}
	if cfg.Attestation.Mode != "strict" || !cfg.Attestation.Mock {
		t.Errorf("expected strict mock attestation, got mode=%s mock=%v", cfg.Attestation.Mode, cfg.Attestation.Mock)
	}
}

func TestLoadServiceConfigDefault(t *testing.T) {
	cfg, err := loadServiceConfig("")
	if err != nil {
		t.Fatalf("loadServiceConfig with empty path failed: %v", err)
	}
	if cfg.Server.Addr != ":8443" {
		t.Errorf("expected default addr :8443, got %s", cfg.Server.Addr)
	}
	if cfg.Backend.DefaultModel != "qwen2.5-coder:3b" {
		t.Errorf("expected default model qwen2.5-coder:3b, got %s", cfg.Backend.DefaultModel)
	}
	if cfg.Attestation.Mode != "permissive" || cfg.Attestation.Mock {
		t.Errorf("expected permissive non-mock attestation, got mode=%s mock=%v", cfg.Attestation.Mode, cfg.Attestation.Mock)
	}
}

func TestMergeConfigWithFlags(t *testing.T) {
	cfg, err := loadServiceConfig("")
	if err != nil {
		t.Fatalf("loadServiceConfig failed: %v", err)
	}
	explicitFlags := map[string]bool{
		"addr":             true,
		"model":            true,
		"mock-attestation": true,
	}
	mergeConfigWithFlags(cfg, explicitFlags, ":9999", "http://ignored", "custom-model:14b", "/certs/hrk.cert", "/certs/hsk.cert", true)

	if cfg.Server.Addr != ":9999" {
		t.Errorf("expected overridden addr :9999, got %s", cfg.Server.Addr)
	}
	if cfg.Backend.DefaultModel != "custom-model:14b" {
		t.Errorf("expected overridden model custom-model:14b, got %s", cfg.Backend.DefaultModel)
	}
	if !cfg.Attestation.Mock {
		t.Errorf("expected overridden mock attestation to true")
	}
	// Verify non-overridden fields kept defaults
	if cfg.Backend.Endpoint != "http://127.0.0.1:11434" {
		t.Errorf("expected default endpoint http://127.0.0.1:11434, got %s", cfg.Backend.Endpoint)
	}
}

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
