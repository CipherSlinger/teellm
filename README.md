# teellm

[![Go Reference](https://pkg.go.dev/badge/github.com/CipherSlinger/teellm.svg)](https://pkg.go.dev/github.com/CipherSlinger/teellm)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

`teellm` provides confidential AI inference capabilities over RFC 8998 ShangMi (SM2/SM3/SM4) TEE-TLS 1.3 channels. It is designed to secure communication between auditing agents or user workloads and LLM inference endpoints operating inside hardware-isolated Trusted Execution Environments (TEEs).

The project combines hardware-rooted remote attestation (supporting Hygon CSV, AMD SEV, and Intel SGX attestation evidence formats via `teetls`), strict SSRF defenses, an extensible backend adapter architecture (including native Ollama integration), and enterprise resilience patterns (circuit breakers, exponential backoff, and full jitter retries).

---

## Key Features

- **Confidential Inference over TEE-TLS 1.3**:
  - Encrypted channel secured using RFC 8998 ShangMi cipher suites (`TLS_SM4_GCM_SM3`, `0x00c6`) with SM2 key exchange, SM3 hashing, and SM4-GCM record protection.
  - Peer public keys are cryptographically bound to enclave attestation evidence, neutralizing Man-In-The-Middle (MITM) attacks.
- **Hardware-Rooted Remote Attestation**:
  - Seamlessly integrates with `github.com/CipherSlinger/teetls` to verify hardware evidence (such as Hygon CSV guest reports and certificates).
  - Measurement verification in Strict mode (enforcing exact launch hashes) or Permissive mode (logging discrepancies for security audits).
- **Extensible Backend Architecture**:
  - Modular `Backend` interface decoupling the network protocol from underlying LLM runtime engines.
  - Native `OllamaBackend` adapter supporting local model serving (e.g., `qwen2.5-coder:3b`), prompt templating, and structured JSON output parsing.
- **Production-Grade Resilience**:
  - **Circuit Breaker**: Detects downstream model failure or timeout spikes and enters Open/Half-Open states to avoid cascading failures.
  - **Exponential Backoff with Full Jitter**: Mitigates thundering-herd issues during retries.
  - **Strict SSRF Defenses**: Disallows plain HTTP, loopback/private CIDRs unless explicitly whitelisted, and cloud metadata IP ranges (`169.254.169.254`).
  - **Payload Guardrails**: Enforces 1MB request/response envelope bounds to prevent resource exhaustion.
- **Flexible Deployment**:
  - Usable directly as a Go client library in your applications.
  - Shippable as a standalone microservice daemon (`bin/teellm-service`) with CLI flags and readiness probing.

---

## Architecture Overview

```
 +-------------------------------------------------------------+
 |                         Client Node                         |
 |                                                             |
 |   +-----------------------------------------------------+   |
 |   |                   teellm.Client                     |   |
 |   |  - SSRF Validator       - Circuit Breaker           |   |
 |   |  - Full Jitter Retry    - Envelope Encoder/Decoder  |   |
 |   +-----------------------------------------------------+   |
 |                              |                              |
 |                              v                              |
 |   +-----------------------------------------------------+   |
 |   |                    teetls Client                    |   |
 |   |  - TLS 1.3 SM2/SM3/SM4 (RFC 8998)                   |   |
 |   |  - Attestation Evidence Verifier (CSV / SEV / SGX)  |   |
 |   +-----------------------------------------------------+   |
 +------------------------------|------------------------------+
                                |
                   RFC 8998 ShangMi TEE-TLS 1.3
                   Hardware-Attested Channel
                                |
 +------------------------------v------------------------------+
 |                  TEE Enclave (Confidential VM)              |
 |                                                             |
 |   +-----------------------------------------------------+   |
 |   |                    teetls Server                    |   |
 |   |  - Ephemeral SM2 Key Pair                           |   |
 |   |  - Evidence Provider (/dev/csv-guest ioctl)         |   |
 |   +-----------------------------------------------------+   |
 |                              |                              |
 |                              v                              |
 |   +-----------------------------------------------------+   |
 |   |                    teellm.Server                    |   |
 |   |  - HTTP/1.1 Endpoint (/v1/verify, /healthz)         |   |
 |   |  - 1MB Payload Limiter                              |   |
 |   +-----------------------------------------------------+   |
 |                              |                              |
 |                              v                              |
 |   +-----------------------------------------------------+   |
 |   |                 OllamaBackendAdapter                |   |
 |   |  - Prompt Synthesis     - Structured JSON Parser    |   |
 |   +-----------------------------------------------------+   |
 |                              |                              |
 |                              v                              |
 |   +-----------------------------------------------------+   |
 |   |               Local Ollama Daemon (Host)            |   |
 |   |               e.g. qwen2.5-coder:3b                 |   |
 |   +-----------------------------------------------------+   |
 +-------------------------------------------------------------+
```

---

## Package Layout

```
teellm/
├── circuit_breaker.go     # Thread-safe circuit breaker implementation
├── client.go              # TEE-LLM client SDK and configuration
├── cmd/
│   └── teellm-service/    # Standalone microservice daemon CLI
│       ├── main.go        # Entrypoint with flags, probe mode, and signals
│       └── main_test.go   # Integration tests for daemon probe mode
├── go.mod                 # Go module definition (go 1.22)
├── go.sum                 # Dependency checksums
├── LICENSE                # Apache-2.0 License
├── ollama_backend.go      # Backend adapter dispatching to Ollama API
├── ollama_backend_test.go # Unit tests for Ollama backend integration
├── README.md              # Project documentation
├── resilience_test.go     # Circuit breaker and jitter retry test suite
├── retry.go               # Exponential backoff with full jitter
├── server.go              # TEE-LLM HTTPS server and router
├── teellm_test.go         # End-to-end client-server and SSRF tests
├── teetls/                # Git submodule providing RFC 8998 TEE-TLS
├── types.go               # Protocol envelopes and data transfer objects
├── types_test.go          # Serialization and envelope validation tests
└── validator.go           # SSRF validator for target inference endpoints
```

---

## Usage as a Client Library

### 1. Initializing the Client

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/CipherSlinger/teellm"
	"github.com/CipherSlinger/teetls"
)

func main() {
	// Configure TEE-TLS verification parameters
	teeTLSConfig := &teetls.Config{
		Mode:                 teetls.ModeStrict,
		ExpectedMeasurements: []string{
			"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
	}

	// Initialize the TEE-LLM client
	client, err := teellm.NewClient(teellm.Config{
		Endpoint:                       "https://enclave.service.local:8443",
		Timeout:                        30 * time.Second,
		AllowedHosts:                   []string{"enclave.service.local"},
		TEETLS:                         teeTLSConfig,
		CircuitBreakerFailureThreshold: 3,
		CircuitBreakerCooldown:         30 * time.Second,
		MaxRetries:                     3,
		RetryBaseDelay:                 100 * time.Millisecond,
		RetryMaxDelay:                  2 * time.Second,
	})
	if err != nil {
		log.Fatalf("failed to initialize client: %v", err)
	}
	defer client.Close()

	// Verify health of target endpoint
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.HealthCheck(ctx); err != nil {
		log.Fatalf("service health check failed: %v", err)
	}
	fmt.Println("Connected to verified TEE enclave successfully.")
}
```

### 2. Submitting an Audit Finding for Verification

```go
req := &teellm.RequestEnvelope{
	ProtocolVersion: teellm.CurrentProtocolVersion,
	RequestID:       "req-001",
	Timestamp:       time.Now().Unix(),
	Nonce:           "random-nonce-1234",
	DeadlineMs:      5000,
	Action:          teellm.ActionVerifyFinding,
	ModelRef: &teellm.ModelReference{
		Name: "qwen2.5-coder:3b",
	},
	Policy: teellm.PolicyOptions{
		Mode: teellm.PolicyModeGate,
	},
	FindingPayload: &teellm.FindingPayload{
		RuleID:      "DYN_001",
		Category:    "DYNAMIC_EXEC",
		Severity:    "HIGH",
		Description: "Detected dynamic module import in Python code",
		Target: teellm.CodeTarget{
			FilePath:    "scripts/runner.py",
			Line:        42,
			CodeSnippet: "mod = __import__(module_name)",
		},
	},
}

resp, err := client.VerifyFinding(context.Background(), req)
if err != nil {
	log.Fatalf("verification error: %v", err)
}

fmt.Printf("Verdict: %s (Confidence: %.2f)\n", resp.Decision.Verdict, resp.Decision.Confidence)
fmt.Printf("Explanation: %s\n", resp.Decision.Explanation)
```

---

## Running as a Standalone Daemon (`teellm-service`)

`teellm-service` is an out-of-the-box daemon that wraps an upstream inference engine (such as Ollama) with a confidential RFC 8998 TEE-TLS 1.3 gateway.

### Building the Daemon

```bash
go build -o bin/teellm-service ./cmd/teellm-service
```

### Command Line Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-addr` | `:8443` | TCP address to listen on |
| `-ollama-url` | `http://127.0.0.1:11434` | Upstream Ollama HTTP endpoint |
| `-model` | `qwen2.5-coder:3b` | Default model used for inference |
| `-hrk` | `/root/taa/certs/hrk.cert` | Path to Hygon HRK root certificate |
| `-hsk-cek` | `/root/taa/certs/hsk_cek.cert` | Path to Hygon HSK/CEK certificate |
| `-mock-attestation`| `false` | Force mock attestation provider (for dev/CI) |
| `-probe` | `""` | Target HTTPS URL to probe for readiness and exit |

### Starting the Service

1. **Production Mode (with Hardware Attestation)**:
   ```bash
   ./bin/teellm-service \
       -addr :8443 \
       -ollama-url http://127.0.0.1:11434 \
       -model qwen2.5-coder:3b \
       -hrk /etc/teetls/certs/hrk.cert \
       -hsk-cek /etc/teetls/certs/hsk_cek.cert
   ```

2. **Development / CI Mode (Mock Attestation)**:
   ```bash
   ./bin/teellm-service \
       -addr 127.0.0.1:8443 \
       -mock-attestation=true
   ```

### Probing Readiness

You can use the built-in probe mode in health check scripts or container liveness probes:

```bash
./bin/teellm-service -probe https://127.0.0.1:8443
# Outputs: OK and exits with status code 0 on success
```

---

## Testing

Run the full test suite covering end-to-end handshakes, circuit breaking, SSRF defenses, and backend adaptors:

```bash
go test -v ./...
```

To run tests with race condition detection:

```bash
go test -race -v ./...
```

---

## License

This project is licensed under the [Apache 2.0 License](LICENSE).
