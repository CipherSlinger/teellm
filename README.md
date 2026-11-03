# teellm

[![Go Reference](https://pkg.go.dev/badge/github.com/CipherSlinger/teellm.svg)](https://pkg.go.dev/github.com/CipherSlinger/teellm)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

`teellm` provides confidential AI inference over RFC 8998 ShangMi (SM2/SM3/SM4) TEE-TLS 1.3 channels. It runs inside a hardware-isolated Trusted Execution Environment (TEE) and turns a prompt into the model's raw text.

The project combines hardware-rooted remote attestation (Hygon CSV evidence, verified through `teetls`) with a thin inference gateway in front of a local Ollama daemon. The gateway is deliberately task-agnostic: prompts, sampling options and the interpretation of the model's output all belong to the caller.

---

## Key Features

- **Confidential Inference over TEE-TLS 1.3**:
  - Encrypted channel secured using RFC 8998 ShangMi cipher suites (`TLS_SM4_GCM_SM3`, `0x00c6`) with SM2 key exchange, SM3 hashing, and SM4-GCM record protection.
  - Peer public keys are cryptographically bound to enclave attestation evidence, neutralizing Man-In-The-Middle (MITM) attacks.
- **Hardware-Rooted Remote Attestation**:
  - Seamlessly integrates with `github.com/CipherSlinger/teetls` to verify hardware evidence (such as Hygon CSV guest reports and certificates).
  - Measurement verification in Strict mode (enforcing exact launch hashes) or Permissive mode (logging discrepancies for security audits).
- **Task-Agnostic Backend Interface**:
  - The `Backend` interface is just `Generate` + `HandleHealthCheck`, decoupled from the wire protocol; the service never inspects the prompt or parses the model's output.
- **Native Ollama Adapter**:
  - `OllamaBackend` forwards the prompt and the caller's `options` to Ollama unchanged, applying only the service default and the model catalog's profile as fallbacks.
- **Payload Guardrails**:
  - Enforces a 1MB request body bound to prevent resource exhaustion.
- **Standalone Daemon**:
  - Shippable as a standalone microservice daemon (`bin/teellm-service`) with CLI flags and a readiness probe.

---

## Architecture Overview

```
 +-------------------------------------------------------------+
 |                         Client Node                         |
 |                                                             |
 |   +-----------------------------------------------------+   |
 |   |            TEE-TLS Client (e.g. TAA)                |   |
 |   |  - Builds the prompt    - Consumes the raw text     |   |
 |   |  - teetls.NewHTTPClient                             |   |
 |   +-----------------------------------------------------+   |
 |                              |                              |
 |                              v                              |
 |   +-----------------------------------------------------+   |
 |   |                    teetls Client                    |   |
 |   |  - TLS 1.3 SM2/SM3/SM4 (RFC 8998)                   |   |
 |   |  - Attestation Evidence Verifier (Hygon CSV)        |   |
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
 |   |  - HTTP/1.1 Endpoint                                |   |
 |   |  - 1MB Payload Limiter                              |   |
 |   |  - /v1/llm/generate, /v1/llm/healthz,               |   |
 |   |    /v1/llm/getAttestation                           |   |
 |   +-----------------------------------------------------+   |
 |                              |                              |
 |                              v                              |
 |   +-----------------------------------------------------+   |
 |   |                 OllamaBackend                       |   |
 |   |  - Options Merge / Ollama Dispatch                  |   |
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
├── attestation.go          # getAttestation endpoint and CSV report snapshot
├── attestation_test.go     # Attestation endpoint tests
├── cmd/
│   └── teellm-service/     # Standalone microservice daemon CLI
│       ├── main.go         # Entrypoint with flags, probe mode, and signals
│       └── main_test.go    # Config, flag and probe mode tests
├── configs/                # JSON configuration for the daemon
│   ├── models.json
│   ├── teellm-docker.json
│   └── teellm-production.json
├── deploy/                 # Deployment scripts
│   └── start.sh
├── go.mod                  # Go module definition (go 1.22)
├── go.sum                  # Dependency checksums
├── LICENSE                 # Apache-2.0 License
├── model_catalog.go        # Model profiles and per-model option defaults
├── model_catalog_test.go   # Catalog loading and fallback tests
├── models/                 # Offline Ollama runtime and model store
│   └── ollama/
├── ollama_backend.go       # Backend adapter dispatching to Ollama
├── ollama_backend_test.go  # Option merge and health check tests
├── platformagent/          # Outbound registration, heartbeat and log reporting
│   ├── attestation.go
│   ├── config.go
│   ├── heartbeat.go
│   ├── logs.go
│   ├── logs_test.go
│   ├── register.go
│   ├── reporter.go
│   └── reporter_test.go
├── README.md               # Project documentation
├── server.go               # TEE-TLS HTTPS server and router
├── server_test.go          # Generate endpoint tests
├── teetls/                 # Git submodule providing RFC 8998 TEE-TLS
└── types.go                # Protocol request and response types
```

---

## Wire Protocol

All three endpoints share the single TEE-TLS 1.3 listener on `:8443`.

| Path | Method | Purpose |
|------|--------|---------|
| `/v1/llm/generate` | POST | Generic inference: prompt in, raw text out |
| `/v1/llm/healthz` | GET | Readiness: runs a one-token inference on the default model, so a cold or not-yet-loaded model answers 503 |
| `/v1/llm/getAttestation` | POST | Platform fetches the CSV report and the certificate it is bound to |

The service knows nothing about the caller's task. Sampling options
(`temperature`, `num_predict`, ...) travel in the request's `options` object and
override the model catalog's profile defaults; whatever they are, the service
passes them through to Ollama unchanged.

Request and response:

```json
{"protocolVersion":"teellm-protocol/v1","requestId":"r1","model":"qwen2.5-coder:3b","prompt":"...","options":{"temperature":0}}
```

```json
{"protocolVersion":"teellm-protocol/v1","requestId":"r1","status":"SUCCESS","model":"qwen2.5-coder:3b","text":"..."}
```

- `model` may be omitted; the service then uses its configured default model.
- Options merge in this order: the service's `num_predict` default, then the
  model catalog's profile for the chosen model, then the request's own
  `options`. The request wins.
- Application-level failures return the same envelope with `status` and
  `errorMessage` set. The protocol defines `INVALID_REQUEST`, `MODEL_NOT_FOUND`,
  `INTERNAL_ERROR` and `SERVICE_UNAVAILABLE`; the service currently emits
  `INVALID_REQUEST` for malformed JSON, an empty prompt or a body over 1MB, and
  `INTERNAL_ERROR` when the backend fails. Two earlier checks bypass the
  envelope: a non-POST method and an unreadable request body return plain-text
  `http.Error` responses (405 and 400 respectively).

There is no client SDK: callers use `teetls.NewHTTPClient` and POST JSON
directly.

```go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/CipherSlinger/teetls"
)

func main() {
	// Generation can take minutes on a CPU-only host; teetls.NewHTTPClient
	// defaults to a 10-second timeout, so raise it explicitly.
	client := teetls.NewHTTPClient(&teetls.Config{
		Mode:                 teetls.ModeStrict,
		ExpectedMeasurements: []string{"<SM3 measurement hex>"},
		Timeout:              120 * time.Second,
	})

	body, err := json.Marshal(map[string]any{
		"protocolVersion": "teellm-protocol/v1",
		"requestId":       "r1",
		"prompt":          "Return the word OK.",
		"options":         map[string]any{"temperature": 0},
	})
	if err != nil {
		log.Fatal(err)
	}

	resp, err := client.Post("https://enclave.service.local:8443/v1/llm/generate", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	fmt.Printf("%s\n", data)
}
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
| `-config` | `""` | Path to a JSON configuration file (model catalog, platform reporting, backend) |
| `-addr` | `:8443` | TCP address to listen on |
| `-ollama-url` | `http://127.0.0.1:11434` | Upstream Ollama HTTP endpoint |
| `-model` | `qwen2.5-coder:3b` | Default model used for inference |
| `-hrk` | `/root/taa/certs/hrk.cert` | Path to Hygon HRK root certificate |
| `-hsk-cek` | `/root/taa/certs/hsk_cek.cert` | Path to Hygon HSK/CEK certificate |
| `-mock-attestation`| `false` | Force mock attestation provider (for dev/CI) |
| `-probe` | `""` | Target HTTPS URL to probe for readiness and exit |

### HTTP Surface

All three endpoints share the single TEE-TLS 1.3 listener on `:8443`.

| Path | Method | Purpose |
|------|--------|---------|
| `/v1/llm/generate` | POST | Generic inference: prompt in, raw text out |
| `/v1/llm/healthz` | GET | Readiness: runs a one-token inference on the default model, so a cold or not-yet-loaded model answers 503 |
| `/v1/llm/getAttestation` | POST | Platform fetches the CSV report and the certificate it is bound to |

`/v1/llm/getAttestation` is mounted on the same TEE-TLS listener as the other
two, so a plain HTTP client cannot call it: the platform must speak ShangMi TLS
(RFC 8998). The response still carries `certificate` in PEM so the caller can
check `SM3(certificate public key) == userdata[:32]` against the report it just
received, instead of relying on whichever certificate the handshake served.
Requests with an empty `requestId` are rejected with 400.

### Platform Reporting

Set `platform.addr` to enable outbound plaintext JSON reporting to the control
platform. The instance registers once at startup (retrying with backoff until it
succeeds -- it never gives up), sends a liveness heartbeat every
`heartbeatSeconds`, and pushes buffered logs every `logFlushSeconds`. If a
heartbeat or a log push fails because the platform cannot be reached at all, the
instance registers again; there is no periodic re-registration. An empty
`platform.addr` disables all three outbound channels; `getAttestation` stays
mounted either way because it is part of the TEE-TLS surface.

```json
"platform": {
  "addr": "http://172.16.10.181:18081",
  "dockerId": "taa-teellm",
  "heartbeatSeconds": 30,
  "logFlushSeconds": 5
}
```

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

Run the full test suite covering the TEE-TLS handshake and attestation endpoint, the generate endpoint, the Ollama adapter's option merge and health check, and platform registration/heartbeat/log reporting:

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
