package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/CipherSlinger/teetls"
	"github.com/CipherSlinger/teellm"
)

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
		addr            string
		ollamaURL       string
		model           string
		hrkPath         string
		hskCekPath      string
		mockAttestation bool
		probeTarget     string
	)

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

	// Server mode
	log.Printf("starting teellm-service daemon on %s (upstream=%s, model=%s)", addr, ollamaURL, model)

	// Determine evidence provider
	var evidenceProvider teetls.EvidenceProvider
	if mockAttestation {
		log.Printf("using mock attestation provider (flag -mock-attestation enabled)")
		evidenceProvider = teetls.NewMockEvidenceProvider()
	} else if _, err := os.Stat("/dev/csv-guest"); err == nil {
		log.Printf("detected /dev/csv-guest; using Hygon hardware evidence provider (hrk=%s, hsk_cek=%s)", hrkPath, hskCekPath)
		evidenceProvider = teetls.NewHygonHardwareProvider("", hrkPath, hskCekPath)
	} else {
		log.Printf("warning: /dev/csv-guest not found; falling back to mock attestation provider for testing")
		evidenceProvider = teetls.NewMockEvidenceProvider()
	}

	teeTLSConfig := &teetls.Config{
		Mode:                          teetls.ModePermissive,
		EvidenceProvider:              evidenceProvider,
		InsecureSkipAttestationVerify: true,
	}

	backend, err := teellm.NewOllamaBackend(teellm.OllamaBackendConfig{
		Endpoint:     ollamaURL,
		DefaultModel: model,
		Timeout:      60 * time.Second,
	})
	if err != nil {
		log.Fatalf("failed to initialize Ollama backend: %v", err)
	}

	server, err := teellm.NewServer(teellm.ServerConfig{
		Addr:    addr,
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
