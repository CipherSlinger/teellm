package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/CipherSlinger/teellm"
	"github.com/CipherSlinger/teellm/platformagent"
	"github.com/CipherSlinger/teetls"
)

// ServerConfigSection defines the HTTP server runtime configuration.
type ServerConfigSection struct {
	Addr                string `json:"addr"`
	ReadTimeoutSeconds  int    `json:"readTimeoutSeconds"`
	WriteTimeoutSeconds int    `json:"writeTimeoutSeconds"`
	MaxResponseBytes    int    `json:"maxResponseBytes"`
}

// BackendConfigSection defines upstream LLM backend configuration.
type BackendConfigSection struct {
	Type             string `json:"type"`
	Endpoint         string `json:"endpoint"`
	DefaultModel     string `json:"defaultModel"`
	TimeoutSeconds   int    `json:"timeoutSeconds"`
	ModelCatalogPath string `json:"modelCatalogPath,omitempty"`
}

// AttestationConfigSection defines TEE hardware and attestation policy configuration.
type AttestationConfigSection struct {
	Mode           string `json:"mode"`
	Mock           bool   `json:"mock"`
	HRKCertPath    string `json:"hrkCertPath"`
	HSKCEKCertPath string `json:"hskCekCertPath"`
}

// PlatformConfigSection defines the outbound platform reporting configuration.
type PlatformConfigSection struct {
	Addr             string `json:"addr"`
	DockerID         string `json:"dockerId"`
	HeartbeatSeconds int    `json:"heartbeatSeconds"`
	LogFlushSeconds  int    `json:"logFlushSeconds"`
}

// ServiceConfigFile holds the complete JSON configuration structure for teellm-service.
type ServiceConfigFile struct {
	Server      ServerConfigSection      `json:"server"`
	Backend     BackendConfigSection     `json:"backend"`
	Attestation AttestationConfigSection `json:"attestation"`
	Platform    PlatformConfigSection    `json:"platform"`
}

// loadServiceConfig loads service configuration from the given file path,
// or returns standard defaults if path is empty.
func loadServiceConfig(path string) (*ServiceConfigFile, error) {
	defaultCfg := &ServiceConfigFile{
		Server: ServerConfigSection{
			Addr:                ":8443",
			ReadTimeoutSeconds:  60,
			WriteTimeoutSeconds: 60,
			MaxResponseBytes:    1048576,
		},
		Backend: BackendConfigSection{
			Type:           "ollama",
			Endpoint:       "http://127.0.0.1:11434",
			DefaultModel:   "qwen2.5-coder:3b",
			TimeoutSeconds: 60,
		},
		Attestation: AttestationConfigSection{
			Mode: "permissive",
			Mock: false,
			// Certificate paths stay empty by default: the service only relays the
			// chain it is given to its peer, and TAA verifies just the report's
			// embedded PEK signature, so a stock deployment ships no vendor files.
			// The fields remain for a peer that still checks the chain; -hrk and
			// -hsk-cek supply them.
		},
		Platform: PlatformConfigSection{
			DockerID:         "taa-teellm",
			HeartbeatSeconds: platformagent.DefaultHeartbeatSeconds,
			LogFlushSeconds:  platformagent.DefaultLogFlushSeconds,
		},
	}

	if path == "" {
		return defaultCfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	cfg := *defaultCfg
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config json: %w", err)
	}
	return &cfg, nil
}

// mergeConfigWithFlags overrides configuration values with explicitly passed CLI flags.
func mergeConfigWithFlags(cfg *ServiceConfigFile, explicitFlags map[string]bool, addr, ollamaURL, model, hrkPath, hskCekPath string, mockAttestation bool) {
	if explicitFlags["addr"] {
		cfg.Server.Addr = addr
	}
	if explicitFlags["ollama-url"] {
		cfg.Backend.Endpoint = ollamaURL
	}
	if explicitFlags["model"] {
		cfg.Backend.DefaultModel = model
	}
	if explicitFlags["hrk"] {
		cfg.Attestation.HRKCertPath = hrkPath
	}
	if explicitFlags["hsk-cek"] {
		cfg.Attestation.HSKCEKCertPath = hskCekPath
	}
	if explicitFlags["mock-attestation"] {
		cfg.Attestation.Mock = mockAttestation
	}
}

