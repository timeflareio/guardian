package chain

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	secretstypes "github.com/timeflareio/chain/x/secrets/types"
)

// Sentinel errors for the client layer. Callers classify with errors.Is —
// never by substring matching.
var (
	// ErrNotFound: the queried entity does not exist on chain.
	ErrNotFound = errors.New("not found")
	// ErrUnavailable: transient chain connectivity failure (retried already).
	ErrUnavailable = errors.New("chain unavailable")
	// ErrTxRejected: the transaction was broadcast and rejected by the chain.
	ErrTxRejected = errors.New("transaction rejected")
	// ErrKeyNotFound: the signing key is missing from the keyring.
	ErrKeyNotFound = errors.New("key not found in keyring")
)

// isRetryable reports whether a gRPC error is worth retrying — decided by
// status code, not error-string matching.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	s, ok := status.FromError(err)
	if !ok {
		return false
	}
	switch s.Code() {
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted, codes.Aborted:
		return true
	}
	return false
}

// guardianNotFoundSignature is how the chain's typed ErrGuardianNotFound
// arrives over gRPC: keeper errors flatten to "codespace <module> code <n>"
// text with status code Unknown, so the ABCI code — single-sourced from the
// module's error registry — is the reliable discriminator.
var guardianNotFoundSignature = fmt.Sprintf("codespace %s code %d",
	secretstypes.ModuleName, secretstypes.ErrGuardianNotFound.ABCICode())

// isNotFound reports whether a gRPC error is a NotFound — either a proper
// codes.NotFound, or the module's typed not-found error flattened into an
// Unknown status by the query server.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	s, ok := status.FromError(err)
	if !ok {
		return false
	}
	if s.Code() == codes.NotFound {
		return true
	}
	return s.Code() == codes.Unknown && strings.Contains(s.Message(), guardianNotFoundSignature)
}
