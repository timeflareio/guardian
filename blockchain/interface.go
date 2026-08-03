package blockchain

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// ClientInterface defines the interface for blockchain operations
type ClientInterface interface {
	// Connectivity
	Ping(ctx context.Context) error
	Close() error

	// Identity
	SignerAddress() string

	// Query operations
	GetGuardian(ctx context.Context, address string) (*Guardian, error)
	GetGuardianKeyHistory(ctx context.Context, address string) ([]KeyEpoch, error)
	GetSecret(ctx context.Context, secretID string) (*Secret, error)
	ListSecretsForGuardian(ctx context.Context, guardianAddress string) ([]Secret, error)
	GetCurrentBlockHeight(ctx context.Context) (int64, error)
	GetGuardianAddress(ctx context.Context, keyName string) (string, error)
	GetBalance(ctx context.Context, address, denom string) (*Balance, error)

	// Transaction operations (signed in-process by the configured key)
	GuardianRegister(ctx context.Context, opts GuardianRegisterOptions) (string, error)
	GuardianUpdate(ctx context.Context, opts GuardianUpdateOptions) (string, error)
	GuardianRotateKey(ctx context.Context, newKey []byte) (string, error)
	GuardianConfirmShares(ctx context.Context, secretID string, accept bool) (string, error)
	GuardianRevealShare(ctx context.Context, secretID string, share []byte) (string, error)
}

// Ensure Client implements ClientInterface
var _ ClientInterface = (*Client)(nil)

// TxSubmitter is the signing-backend boundary (key custody plan, Phase 3):
// anything that can sign and broadcast messages for the guardian's account.
// The keyring-backed Signer (signer.go) is the default; a remote-signer or
// KMS backend slots in behind this interface without touching the client.
// (The KMS/HSM implementation itself is descoped — owner ruling, July 2026 —
// but the seam is kept so adding one later is not a refactor.)
type TxSubmitter interface {
	// Address returns the signer's bech32 account address.
	Address() string
	// SubmitTx signs and broadcasts msgs as one transaction, returning the
	// tx hash once the chain accepts it into the mempool.
	SubmitTx(ctx context.Context, msgs ...sdk.Msg) (string, error)
}

// Ensure the keyring-backed signer satisfies the boundary.
var _ TxSubmitter = (*Signer)(nil)

// re-export for callers building GuardianUpdateOptions deposits without
// importing the sdk directly is intentionally NOT provided — construct
// sdk.Coin via sdk.ParseCoinNormalized where needed.
var _ = sdk.Coin{}
