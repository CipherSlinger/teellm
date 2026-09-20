package teellm

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadModelCatalog_Success(t *testing.T) {
	tempDir := t.TempDir()
	jsonPath := filepath.Join(tempDir, "models.json")
	sampleJSON := `{
		"version": "1.0",
		"activeModel": "qwen2.5-coder:3b",
		"models": {
			"qwen2.5-coder:0.5b": {
				"family": "qwen2.5-coder",
				"parameterSize": "0.5B",
				"weightSizeMB": 397,
				"timeoutSeconds": 30,
				"options": {
					"num_ctx": 2048,
					"temperature": 0.1,
					"num_predict": 160
				}
			},
			"qwen3:8b": {
				"family": "qwen3",
				"options": {
					"num_ctx": 4096,
					"think": false
				}
			}
		}
	}`

	if err := os.WriteFile(jsonPath, []byte(sampleJSON), 0644); err != nil {
		t.Fatalf("failed to write test json: %v", err)
	}

	catalog, err := LoadModelCatalog(jsonPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if catalog.ActiveModel != "qwen2.5-coder:3b" {
		t.Errorf("expected active model qwen2.5-coder:3b, got %s", catalog.ActiveModel)
	}

	p05 := catalog.GetProfile("qwen2.5-coder:0.5b")
	if p05 == nil {
		t.Fatalf("expected profile for qwen2.5-coder:0.5b")
	}
	if p05.TimeoutSeconds != 30 {
		t.Errorf("expected timeout 30, got %d", p05.TimeoutSeconds)
	}
	if p05.Options["num_ctx"] != float64(2048) {
		t.Errorf("expected num_ctx 2048, got %v", p05.Options["num_ctx"])
	}

	p8b := catalog.GetProfile("qwen3:8b")
	if p8b == nil {
		t.Fatalf("expected profile for qwen3:8b")
	}
	if p8b.Options["think"] != false {
		t.Errorf("expected think=false, got %v", p8b.Options["think"])
	}

	// Test fallback for unknown model
	pUnknown := catalog.GetProfile("custom-model:latest")
	if pUnknown == nil {
		t.Fatalf("expected fallback profile for unknown model")
	}
	if pUnknown.TimeoutDuration(60 * time.Second) != 60*time.Second {
		t.Errorf("expected default timeout 60s")
	}
}

func TestLoadModelCatalog_NotFoundFallback(t *testing.T) {
	catalog, err := LoadModelCatalog("/non/existent/path.json")
	if err == nil {
		t.Errorf("expected error for non-existent path")
	}
	if catalog == nil {
		t.Fatalf("expected non-nil default fallback catalog even on error")
	}
}
