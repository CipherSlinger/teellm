package teellm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestOllamaBackend_CatalogOptionsMerged(t *testing.T) {
	var capturedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/generate" {
			_ = json.NewDecoder(r.Body).Decode(&capturedBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"response": "{\"verdict\":\"BENIGN\",\"reason\":\"ok\"}"}`))
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

	req := &RequestEnvelope{
		RequestID: "req-cat-1",
		ModelRef: &ModelReference{
			Name: "qwen3:8b",
		},
		FindingPayload: &FindingPayload{
			RuleID: "RULE_01",
			Target: CodeTarget{
				FilePath: "test.py",
				Line:     10,
			},
		},
	}

	_, err = b.HandleVerifyFinding(context.Background(), req)
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

// TestFindingAnalysisPromptOrderAndVerbs pins the layout of the finding prompt:
// how the nine format verbs map onto their arguments, and where the scoring
// rubric sits relative to the code.
//
// The prompt is one fmt.Sprintf format string, so moving a verb-bearing line
// silently re-binds every argument after it. The result stays syntactically
// valid and only the model sees the damage, which is why the sentinels below
// exist: they make a re-binding readable in the failure output.
//
// The rubric's position is pinned because moving it was measured, not because
// it is arbitrary. Hoisting the rubric to the front of the prompt (a natural
// idea: it lengthens the prefix that every finding shares, and llama.cpp reuses
// only a common prefix) took the per-call cached prefix from 62 to 553 tokens
// and cut prompt eval from ~84s to ~29s across five findings - and broke the
// verdicts. On the same sample, same model, same payloads:
//
//	rubric after the code: DYN_001 BENIGN, EMB_003 x4 BENIGN, passed=true
//	rubric before the code: DYN_001 MALICIOUS, EMB_003 mostly SUSPICIOUS,
//	                        passed=false, risk_level=CRITICAL (reproduced twice)
//
// The rubric names rule ids (EMB_003, DYN_001) and says they must be BENIGN.
// With the rubric last, those ids are still fresh when the model answers; with
// the rubric first, the code and the rule block sit between the instruction and
// the answer. The speed was real and so was the regression, so this ordering is
// load-bearing and the test exists to make the next reorder fail loudly rather
// than quietly.
func TestFindingAnalysisPromptOrderAndVerbs(t *testing.T) {
	var capturedPrompt string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		capturedPrompt, _ = body["prompt"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response": "{\"verdict\":\"BENIGN\",\"reason\":\"ok\"}"}`))
	}))
	defer server.Close()

	b, err := NewOllamaBackend(OllamaBackendConfig{Endpoint: server.URL, DefaultModel: "test-model"})
	if err != nil {
		t.Fatalf("failed to create backend: %v", err)
	}

	req := &RequestEnvelope{
		RequestID: "req-order-1",
		FindingPayload: &FindingPayload{
			RuleID:      "<<RID>>",
			Category:    "<<CAT>>",
			Severity:    "<<SEV>>",
			Description: "<<DESC>>",
			Target: CodeTarget{
				FilePath:      "<<FP>>",
				Line:          9042,
				ContextBefore: "<<BEF>>",
				CodeSnippet:   "<<SNIP>>",
				ContextAfter:  "<<AFT>>",
			},
		},
	}
	if _, err := b.HandleVerifyFinding(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedPrompt == "" {
		t.Fatal("server captured no prompt")
	}

	// A mispaired verb renders as "%!s(MISSING)" or "%!(EXTRA string=...)"; both
	// carry the "%!" marker.
	if strings.Contains(capturedPrompt, "%!") {
		t.Errorf("rendered prompt carries a fmt error marker:\n%s", capturedPrompt)
	}

	// The layout invariant. Each step is load-bearing: the code comes first, the
	// rubric that names the rule ids comes after the rule block, and the bare-JSON
	// demand stays nearest generation. See the note on this test for the measured
	// cost of getting the rubric's position wrong.
	order := []string{"## 代码片段", "规则说明", "判定优先级与安全边界", "## 请回答", `{"verdict"`}
	prev := -1
	for _, marker := range order {
		at := strings.Index(capturedPrompt, marker)
		if at < 0 {
			t.Fatalf("prompt marker %q not found", marker)
		}
		if at < prev {
			t.Errorf("prompt marker %q is out of order: at %d, previous marker at %d", marker, at, prev)
		}
		prev = at
	}

	// The nine arguments in the order the call site passes them.
	want := []string{"<<FP>>", "9042", "<<BEF>>", "<<SNIP>>", "<<AFT>>", "<<RID>>", "<<CAT>>", "<<SEV>>", "<<DESC>>"}
	prev = -1
	for _, w := range want {
		at := strings.Index(capturedPrompt, w)
		if at < 0 {
			t.Fatalf("sentinel %s missing from the rendered prompt", w)
		}
		if at < prev {
			t.Errorf("sentinel %s is out of order: at %d, previous sentinel at %d", w, at, prev)
		}
		prev = at
	}
}
