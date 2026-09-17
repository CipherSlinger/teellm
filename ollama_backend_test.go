package teellm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOllamaBackend_HandleHealthCheck(t *testing.T) {
	t.Run("healthy endpoint", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/tags" {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"models":[]}`))
		}))
		defer ts.Close()

		b, err := NewOllamaBackend(OllamaBackendConfig{
			Endpoint:     ts.URL,
			DefaultModel: "qwen2.5-coder:3b",
			Timeout:      2 * time.Second,
		})
		if err != nil {
			t.Fatalf("unexpected error creating backend: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		if err := b.HandleHealthCheck(ctx); err != nil {
			t.Fatalf("expected health check success, got: %v", err)
		}
	})

	t.Run("unhealthy status code", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer ts.Close()

		b, err := NewOllamaBackend(OllamaBackendConfig{
			Endpoint:     ts.URL,
			DefaultModel: "qwen2.5-coder:3b",
			Timeout:      2 * time.Second,
		})
		if err != nil {
			t.Fatalf("unexpected error creating backend: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		if err := b.HandleHealthCheck(ctx); err == nil {
			t.Fatalf("expected health check to fail for status 503")
		}
	})
}

func TestOllamaBackend_HandleVerifyFinding(t *testing.T) {
	t.Run("successful finding verification", func(t *testing.T) {
		var receivedReq map[string]any
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/generate" {
				http.NotFound(w, r)
				return
			}
			if err := json.NewDecoder(r.Body).Decode(&receivedReq); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			resp := map[string]any{
				"response": `{"verdict": "BENIGN", "reason": "Standard dataset label printing", "risk": "None"}`,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer ts.Close()

		b, err := NewOllamaBackend(OllamaBackendConfig{
			Endpoint:     ts.URL,
			DefaultModel: "qwen2.5-coder:3b",
			Timeout:      2 * time.Second,
		})
		if err != nil {
			t.Fatalf("unexpected error creating backend: %v", err)
		}

		req := &RequestEnvelope{
			ProtocolVersion: CurrentProtocolVersion,
			RequestID:       "req-001",
			Action:          ActionVerifyFinding,
			FindingPayload: &FindingPayload{
				RuleID:      "EMB_003",
				Category:    "logging",
				Severity:    "HIGH",
				Description: "Potential data leak in print statement",
				Target: CodeTarget{
					FilePath:      "train.py",
					Line:          42,
					CodeSnippet:   "print(dataset + '--------')",
					ContextBefore: "def log_info():",
					ContextAfter:  "    pass",
				},
			},
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		resp, err := b.HandleVerifyFinding(ctx, req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Status != StatusSuccess {
			t.Fatalf("expected StatusSuccess, got %s", resp.Status)
		}
		if resp.Decision == nil {
			t.Fatalf("expected non-nil decision")
		}
		if resp.Decision.Verdict != VerdictBenign {
			t.Fatalf("expected verdict BENIGN, got %s", resp.Decision.Verdict)
		}
		if resp.Decision.Explanation != "Standard dataset label printing" {
			t.Fatalf("unexpected explanation: %s", resp.Decision.Explanation)
		}
	})

	t.Run("nil payload error handling", func(t *testing.T) {
		b, err := NewOllamaBackend(OllamaBackendConfig{
			Endpoint:     "http://127.0.0.1:11434",
			DefaultModel: "qwen2.5-coder:3b",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		resp, err := b.HandleVerifyFinding(context.Background(), &RequestEnvelope{
			RequestID: "req-empty",
		})
		if err == nil {
			t.Fatalf("expected error for nil finding payload")
		}
		if resp != nil && resp.Status != StatusInvalidRequest {
			t.Fatalf("expected StatusInvalidRequest, got %s", resp.Status)
		}
	})
}
