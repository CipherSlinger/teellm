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

// TestOllamaBackend_TemperatureIsPinned pins the audit's determinism as a
// property of the backend rather than of any one model profile.
//
// The failure it guards against is not a crash. At temperature 0.1 the audit
// flipped roughly one real finding in ten to MALICIOUS, and under assist +
// failClosed that verdict blocks an import of code that never changed. Sampling
// is a proven source of that kind of drift: replaying the five captured prompts
// at 20 samples each, 0.1 produced between 2 and 15 distinct completions per
// prompt, while 0 produced exactly one.
//
// What that measurement does not establish is that sampling accounts for the
// observed flip rate. The same replay produced no MALICIOUS at either
// temperature, so the E2E flip is rare enough (or context-dependent enough) to
// have stayed out of reach. Pinning the temperature removes one source of
// nondeterminism; it is not a demonstrated fix for the blocked import.
//
// The second subtest is the load-bearing one. The catalog merge runs before
// the pin, so a profile carrying its own temperature would overwrite the pin
// and silently re-randomise the audit -- which is exactly how the same 0.1
// ended up duplicated across the built-in catalog, the shipped JSON catalog
// and the backend default, with only whichever merged last actually mattering.
func TestOllamaBackend_TemperatureIsPinned(t *testing.T) {
	captureOptions := func(t *testing.T, req *RequestEnvelope, catalog *ModelCatalog) map[string]any {
		t.Helper()

		var capturedBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/generate" {
				http.NotFound(w, r)
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&capturedBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"response": "{\"verdict\":\"BENIGN\",\"reason\":\"ok\"}"}`))
		}))
		defer server.Close()

		cfg := OllamaBackendConfig{Endpoint: server.URL, DefaultModel: "qwen2.5-coder:3b"}
		if catalog != nil {
			cfg.Catalog = catalog
		}
		b, err := NewOllamaBackend(cfg)
		if err != nil {
			t.Fatalf("failed to create backend: %v", err)
		}
		if _, err := b.HandleVerifyFinding(context.Background(), req); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		options, ok := capturedBody["options"].(map[string]any)
		if !ok {
			t.Fatalf("expected options map in request body, got %v", capturedBody["options"])
		}
		return options
	}

	newRequest := func() *RequestEnvelope {
		return &RequestEnvelope{
			RequestID: "req-temp-1",
			FindingPayload: &FindingPayload{
				RuleID: "RULE_01",
				Target: CodeTarget{FilePath: "test.py", Line: 10},
			},
		}
	}

	t.Run("default request asks for greedy decoding", func(t *testing.T) {
		// The built-in catalog must not be able to set it either.
		options := captureOptions(t, newRequest(), DefaultModelCatalog())
		if options["temperature"] != float64(0) {
			t.Errorf("expected temperature=0, got %v", options["temperature"])
		}
	})

	t.Run("a catalog profile cannot raise it", func(t *testing.T) {
		catalog := &ModelCatalog{
			Models: map[string]ModelProfile{
				"qwen2.5-coder:3b": {
					Options: map[string]any{
						"num_ctx":     float64(4096),
						"temperature": 0.1,
					},
				},
			},
		}
		options := captureOptions(t, newRequest(), catalog)
		if options["temperature"] != float64(0) {
			t.Errorf("catalog profile re-randomised the audit: temperature=%v", options["temperature"])
		}
		// The rest of the profile must still merge through untouched.
		if options["num_ctx"] != float64(4096) {
			t.Errorf("expected num_ctx=4096, got %v", options["num_ctx"])
		}
	})

	t.Run("an explicit policy temperature still wins", func(t *testing.T) {
		// 0.5 rather than 0.1: PolicyOptions.Temperature is a *float32, and this
		// subtest compares the decoded float64 body, so it needs a value float32
		// can hold exactly.
		explicit := float32(0.5)
		req := newRequest()
		req.Policy = PolicyOptions{Temperature: &explicit}
		options := captureOptions(t, req, DefaultModelCatalog())
		if options["temperature"] != 0.5 {
			t.Errorf("expected policy temperature=0.5 to override the pin, got %v", options["temperature"])
		}
	})
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
// it is arbitrary. Two experiments sit behind this ordering.
//
// First, hoisting the rubric to the front of the prompt (a natural idea: it
// lengthens the prefix that every finding shares, and llama.cpp reuses only a
// common prefix) took the per-call cached prefix from 62 to 553 tokens and cut
// prompt eval from ~84s to ~29s across five findings - and broke the verdicts.
// On the same sample, same model, same payloads:
//
//	rubric after the code: DYN_001 BENIGN, EMB_003 x4 BENIGN, passed=true
//	rubric before the code: DYN_001 MALICIOUS, EMB_003 mostly SUSPICIOUS,
//	                        passed=false, risk_level=CRITICAL (reproduced twice)
//
// The rubric names rule ids (EMB_003, DYN_001) and says they must be BENIGN.
// With the rubric last, those ids are still fresh when the model answers; with
// the rubric first, the code and the rule block sit between the instruction and
// the answer, and the binding is what gets lost.
//
// The ordering pinned below is the second experiment. The rubric is still
// hoisted - that is where the whole speedup comes from - but the binding is
// restated in "## 判定提醒" immediately before the answer, on the theory that
// one uncacheable line is cheaper than losing the rule ids to distance.
//
// It was measured against the old layout in an A/B/B/A run of the end-to-end
// audit (old, new, new, old; five findings each), timing every request from the
// server log:
//
//	cached prefix      62 -> 553 tokens
//	per call (median)  19.65s -> 13.24s
//	per audit          99.3s -> 71.0s  (1.40x over five findings; 1.57x once
//	                                    the first, still-cold call is amortised
//	                                    over a longer audit)
//
// The verdicts did not hold as cleanly as that number suggests, and the honest
// summary is that this run could not tell the two layouts apart. Three arms
// returned the baseline verdict set verbatim; the fourth - the *old* layout,
// the control - turned one EMB_003 finding into MALICIOUS and blocked the
// import. Its reason ("printing the dataset name may leak sensitive information
// into log files") contradicts the exemption the rubric states in as many
// words, and the other four findings in that same run quote the rubric
// correctly, so it is a sampling slip at temperature 0.1 rather than a reading
// failure. Same binary, same prompts: the two control arms logged identical
// prompt-eval token counts (844/805/927/690/890) and still disagreed.
//
// That control failure bounds what this test can claim. The unmodified prompt
// already flips roughly one finding in ten, so two clean runs of the new layout
// are the expected outcome under either hypothesis, and only a much larger
// sample could separate them. The speedup above is measured; the verdicts are
// only "not obviously worse". What the first experiment saw was different in
// kind - a broad shift, DYN_001 and most EMB_003 at once, reproduced twice -
// which is why the reminder is kept as insurance even though this run did not
// need it.
//
// Both halves are pinned for reasons that do not lean on that evidence: move
// the rubric back after the code and the cacheable prefix collapses from 945
// characters to 45, so every call pays to re-evaluate the entire rubric again;
// delete the reminder and nothing stands between a hoisted rubric and the first
// experiment's failure mode.
//
// Concurrency was measured too, and is not an alternative lever: with 8 prompts
// and num_predict 256, raising OLLAMA_NUM_PARALLEL from 1 to 4 changed aggregate
// throughput by nothing (165.4s vs 163.1s for the same 8 calls) and raising it
// to 8 made things worse (208.5s, and every call exceeded 142s - past the 120s
// request timeout, which under assist+failClosed turns each finding UNCERTAIN
// and blocks the import). The host is compute-bound; only doing less work helps.
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

	// The layout invariant. Each step is load-bearing: the rubric is hoisted so it
	// forms a cacheable prefix, the restated binding sits between the rule block
	// and the answer, and the bare-JSON demand stays nearest generation. See the
	// note on this test for what each position was measured to cost.
	order := []string{"判定优先级与安全边界", "## 代码片段", "规则说明", "## 判定提醒", "## 请回答", `{"verdict"`}
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

// TestFindingAnalysisPromptCachingInvariants pins the two properties that make
// hoisting the rubric worth its risk. Neither one shows up in a verdict, so
// without this test a later edit could quietly undo either of them and the only
// symptom would be a slower audit.
//
//  1. The rubric must precede the variable blocks. llama.cpp reuses only a
//     common prefix, so this one ordering fact is the entire speedup: the fixed
//     prefix is 945 characters when the rubric leads and 45 when it trails, and
//     the measured cached-token count follows (553 against 62 per call).
//  2. The restated binding must sit between the rule block and the answer, and
//     the benign exemptions must survive. The rubric names five rule ids and
//     binds them to BENIGN, and DYN_001 to BENIGN specifically is a written
//     requirement in section 2.2 of
//     .claude/specs/2026-09-18-codeaudit-risk-classification-and-precision-design.md,
//     not an incidental turn of phrase. A rewrite that dropped one of those ids
//     would still render, still parse, and still be wrong.
func TestFindingAnalysisPromptCachingInvariants(t *testing.T) {
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
		RequestID: "req-cache-1",
		FindingPayload: &FindingPayload{
			RuleID:      "EMB_003",
			Category:    "data_leak",
			Severity:    "low",
			Description: "print(dataset)",
			Target:      CodeTarget{FilePath: "train.py", Line: 8, CodeSnippet: "print(dataset)"},
		},
	}
	if _, err := b.HandleVerifyFinding(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedPrompt == "" {
		t.Fatal("server captured no prompt")
	}

	// 1. The cacheable prefix: everything before the first variable block is what
	// llama.cpp can reuse across findings. If the rubric slips back after the code
	// the prefix collapses to the fixed instruction line and the speedup is gone.
	rubricAt := strings.Index(capturedPrompt, "## 判定优先级与安全边界")
	codeAt := strings.Index(capturedPrompt, "## 代码片段")
	if rubricAt < 0 || codeAt < 0 {
		t.Fatalf("prompt is missing the rubric (at %d) or the code block (at %d)", rubricAt, codeAt)
	}
	if rubricAt > codeAt {
		t.Errorf("the rubric must precede the code block to stay cacheable: rubric at %d, code at %d", rubricAt, codeAt)
	}

	// 2. The restated binding. Both positions matter: after the rule block so it
	// follows the rule ids it names, and before the answer so it is still fresh
	// when the model commits to a verdict.
	reminderAt := strings.Index(capturedPrompt, "## 判定提醒")
	if reminderAt < 0 {
		t.Fatal("the restated binding (## 判定提醒) is missing; without it the verdicts regress")
	}
	rulesAt := strings.Index(capturedPrompt, "规则说明")
	if rulesAt < 0 || reminderAt < rulesAt {
		t.Errorf("the restated binding must follow the rule block: rules at %d, reminder at %d", rulesAt, reminderAt)
	}
	if answerAt := strings.Index(capturedPrompt, "## 请回答"); answerAt < 0 || reminderAt > answerAt {
		t.Errorf("the restated binding must precede the answer: reminder at %d, answer at %d", reminderAt, answerAt)
	}
	reminder := capturedPrompt[reminderAt:strings.Index(capturedPrompt, "## 请回答")]
	if !strings.Contains(reminder, "BENIGN") || !strings.Contains(reminder, "DYN_001") {
		t.Errorf("the restated binding is not restating anything:\n%s", reminder)
	}

	// 3. The benign exemptions, and the property that lets the rubric live inside a
	// fmt.Sprintf format string at all.
	for _, id := range []string{"EMB_001", "EMB_002", "EMB_003", "EMB_004", "DYN_001"} {
		if !strings.Contains(capturedPrompt, id) {
			t.Errorf("the prompt no longer binds %s to BENIGN", id)
		}
	}
	if n := strings.Count(capturedPrompt[:codeAt], "%"); n != 0 {
		t.Errorf("the cacheable prefix holds %d '%%' characters; it is a fmt.Sprintf format string, "+
			"so a stray verb would corrupt every rendered prompt", n)
	}
}
