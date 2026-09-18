package teellm

import (
	"encoding/json"
	"testing"
)

func TestRequestEnvelope_Serialization(t *testing.T) {
	temp := float32(0.2)
	req := RequestEnvelope{
		ProtocolVersion: CurrentProtocolVersion,
		RequestID:       "req-123",
		TaskID:          "task-456",
		Timestamp:       1789728000,
		Nonce:           "nonce-789",
		DeadlineMs:      30000,
		Auth: &AuthConfig{
			AuthType: "bearer",
			Token:    "secret-token-val",
		},
		ModelRef: &ModelReference{
			Name:             "qwen2.5:7b",
			Tag:              "latest",
			MinContextTokens: 4096,
		},
		Action: ActionVerifyFinding,
		FindingPayload: &FindingPayload{
			RuleID:      "RULE-SQLI-001",
			Category:    "SECURITY_SQLI",
			Severity:    "HIGH",
			Description: "Potential SQL injection vulnerability",
			Target: CodeTarget{
				FilePath:      "db.py",
				Line:          42,
				CodeSnippet:   "cursor.execute(f'SELECT {user_input}')",
				ContextBefore: "user_input = get_query()",
				ContextAfter:  "return cursor.fetchall()",
				ContentSHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			},
		},
		Policy: PolicyOptions{
			Mode:                 PolicyModeGate,
			EnableReasoningChain: true,
			Temperature:          &temp,
			MaxCompletionTokens:  1024,
		},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal request: %v", err)
	}

	// Verify JSON keys in raw map
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal raw map: %v", err)
	}

	expectedKeys := []string{
		"protocolVersion", "requestId", "taskId", "timestamp", "nonce",
		"deadlineMs", "auth", "modelRef", "action", "findingPayload", "policy",
	}
	for _, key := range expectedKeys {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing key in JSON: %s", key)
		}
	}

	var reqBack RequestEnvelope
	if err := json.Unmarshal(data, &reqBack); err != nil {
		t.Fatalf("Unmarshal request: %v", err)
	}

	if reqBack.ProtocolVersion != CurrentProtocolVersion {
		t.Errorf("version mismatch: got %s, want %s", reqBack.ProtocolVersion, CurrentProtocolVersion)
	}
	if reqBack.FindingPayload == nil || reqBack.FindingPayload.Target.Line != 42 {
		t.Fatalf("line mismatch or nil payload: %v", reqBack.FindingPayload)
	}
	if reqBack.FindingPayload.Target.FilePath != "db.py" {
		t.Errorf("filePath mismatch: %s", reqBack.FindingPayload.Target.FilePath)
	}
	if reqBack.Auth == nil || reqBack.Auth.Token != "secret-token-val" {
		t.Errorf("auth token mismatch: %v", reqBack.Auth)
	}
	if reqBack.ModelRef == nil || reqBack.ModelRef.Name != "qwen2.5:7b" {
		t.Errorf("modelRef name mismatch: %v", reqBack.ModelRef)
	}
	if reqBack.Policy.Mode != PolicyModeGate {
		t.Errorf("policy mode mismatch: %s", reqBack.Policy.Mode)
	}
	if reqBack.Policy.Temperature == nil || *reqBack.Policy.Temperature != 0.2 {
		t.Errorf("policy temperature mismatch: %v", reqBack.Policy.Temperature)
	}
}

