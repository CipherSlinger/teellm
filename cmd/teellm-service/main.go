package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/CipherSlinger/teellm"
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
	Type           string `json:"type"`
	Endpoint       string `json:"endpoint"`
	DefaultModel   string `json:"defaultModel"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

// AttestationConfigSection defines TEE hardware and attestation policy configuration.
type AttestationConfigSection struct {
	Mode           string `json:"mode"`
	Mock           bool   `json:"mock"`
	HRKCertPath    string `json:"hrkCertPath"`
	HSKCEKCertPath string `json:"hskCekCertPath"`
}

// ServiceConfigFile holds the complete JSON configuration structure for teellm-service.
type ServiceConfigFile struct {
	Server      ServerConfigSection      `json:"server"`
	Backend     BackendConfigSection     `json:"backend"`
	Attestation AttestationConfigSection `json:"attestation"`
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
			Mode:           "permissive",
			Mock:           false,
			HRKCertPath:    "/root/taa/certs/hrk.cert",
			HSKCEKCertPath: "/root/taa/certs/hsk_cek.cert",
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

func runProbe(ctx context.Context, endpoint string) error {
	client, err := teellm.NewClient(teellm.Config{
		Endpoint: endpoint,
		Timeout:  5 * time.Second,
		TEETLS: &teetls.Config{
			Mode:                          teetls.ModePermissive,
			InsecureSkipAttestationVerify: true,
		},
	})
	if err != nil {
		return fmt.Errorf("create probe client: %w", err)
	}
	defer client.Close()

	if err := client.HealthCheck(ctx); err != nil {
		return fmt.Errorf("probe healthcheck: %w", err)
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
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
	log.Printf("starting teellm-service daemon on %s (upstream=%s, model=%s)", cfg.Server.Addr, cfg.Backend.Endpoint, cfg.Backend.DefaultModel)

	// Determine evidence provider
	var evidenceProvider teetls.EvidenceProvider
	if cfg.Attestation.Mock {
		log.Printf("using mock attestation provider (flag or config mock enabled)")
		evidenceProvider = teetls.NewMockEvidenceProvider()
	} else if _, err := os.Stat("/dev/csv-guest"); err == nil {
		log.Printf("detected /dev/csv-guest; using Hygon hardware evidence provider (hrk=%s, hsk_cek=%s)", cfg.Attestation.HRKCertPath, cfg.Attestation.HSKCEKCertPath)
		evidenceProvider = teetls.NewHygonHardwareProvider("", cfg.Attestation.HRKCertPath, cfg.Attestation.HSKCEKCertPath)
	} else {
		log.Printf("warning: /dev/csv-guest not found; falling back to mock attestation provider for testing")
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

	backendTimeout := time.Duration(cfg.Backend.TimeoutSeconds) * time.Second
	if backendTimeout <= 0 {
		backendTimeout = 60 * time.Second
	}

	backend, err := teellm.NewOllamaBackend(teellm.OllamaBackendConfig{
		Endpoint:     cfg.Backend.Endpoint,
		DefaultModel: cfg.Backend.DefaultModel,
		Timeout:      backendTimeout,
	})
	if err != nil {
		log.Fatalf("failed to initialize Ollama backend: %v", err)
	}

	server, err := teellm.NewServer(teellm.ServerConfig{
		Addr:    cfg.Server.Addr,
		TEETLS:  teeTLSConfig,
		Backend: backend,
	})
	if err != nil {
		log.Fatalf("failed to create teellm server: %v", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	serverErrCh := make(chan error, 1)
	go func() {
		log.Printf("teellm-service listening on %s via RFC 8998 TEE-TLS 1.3", server.Addr())
		if err := server.Start(); err != nil {
			serverErrCh <- err
		}
	}()

	select {
	case sig := <-sigCh:
		log.Printf("received signal %v, shutting down teellm-service...", sig)
		_ = server.Close()
		log.Printf("teellm-service stopped cleanly")
	case err := <-serverErrCh:
		log.Fatalf("teellm server error: %v", err)
	}
}
