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
			switch r.URL.Path {
			case "/api/tags":
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"models":[{"name":"qwen2.5-coder:3b"}]}`))
			case "/api/generate":
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"response":"pong"}`))
			default:
				http.NotFound(w, r)
			}
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

	t.Run("unhealthy status code on tags", func(t *testing.T) {
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

	t.Run("unhealthy generate probe with truncated weights", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/tags":
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"models":[{"name":"qwen2.5-coder:3b"}]}`))
			case "/api/generate":
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"read GGUF metadata: unsupported tensor offset exceeds file size"}`))
			default:
				http.NotFound(w, r)
			}
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
			t.Fatalf("expected health check to fail when generate probe fails with 500")
		}
	})
}

func TestOllamaBackend_CatalogOptionsMerged(t *testing.T) {
	var capturedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/generate" {
			_ = json.NewDecoder(r.Body).Decode(&capturedBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"response": "answer"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	catalog := &ModelCatalog{
		Models: map[string]ModelProfile{
			"qwen3:8b": {
				Options: map[string]any{
					"num_ctx": float64(4096),
					"think":   false,
				},
			},
		},
	}

	b, err := NewOllamaBackend(OllamaBackendConfig{
		Endpoint:     server.URL,
		DefaultModel: "qwen3:8b",
		Catalog:      catalog,
	})
	if err != nil {
		t.Fatalf("failed to create backend: %v", err)
	}

	_, err = b.Generate(context.Background(), &GenerateRequest{
		ProtocolVersion: CurrentProtocolVersion,
		RequestID:       "req-cat-1",
		Model:           "qwen3:8b",
		Prompt:          "hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	options, ok := capturedBody["options"].(map[string]any)
	if !ok {
		t.Fatalf("expected options map in request body, got %v", capturedBody["options"])
	}

	if options["num_ctx"] != float64(4096) {
		t.Errorf("expected num_ctx=4096, got %v", options["num_ctx"])
	}
	if options["think"] != false {
		t.Errorf("expected think=false, got %v", options["think"])
	}
}

func TestOllamaBackend_GenerateMergesOptions(t *testing.T) {
	var got map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"response":"answer"}`))
	}))
	defer upstream.Close()

	catalog := DefaultModelCatalog()
	backend, err := NewOllamaBackend(OllamaBackendConfig{
		Endpoint:     upstream.URL,
		DefaultModel: "qwen2.5-coder:0.5b",
		Catalog:      catalog,
	})
	if err != nil {
		t.Fatalf("NewOllamaBackend failed: %v", err)
	}

	resp, err := backend.Generate(context.Background(), &GenerateRequest{
		ProtocolVersion: CurrentProtocolVersion,
		RequestID:       "r1",
		Prompt:          "hello",
		Options:         map[string]any{"temperature": 0, "num_predict": 42},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if resp.Text != "answer" || resp.Status != StatusSuccess {
		t.Fatalf("response = %+v", resp)
	}

	opts, _ := got["options"].(map[string]any)
	if opts["temperature"] != float64(0) {
		t.Errorf("temperature = %v", opts["temperature"])
	}
	if opts["num_predict"] != float64(42) {
		t.Errorf("num_predict = %v, want the request's override", opts["num_predict"])
	}
	if got["prompt"] != "hello" {
		t.Errorf("prompt = %v", got["prompt"])
	}
}

func TestOllamaBackend_GenerateRejectsEmptyPrompt(t *testing.T) {
	backend, err := NewOllamaBackend(OllamaBackendConfig{Endpoint: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewOllamaBackend failed: %v", err)
	}
	resp, err := backend.Generate(context.Background(), &GenerateRequest{})
	if err == nil {
		t.Fatal("expected an error for an empty prompt")
	}
	if resp == nil || resp.Status != StatusInvalidRequest {
		t.Fatalf("response = %+v", resp)
	}
}
