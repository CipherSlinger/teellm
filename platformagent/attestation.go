package platformagent

import (
	"context"

	"github.com/CipherSlinger/teellm"
)

// SnapshotFunc supplies the attestation material a registration reports.
//
// It is a function rather than a *teetls.Config so this package never depends
// on the TLS layer, and so tests can answer without hardware.
type SnapshotFunc func(ctx context.Context) (*teellm.AttestationSnapshot, error)
