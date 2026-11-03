package platformagent

import (
	"context"
	"fmt"
	"time"
)

// heartbeatRequest is liveness only: no health, model or measurement data.
type heartbeatRequest struct {
	DockerID  string `json:"dockerId"`
	Timestamp string `json:"timestamp"`
}

func (a *Agent) sendHeartbeat(ctx context.Context) error {
	return a.postJSON(ctx, HeartbeatEndpoint, heartbeatRequest{
		DockerID:  a.cfg.DockerID,
		Timestamp: fmt.Sprintf("%d", time.Now().Unix()),
	})
}