func TestResponseEnvelope_Serialization(t *testing.T) {
	resp := ResponseEnvelope{
		ProtocolVersion: CurrentProtocolVersion,
		RequestID:       "req-123",
		Status:          StatusSuccess,
		ErrorMessage:    "",
		Decision: &DecisionResult{
			Verdict:              VerdictMalicious,
			Confidence:           0.98,
			RiskLevel:            "HIGH",
			ReasonCode:           "SQL_INJECTION_CONFIRMED",
			Explanation:          "User input is directly interpolated into SQL query string.",
			SuggestedRemediation: "Use parameterized query with placeholders.",
			ReasoningChain:       "Step 1: Check source. Step 2: Check sink.",
		},
		Metrics: &Metrics{
			LatencyMs:        150,
			PromptTokens:     420,
			CompletionTokens: 90,
		},
		EngineInfo: &EngineInfo{
			Backend:     "qwen-service",
			ModelLoaded: "qwen2.5:7b",
			ModelDigest: "sha256:abc123def456",
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal response: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal response to raw map: %v", err)
	}

	expectedKeys := []string{
		"protocolVersion", "requestId", "status", "decision", "metrics", "engineInfo",
	}
	for _, key := range expectedKeys {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing key in JSON: %s", key)
		}
	}

	var respBack ResponseEnvelope
	if err := json.Unmarshal(data, &respBack); err != nil {
		t.Fatalf("Unmarshal response back: %v", err)
	}

	if respBack.ProtocolVersion != CurrentProtocolVersion {
		t.Errorf("version mismatch: got %s, want %s", respBack.ProtocolVersion, CurrentProtocolVersion)
	}
	if respBack.Decision == nil || respBack.Decision.Verdict != VerdictMalicious {
		t.Fatalf("verdict mismatch: %v", respBack.Decision)
	}
	if respBack.Metrics == nil || respBack.Metrics.LatencyMs != 150 {
		t.Fatalf("metrics latency mismatch: %v", respBack.Metrics)
	}
	if respBack.EngineInfo == nil || respBack.EngineInfo.Backend != "qwen-service" {
		t.Fatalf("engineInfo backend mismatch: %v", respBack.EngineInfo)
	}
}

func TestRequestEnvelope_MinimalOmitted(t *testing.T) {
	req := RequestEnvelope{
		ProtocolVersion: CurrentProtocolVersion,
		RequestID:       "req-health-001",
		Timestamp:       1789728000,
		Nonce:           "nonce-minimal",
		DeadlineMs:      5000,
		Action:          ActionHealthCheck,
		Policy: PolicyOptions{
			Mode: PolicyModeGate,
		},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal minimal request: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal raw map: %v", err)
	}

	disallowedKeys := []string{"taskId", "auth", "modelRef", "findingPayload"}
	for _, key := range disallowedKeys {
		if _, ok := raw[key]; ok {
			t.Errorf("expected key %s to be omitted, but found in JSON: %v", key, raw[key])
		}
	}

	requiredKeys := []string{"protocolVersion", "requestId", "timestamp", "nonce", "deadlineMs", "action", "policy"}
	for _, key := range requiredKeys {
		if _, ok := raw[key]; !ok {
			t.Errorf("expected required key %s in JSON", key)
		}
	}
}

func TestResponseEnvelope_MinimalAndFailure(t *testing.T) {
	resp := ResponseEnvelope{
		ProtocolVersion: CurrentProtocolVersion,
		RequestID:       "req-fail-001",
		Status:          StatusServiceUnavailable,
		ErrorMessage:    "circuit breaker OPEN: inference engine down",
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal error response: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal error response to raw map: %v", err)
	}

	disallowedKeys := []string{"decision", "metrics", "engineInfo"}
	for _, key := range disallowedKeys {
		if _, ok := raw[key]; ok {
			t.Errorf("expected key %s to be omitted from error response, but found: %v", key, raw[key])
		}
	}

	if raw["errorMessage"] != "circuit breaker OPEN: inference engine down" {
		t.Errorf("unexpected errorMessage: %v", raw["errorMessage"])
	}
	if raw["status"] != StatusServiceUnavailable {
		t.Errorf("unexpected status: %v", raw["status"])
	}
}

func TestPolicyOptions_ZeroTemperature(t *testing.T) {
	zeroTemp := float32(0.0)
	opts := PolicyOptions{
		Mode:        PolicyModeGate,
		Temperature: &zeroTemp,
	}

	data, err := json.Marshal(opts)
	if err != nil {
		t.Fatalf("Marshal policy options: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal policy options: %v", err)
	}

	val, ok := raw["temperature"]
	if !ok {
		t.Fatalf("expected temperature to be serialized even when 0.0")
	}
	if val.(float64) != 0.0 {
		t.Errorf("expected temperature 0.0, got %v", val)
	}
}
