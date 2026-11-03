package platformagent

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Defaults applied by New when a field is left at zero.
const (
	DefaultDockerID          = "taa-teellm"
	DefaultHeartbeatInterval = 30 * time.Second
	DefaultLogFlushInterval  = 5 * time.Second
	DefaultLogBufferSize     = 2000
	DefaultHeartbeatSeconds  = 30
	DefaultLogFlushSeconds   = 5
	DefaultMaxLogBatchSize   = 100
	DefaultRequestTimeout    = 10 * time.Second
)

// Register retry ladder. It mirrors the shape of TAA's register retry -- double
// from a short delay up to a cap -- but it has no give-up window: registration
// is the only step that makes this instance exist as far as the platform is
// concerned, so it keeps trying for the life of the process.
const (
	registerRetryInitialDelay = 1 * time.Second
	registerRetryMaxDelay     = 60 * time.Second
	// registerFailureLogEvery throttles the stderr reminder once the ladder has
	// settled at its cap. Retrying forever at 60s must not mean one log line per
	// minute forever in the guest, so only every tenth attempt is printed.
	registerFailureLogEvery = 10
)

// Config configures the platform reporter.
type Config struct {
	// PlatformAddr is the platform base URL, e.g. http://172.16.10.181:18081.
	// Empty disables reporting entirely.
	PlatformAddr string
	// DockerID identifies this LLM instance on the platform.
	DockerID string
	// HeartbeatInterval is the liveness period. Default 30s.
	HeartbeatInterval time.Duration
	// LogFlushInterval is how often buffered logs are pushed. Default 5s.
	LogFlushInterval time.Duration
	// LogBufferSize is the ring capacity, and also the cap on the pending queue.
	LogBufferSize int
	// Logs is the shared buffer. When nil an internal one is created; the caller
	// keeps a reference so it can instrument the service.
	Logs *LogBuffer
	// Snapshot supplies the attestation material each registration reports.
	Snapshot SnapshotFunc
	// HTTPClient overrides the client used for every call. Tests inject one.
	HTTPClient *http.Client
}

// withDefaults fills zero fields and normalises the platform address.
func (c Config) withDefaults() Config {
	if c.DockerID == "" {
		c.DockerID = DefaultDockerID
	}
	if c.HeartbeatInterval <= 0 {
		c.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if c.LogFlushInterval <= 0 {
		c.LogFlushInterval = DefaultLogFlushInterval
	}
	if c.LogBufferSize <= 0 {
		c.LogBufferSize = DefaultLogBufferSize
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: DefaultRequestTimeout}
	}
	c.PlatformAddr = strings.TrimRight(strings.TrimSpace(c.PlatformAddr), "/")
	if c.PlatformAddr != "" && !strings.Contains(c.PlatformAddr, "://") {
		c.PlatformAddr = "http://" + c.PlatformAddr
	}
	return c
}

// validate checks the fields that cannot be defaulted.
func (c Config) validate() error {
	if strings.TrimSpace(c.DockerID) == "" {
		return errors.New("platformagent: dockerId is required")
	}
	if !strings.HasPrefix(c.PlatformAddr, "http://") && !strings.HasPrefix(c.PlatformAddr, "https://") {
		return fmt.Errorf("platformagent: unsupported platform address %q", c.PlatformAddr)
	}
	return nil
}