// probeHealthCheckTimeout is the budget one readiness probe attempt gets.
//
// It is far longer than an ordinary request timeout because /v1/llm/healthz is
// not a ping: the service answers it only once the model is loaded, which on a
// cold service is seconds of disk and memory work. The probe deploys run is the
// very thing that warms it, so the attempt is not retried: cutting it off
// cancels the load it was waiting on, and the next attempt starts from nothing.
const probeHealthCheckTimeout = 120 * time.Second

// probeContextTimeout bounds one readiness probe invocation. It sits just above
// the health budget so that a backend that never answers is reported as a
// health-check timeout rather than as an opaque cancellation.
const probeContextTimeout = probeHealthCheckTimeout + 10*time.Second

func runProbe(ctx context.Context, endpoint string) error {
	client := teetls.NewHTTPClient(&teetls.Config{
		Mode:                          teetls.ModePermissive,
		InsecureSkipAttestationVerify: true,
	})
	client.Timeout = probeHealthCheckTimeout
	defer client.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/v1/llm/healthz", nil)
	if err != nil {
		return fmt.Errorf("create probe request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("probe healthcheck: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("probe healthcheck returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func main() {
	var (
		configPath      string
		addr            string
		ollamaURL       string
		model           string
		hrkPath         string
		hskCekPath      string
		mockAttestation bool
		probeTarget     string
	)

	flag.StringVar(&configPath, "config", "", "Path to JSON configuration file")
	flag.StringVar(&addr, "addr", ":8443", "TCP address to listen on")
	flag.StringVar(&ollamaURL, "ollama-url", "http://127.0.0.1:11434", "Upstream Ollama HTTP endpoint")
	flag.StringVar(&model, "model", "qwen2.5-coder:3b", "Default LLM model name")
	flag.StringVar(&hrkPath, "hrk", "/root/taa/certs/hrk.cert", "Path to Hygon HRK root certificate")
	flag.StringVar(&hskCekPath, "hsk-cek", "/root/taa/certs/hsk_cek.cert", "Path to Hygon HSK/CEK certificate")
	flag.BoolVar(&mockAttestation, "mock-attestation", false, "Force mock attestation evidence provider")
	flag.StringVar(&probeTarget, "probe", "", "Probe a target TEE-LLM HTTPS endpoint for readiness and exit")
	flag.Parse()

	// Probe mode
	if probeTarget != "" {
		ctx, cancel := context.WithTimeout(context.Background(), probeContextTimeout)
		defer cancel()

		if err := runProbe(ctx, probeTarget); err != nil {
			fmt.Fprintf(os.Stderr, "probe failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("OK")
		os.Exit(0)
	}

	cfg, err := loadServiceConfig(configPath)
	if err != nil {
		log.Fatalf("failed to load service config: %v", err)
	}

	explicitFlags := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) {
		explicitFlags[f.Name] = true
	})
	mergeConfigWithFlags(cfg, explicitFlags, addr, ollamaURL, model, hrkPath, hskCekPath, mockAttestation)

	// Server mode
	// The same buffer is shared by the service's own instrumentation and the
	// reporter, so instrumented lines reach both stderr and the platform.
	logs := platformagent.NewLogBuffer(platformagent.DefaultLogBufferSize)
	logs.Add(platformagent.LevelInfo, "teellm", "starting teellm-service daemon on %s (upstream=%s, model=%s)", cfg.Server.Addr, cfg.Backend.Endpoint, cfg.Backend.DefaultModel)

	// Determine evidence provider
	var evidenceProvider teetls.EvidenceProvider
	if cfg.Attestation.Mock {
		logs.Add(platformagent.LevelWarn, "teellm", "using mock attestation provider (flag or config mock enabled)")
		evidenceProvider = teetls.NewMockEvidenceProvider()
	} else if _, err := os.Stat("/dev/csv-guest"); err == nil {
		logs.Add(platformagent.LevelInfo, "teellm", "detected /dev/csv-guest; using Hygon hardware evidence provider (hrk=%s, hsk_cek=%s)", cfg.Attestation.HRKCertPath, cfg.Attestation.HSKCEKCertPath)
		evidenceProvider = teetls.NewHygonHardwareProvider("", cfg.Attestation.HRKCertPath, cfg.Attestation.HSKCEKCertPath)
	} else {
		logs.Add(platformagent.LevelWarn, "teellm", "/dev/csv-guest not found; falling back to mock attestation provider for testing")
		evidenceProvider = teetls.NewMockEvidenceProvider()
	}

	attestationMode := teetls.ModePermissive
	if cfg.Attestation.Mode == string(teetls.ModeStrict) {
		attestationMode = teetls.ModeStrict
	}

	teeTLSConfig := &teetls.Config{
		Mode:                          attestationMode,
		EvidenceProvider:              evidenceProvider,
		InsecureSkipAttestationVerify: true,
	}

	agent, err := platformagent.New(platformagent.Config{
		PlatformAddr:      cfg.Platform.Addr,
		DockerID:          cfg.Platform.DockerID,
		HeartbeatInterval: time.Duration(cfg.Platform.HeartbeatSeconds) * time.Second,
		LogFlushInterval:  time.Duration(cfg.Platform.LogFlushSeconds) * time.Second,
		Logs:              logs,
		Snapshot: func(ctx context.Context) (*teellm.AttestationSnapshot, error) {
			return teellm.BuildAttestationSnapshot(ctx, teeTLSConfig)
		},
	})
	if err != nil {
		log.Fatalf("failed to initialize platform agent: %v", err)
	}

	// A zero backend timeout means no deadline. It must not be replaced by a
	// fallback: a CPU-only 7B model needs minutes to answer the first prompt, and a
	// fallback here turns that into a 500 the caller reads as "service
	// unavailable".
	backendTimeout := time.Duration(cfg.Backend.TimeoutSeconds) * time.Second

	catalog, err := teellm.LoadModelCatalog(cfg.Backend.ModelCatalogPath)
	if err != nil {
		log.Printf("warning: could not load model catalog from %q: %v (using defaults)", cfg.Backend.ModelCatalogPath, err)
	} else {
		log.Printf("loaded model catalog from %s (activeModel=%s, registered=%d)", cfg.Backend.ModelCatalogPath, catalog.ActiveModel, len(catalog.Models))
	}

	backend, err := teellm.NewOllamaBackend(teellm.OllamaBackendConfig{
		Endpoint:     cfg.Backend.Endpoint,
		DefaultModel: cfg.Backend.DefaultModel,
		Timeout:      backendTimeout,
		Catalog:      catalog,
	})
	if err != nil {
		log.Fatalf("failed to initialize Ollama backend: %v", err)
	}
	logs.Add(platformagent.LevelInfo, "teellm", "ollama backend ready: endpoint=%s model=%s", cfg.Backend.Endpoint, cfg.Backend.DefaultModel)

	server, err := teellm.NewServer(teellm.ServerConfig{
		Addr:    cfg.Server.Addr,
		TEETLS:  teeTLSConfig,
		Backend: backend,
	})
	if err != nil {
		log.Fatalf("failed to create teellm server: %v", err)
	}

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()

	if agent != nil {
		agent.Start(runCtx)
		defer agent.Stop()
		logs.Add(platformagent.LevelInfo, "teellm", "platform reporting enabled to %s", cfg.Platform.Addr)
	} else {
		logs.Add(platformagent.LevelInfo, "teellm", "platform reporting disabled (empty platform.addr)")
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	serverErrCh := make(chan error, 1)
	go func() {
		logs.Add(platformagent.LevelInfo, "teellm", "teellm-service listening on %s via RFC 8998 TEE-TLS 1.3", server.Addr())
		if err := server.Start(); err != nil {
			serverErrCh <- err
		}
	}()

	select {
	case sig := <-sigCh:
		logs.Add(platformagent.LevelInfo, "teellm", "received signal %v, shutting down teellm-service", sig)
		_ = server.Close()
		log.Printf("teellm-service stopped cleanly")
	case err := <-serverErrCh:
		log.Fatalf("teellm server error: %v", err)
	}
}
