package teellm

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// ModelProfile defines configuration, metadata, and generation options for a model.
type ModelProfile struct {
	Family           string         `json:"family,omitempty"`
	ParameterSize    string         `json:"parameterSize,omitempty"`
	WeightSizeMB     int            `json:"weightSizeMB,omitempty"`
	RecommendedRAMGB int            `json:"recommendedRamGB,omitempty"`
	TimeoutSeconds   int            `json:"timeoutSeconds,omitempty"`
	Options          map[string]any `json:"options,omitempty"`
	Description      string         `json:"description,omitempty"`
}

// TimeoutDuration returns the configured timeout duration, or the fallback if unspecified.
func (p *ModelProfile) TimeoutDuration(fallback time.Duration) time.Duration {
	if p != nil && p.TimeoutSeconds > 0 {
		return time.Duration(p.TimeoutSeconds) * time.Second
	}
	return fallback
}

// ModelCatalog holds all registered model profiles and active model selection.
type ModelCatalog struct {
	Version     string                  `json:"version"`
	ActiveModel string                  `json:"activeModel"`
	Models      map[string]ModelProfile `json:"models"`
	mu          sync.RWMutex
}

// DefaultModelCatalog returns an initialized standard fallback catalog.
func DefaultModelCatalog() *ModelCatalog {
	return &ModelCatalog{
		Version:     "1.0",
		ActiveModel: "qwen2.5-coder:3b",
		Models: map[string]ModelProfile{
			"qwen2.5-coder:0.5b": {
				Family:         "qwen2.5-coder",
				ParameterSize:  "0.5B",
				TimeoutSeconds: 30,
				Options: map[string]any{
					"num_ctx":     2048,
					"temperature": 0.1,
					"num_predict": 160,
				},
			},
			"qwen2.5-coder:1.5b": {
				Family:         "qwen2.5-coder",
				ParameterSize:  "1.5B",
				TimeoutSeconds: 45,
				Options: map[string]any{
					"num_ctx":     2048,
					"temperature": 0.1,
					"num_predict": 200,
				},
			},
			"qwen2.5-coder:3b": {
				Family:         "qwen2.5-coder",
				ParameterSize:  "3B",
				TimeoutSeconds: 60,
				Options: map[string]any{
					"num_ctx":     4096,
					"temperature": 0.1,
					"num_predict": 256,
				},
			},
			"qwen2.5-coder:7b": {
				Family:         "qwen2.5-coder",
				ParameterSize:  "7B",
				TimeoutSeconds: 90,
				Options: map[string]any{
					"num_ctx":     4096,
					"temperature": 0.1,
					"num_predict": 300,
				},
			},
			"qwen3:8b": {
				Family:         "qwen3",
				ParameterSize:  "8B",
				TimeoutSeconds: 120,
				Options: map[string]any{
					"num_ctx":     4096,
					"temperature": 0.1,
					"think":       false,
					"num_predict": 300,
				},
			},
			"qwen3:14b": {
				Family:         "qwen3",
				ParameterSize:  "14B",
				TimeoutSeconds: 180,
				Options: map[string]any{
					"num_ctx":     8192,
					"temperature": 0.1,
					"think":       false,
					"num_predict": 512,
				},
			},
		},
	}
}

// LoadModelCatalog reads and decodes a ModelCatalog from a JSON file.
// If the path is empty or unreadable, it returns a default catalog alongside the error.
func LoadModelCatalog(path string) (*ModelCatalog, error) {
	fallback := DefaultModelCatalog()
	if strings.TrimSpace(path) == "" {
		return fallback, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fallback, fmt.Errorf("read model catalog %q: %w", path, err)
	}

	var catalog ModelCatalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		return fallback, fmt.Errorf("parse model catalog json: %w", err)
	}

	if catalog.Models == nil {
		catalog.Models = make(map[string]ModelProfile)
	}
	return &catalog, nil
}

// cloneModelProfile returns a copy of the profile with its Options map cloned defensively.
func cloneModelProfile(p *ModelProfile) *ModelProfile {
	if p == nil {
		return nil
	}
	res := *p
	if p.Options != nil {
		res.Options = make(map[string]any, len(p.Options))
		for k, v := range p.Options {
			res.Options[k] = v
		}
	}
	return &res
}

// GetProfile retrieves the profile for a given model name, or a default fallback.
func (c *ModelCatalog) GetProfile(modelName string) *ModelProfile {
	if c == nil {
		return &ModelProfile{
			Options: map[string]any{
				"temperature": 0.1,
				"num_predict": 200,
			},
		}
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	trimmed := strings.TrimSpace(modelName)
	if p, ok := c.Models[trimmed]; ok {
		return cloneModelProfile(&p)
	}

	// Case-insensitive match
	for k, v := range c.Models {
		if strings.EqualFold(k, trimmed) {
			return cloneModelProfile(&v)
		}
	}

	// Safe fallback profile for unregistered models
	return &ModelProfile{
		TimeoutSeconds: 60,
		Options: map[string]any{
			"temperature": 0.1,
			"num_predict": 200,
		},
	}
}
