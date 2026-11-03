package teellm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OllamaBackendConfig configures the OllamaBackend adapter.
type OllamaBackendConfig struct {
	Endpoint     string
	DefaultModel string
	Timeout      time.Duration
	HTTPClient   *http.Client
	Catalog      *ModelCatalog
}

// OllamaBackend implements teellm.Backend by dispatching prompts to an Ollama server.
type OllamaBackend struct {
	endpoint     string
	defaultModel string
	client       *http.Client
	catalog      *ModelCatalog
}

var _ Backend = (*OllamaBackend)(nil)

// NewOllamaBackend creates and initializes a new OllamaBackend instance.
func NewOllamaBackend(cfg OllamaBackendConfig) (*OllamaBackend, error) {
	endpoint := strings.TrimRight(cfg.Endpoint, "/")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:11434"
	}

	model := strings.TrimSpace(cfg.DefaultModel)
	if model == "" {
		model = "qwen2.5-coder:3b"
	}

	// A zero timeout is deliberate: generation on a CPU-only host takes as long
	// as it takes, and the caller's own context (or a configured deadline)
	// governs cancellation.
	timeout := cfg.Timeout
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: timeout,
		}
	}

	catalog := cfg.Catalog
	if catalog == nil {
		catalog = DefaultModelCatalog()
	}

	return &OllamaBackend{
		endpoint:     endpoint,
		defaultModel: model,
		client:       client,
		catalog:      catalog,
	}, nil
}

// HandleHealthCheck queries the Ollama /api/tags endpoint to verify daemon health,
// and optionally verifies model execution via a 1-token generation probe.
func (b *OllamaBackend) HandleHealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.endpoint+"/api/tags", nil)
	if err != nil {
		return fmt.Errorf("create ollama health check request: %w", err)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("ollama health check failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama health check returned status %d", resp.StatusCode)
	}

	// Active inference probe: verify that model tensor weights can actually be mapped and loaded.
	if b.defaultModel != "" {
		probePayload, err := json.Marshal(ollamaGenerateReq{
			Model:  b.defaultModel,
			Prompt: "ping",
			Stream: false,
			Options: map[string]any{
				"num_predict": 1,
			},
		})
		if err != nil {
			return fmt.Errorf("marshal ollama health check probe: %w", err)
		}

		genReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint+"/api/generate", bytes.NewReader(probePayload))
		if err != nil {
			return fmt.Errorf("create ollama generate probe request: %w", err)
		}
		genReq.Header.Set("Content-Type", "application/json")

		genResp, err := b.client.Do(genReq)
		if err != nil {
			return fmt.Errorf("ollama model %q inference check failed: %w", b.defaultModel, err)
		}
		defer genResp.Body.Close()

		if genResp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(genResp.Body)
			return fmt.Errorf("ollama model %q inference check returned status %d: %s", b.defaultModel, genResp.StatusCode, strings.TrimSpace(string(body)))
		}
	}

	return nil
}

// ollamaGenerateReq represents the JSON payload expected by Ollama's /api/generate endpoint.
type ollamaGenerateReq struct {
	Model   string         `json:"model"`
	Prompt  string         `json:"prompt"`
	Stream  bool           `json:"stream"`
	Options map[string]any `json:"options,omitempty"`
}

// ollamaGenerateResp represents the response body returned by Ollama's /api/generate endpoint.
type ollamaGenerateResp struct {
	Response string `json:"response"`
}

// Generate dispatches one prompt to Ollama.
//
// Options merge in order: the service's num_predict default, then the model
// catalog's profile, then the request's own options. The request wins so a
// caller can adjust generation without the service knowing why; the catalog
// still owns the defaults so a caller that sends nothing gets the same
// behaviour as before.
func (b *OllamaBackend) Generate(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	if req == nil || strings.TrimSpace(req.Prompt) == "" {
		return &GenerateResponse{
			ProtocolVersion: CurrentProtocolVersion,
			RequestID:       reqIDOrEmpty(req),
			Status:          StatusInvalidRequest,
			ErrorMessage:    "prompt must not be empty",
		}, errors.New("prompt must not be empty")
	}

	model := b.defaultModel
	if strings.TrimSpace(req.Model) != "" {
		model = strings.TrimSpace(req.Model)
	}

	genOptions := map[string]any{
		"num_predict": 200,
	}
	if b.catalog != nil {
		if profile := b.catalog.GetProfile(model); profile != nil && len(profile.Options) > 0 {
			for k, v := range profile.Options {
				genOptions[k] = v
			}
		}
	}
	for k, v := range req.Options {
		genOptions[k] = v
	}

	ollamaReqBody := ollamaGenerateReq{
		Model:   model,
		Prompt:  req.Prompt,
		Stream:  false,
		Options: genOptions,
	}

	reqBytes, err := json.Marshal(ollamaReqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal ollama request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint+"/api/generate", bytes.NewReader(reqBytes))
	if err != nil {
		return nil, fmt.Errorf("create ollama request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := b.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama generate request failed: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 512))
		return nil, fmt.Errorf("ollama returned HTTP %d: %s", httpResp.StatusCode, string(body))
	}

	var genResp ollamaGenerateResp
	if err := json.NewDecoder(httpResp.Body).Decode(&genResp); err != nil {
		return nil, fmt.Errorf("decode ollama response: %w", err)
	}

	return &GenerateResponse{
		ProtocolVersion: CurrentProtocolVersion,
		RequestID:       req.RequestID,
		Status:          StatusSuccess,
		Model:           model,
		Text:            genResp.Response,
	}, nil
}

func reqIDOrEmpty(req *GenerateRequest) string {
	if req != nil {
		return req.RequestID
	}
	return ""
}
