package platformagent

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"
)

const (
	// RegisterEndpoint receives the plaintext registration payload.
	RegisterEndpoint = "/v1/llm/register"
	// HeartbeatEndpoint receives the plaintext liveness ping.
	HeartbeatEndpoint = "/v1/llm/heartbeat"
	// LogEndpoint receives batched run logs.
	LogEndpoint = "/v1/llm/log"
)

// registerRequest mirrors taa/internal/platform.registerRequest field for
// field, minus taaSecretKey: that field exists so TAA can hand back a wrapped
// SM2 private scalar, and the LLM has no such key to return.
type registerRequest struct {
	DockerID          string `json:"dockerId"`
	AuthInfo          any    `json:"authInfo"`
	TaaPublicKey      string `json:"taaPublicKey"`
	AttestationValues string `json:"attestationValues"`
	Timestamp         string `json:"timestamp"`
	VerifiedPass      bool   `json:"verifiedPass"`
	Attestation       string `json:"attestation"`
}

// registerOnce builds a current payload and sends it. The payload is rebuilt on
// every attempt so a retry reports the certificate in force at that moment.
func (a *Agent) registerOnce(ctx context.Context) error {
	snapshot, err := a.cfg.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("build attestation snapshot: %w", err)
	}

	payload := registerRequest{
		DockerID:          a.cfg.DockerID,
		AuthInfo:          nil,
		TaaPublicKey:      snapshot.PublicKeyPEM,
		AttestationValues: snapshot.ValuesJSON(),
		Timestamp:         fmt.Sprintf("%d", time.Now().Unix()),
		VerifiedPass:      snapshot.VerifiedPass,
		Attestation:       base64.StdEncoding.EncodeToString(snapshot.Report),
	}
	return a.postJSON(ctx, RegisterEndpoint, payload)
}
