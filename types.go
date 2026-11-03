package teellm

// CurrentProtocolVersion represents the supported protocol version.
const CurrentProtocolVersion = "teellm-protocol/v1"

// MaxRequestPayloadBytes bounds one request body (1MB).
const MaxRequestPayloadBytes = 1 << 20

// Status constants representing response status codes.
const (
	StatusSuccess            = "SUCCESS"
	StatusInvalidRequest     = "INVALID_REQUEST"
	StatusModelNotFound      = "MODEL_NOT_FOUND"
	StatusInternalError      = "INTERNAL_ERROR"
	StatusServiceUnavailable = "SERVICE_UNAVAILABLE"
)

// GenerateRequest is one inference request. The service treats prompt and
// options as opaque: it knows nothing about what the caller is asking the model
// to do.
type GenerateRequest struct {
	ProtocolVersion string         `json:"protocolVersion"`
	RequestID       string         `json:"requestId"`
	Model           string         `json:"model,omitempty"`
	Prompt          string         `json:"prompt"`
	Options         map[string]any `json:"options,omitempty"`
}

// GenerateResponse carries the model's raw text output.
type GenerateResponse struct {
	ProtocolVersion string `json:"protocolVersion"`
	RequestID       string `json:"requestId"`
	Status          string `json:"status"`
	ErrorMessage    string `json:"errorMessage,omitempty"`
	Model           string `json:"model,omitempty"`
	Text            string `json:"text,omitempty"`
}
