package teellm

// CurrentProtocolVersion represents the supported protocol version.
const CurrentProtocolVersion = "teellm-protocol/v1"

// Action constants defining supported operations.
const (
	ActionVerifyFinding = "VERIFY_FINDING"
	ActionAnalyzeFile   = "ANALYZE_FILE"
	ActionHealthCheck   = "HEALTH_CHECK"
)

// Verdict constants representing semantic decision outcomes.
const (
	VerdictMalicious  = "MALICIOUS"
	VerdictSuspicious = "SUSPICIOUS"
	VerdictBenign     = "BENIGN"
	VerdictUncertain  = "UNCERTAIN"
)

// Status constants representing response status codes.
const (
	StatusSuccess            = "SUCCESS"
	StatusInvalidRequest     = "INVALID_REQUEST"
	StatusUnauthorized       = "UNAUTHORIZED"
	StatusServiceOverloaded  = "SERVICE_OVERLOADED"
	StatusModelNotFound      = "MODEL_NOT_FOUND"
	StatusInternalError      = "INTERNAL_ERROR"
	StatusServiceUnavailable = "SERVICE_UNAVAILABLE"
)

// PolicyMode constants defining the enforcement mode.
const (
	PolicyModeGate   = "gate"
	PolicyModeAssist = "assist"
)

// AuthConfig defines authentication credentials for the inference endpoint.
type AuthConfig struct {
	AuthType string `json:"authType"`
	Token    string `json:"token,omitempty"`
}

// ModelReference identifies the model to be targeted for inference.
type ModelReference struct {
	Name             string `json:"name"`
	Tag              string `json:"tag,omitempty"`
	MinContextTokens int    `json:"minContextTokens,omitempty"`
}

// CodeTarget identifies the location and slice of code under audit.
type CodeTarget struct {
	FilePath      string `json:"filePath"`
	Line          int    `json:"line"`
	CodeSnippet   string `json:"codeSnippet"`
	ContextBefore string `json:"contextBefore,omitempty"`
	ContextAfter  string `json:"contextAfter,omitempty"`
	ContentSHA256 string `json:"contentSha256,omitempty"`
}

// FindingPayload contains details of a static finding being verified.
type FindingPayload struct {
	RuleID      string     `json:"ruleId"`
	Category    string     `json:"category"`
	Severity    string     `json:"severity"`
	Description string     `json:"description"`
	Target      CodeTarget `json:"target"`
}

// PolicyOptions configures the runtime audit policy.
type PolicyOptions struct {
	Mode                 string   `json:"mode"`
	EnableReasoningChain bool     `json:"enableReasoningChain,omitempty"`
	Temperature          *float32 `json:"temperature,omitempty"`
	MaxCompletionTokens  int      `json:"maxCompletionTokens,omitempty"`
}

// RequestEnvelope is the standardized request structure for teellm-protocol/v1.
type RequestEnvelope struct {
	ProtocolVersion string          `json:"protocolVersion"`
	RequestID       string          `json:"requestId"`
	TaskID          string          `json:"taskId,omitempty"`
	Timestamp       int64           `json:"timestamp"`
	Nonce           string          `json:"nonce"`
	DeadlineMs      int64           `json:"deadlineMs"`
	Auth            *AuthConfig     `json:"auth,omitempty"`
	ModelRef        *ModelReference `json:"modelRef,omitempty"`
	Action          string          `json:"action"`
	FindingPayload  *FindingPayload `json:"findingPayload,omitempty"`
	Policy          PolicyOptions   `json:"policy"`
}

// DecisionResult represents the semantic arbitration verdict.
type DecisionResult struct {
	Verdict              string  `json:"verdict"`
	Confidence           float32 `json:"confidence"`
	RiskLevel            string  `json:"riskLevel"`
	ReasonCode           string  `json:"reasonCode"`
	Explanation          string  `json:"explanation"`
	SuggestedRemediation string  `json:"suggestedRemediation,omitempty"`
	ReasoningChain       string  `json:"reasoningChain,omitempty"`
}

// Metrics tracks the execution overhead of the inference request.
type Metrics struct {
	LatencyMs        int64 `json:"latencyMs"`
	PromptTokens     int   `json:"promptTokens"`
	CompletionTokens int   `json:"completionTokens"`
}

// EngineInfo reports metadata about the backend model and runtime.
type EngineInfo struct {
	Backend     string `json:"backend"`
	ModelLoaded string `json:"modelLoaded"`
	ModelDigest string `json:"modelDigest,omitempty"`
}

// ResponseEnvelope is the standardized response structure for teellm-protocol/v1.
type ResponseEnvelope struct {
	ProtocolVersion string          `json:"protocolVersion"`
	RequestID       string          `json:"requestId"`
	Status          string          `json:"status"`
	ErrorMessage    string          `json:"errorMessage,omitempty"`
	Decision        *DecisionResult `json:"decision,omitempty"`
	Metrics         *Metrics        `json:"metrics,omitempty"`
	EngineInfo      *EngineInfo     `json:"engineInfo,omitempty"`
}
