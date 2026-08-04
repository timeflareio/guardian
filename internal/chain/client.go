package chain

import (
	"context"
	"fmt"
	"time"

	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	gogotypes "github.com/cosmos/gogoproto/types"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	secretstypes "github.com/timeflareio/chain/x/secrets/types"
	"github.com/timeflareio/guardian/internal/config"
)

// Client is the fully native chain client: typed gRPC queries and in-process
// transaction signing. No subprocesses, no stdout parsing, no timeflared
// binary dependency.
type Client struct {
	cfg    *config.Config
	logger *zap.Logger

	conn    *grpc.ClientConn
	secrets secretstypes.QueryClient
	bank    banktypes.QueryClient
	cmt     cmtservice.ServiceClient
	// signer is held behind the TxSubmitter boundary so a remote-signer/KMS
	// backend can replace the keyring-backed Signer without client changes.
	signer TxSubmitter
}

// NewClient dials the configured gRPC endpoint and opens the keyring for
// signing. The dial is lazy (see Dial) — connectivity is verified by the first
// call (or Ping).
func NewClient(cfg *config.Config, logger *zap.Logger) (*Client, error) {
	conn, err := Dial(cfg, logger)
	if err != nil {
		return nil, err
	}

	signer, err := NewSigner(cfg, conn, logger)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	return &Client{
		cfg:     cfg,
		logger:  logger,
		conn:    conn,
		secrets: secretstypes.NewQueryClient(conn),
		bank:    banktypes.NewQueryClient(conn),
		cmt:     cmtservice.NewServiceClient(conn),
		signer:  signer,
	}, nil
}

// Close releases the gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// SignerAddress returns the address of the configured signing key.
func (c *Client) SignerAddress() string {
	return c.signer.Address()
}

// withRetry runs fn under the configured timeout, retrying transient gRPC
// failures with linear backoff. Retryability is decided by status codes
// (errors.go), never by error-string matching.
func (c *Client) withRetry(ctx context.Context, op string, fn func(ctx context.Context) error) error {
	var lastErr error
	for attempt := 0; attempt < c.cfg.RetryAttempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, c.cfg.RequestTimeout)
		err := fn(attemptCtx)
		cancel()

		if err == nil {
			return nil
		}
		lastErr = err

		if !isRetryable(err) {
			return err
		}
		c.logger.Debug("Transient chain request failure — retrying",
			zap.String("operation", op),
			zap.Int("attempt", attempt+1),
			zap.Error(err))

		if attempt < c.cfg.RetryAttempts-1 {
			backoff := c.cfg.RetryBackoff * time.Duration(attempt+1)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}
	}
	return fmt.Errorf("%w: %s: %v", ErrUnavailable, op, lastErr)
}

// Ping verifies chain connectivity (used at startup and by health checks).
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.GetCurrentBlockHeight(ctx)
	return err
}

// GetGuardian retrieves guardian information by address.
func (c *Client) GetGuardian(ctx context.Context, address string) (*Guardian, error) {
	var out *Guardian
	err := c.withRetry(ctx, "guardian query", func(ctx context.Context) error {
		resp, err := c.secrets.Guardian(ctx, &secretstypes.QueryGuardianRequest{Address: address})
		if err != nil {
			if isNotFound(err) {
				return fmt.Errorf("guardian %s: %w", address, ErrNotFound)
			}
			return err
		}
		out = guardianFromProto(&resp.Guardian)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetGuardianKeyHistory retrieves a guardian's append-only key-epoch history
// in epoch order — the derivation input for resolving which key an assignment
// was encrypted to (the epoch in force at the secret's creation height).
func (c *Client) GetGuardianKeyHistory(ctx context.Context, address string) ([]KeyEpoch, error) {
	var out []KeyEpoch
	err := c.withRetry(ctx, "guardian key history query", func(ctx context.Context) error {
		resp, err := c.secrets.GuardianKeyHistory(ctx, &secretstypes.QueryGuardianKeyHistoryRequest{Address: address})
		if err != nil {
			if isNotFound(err) {
				return fmt.Errorf("guardian %s: %w", address, ErrNotFound)
			}
			return err
		}
		out = out[:0]
		for _, e := range resp.Epochs {
			out = append(out, KeyEpoch{
				Epoch:               e.Epoch,
				PublicKey:           e.Entry.PublicKey,
				EffectiveFromHeight: e.Entry.EffectiveFromHeight,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetSecret retrieves secret information by ID.
func (c *Client) GetSecret(ctx context.Context, secretID string) (*Secret, error) {
	var out *Secret
	err := c.withRetry(ctx, "secret query", func(ctx context.Context) error {
		resp, err := c.secrets.Secret(ctx, &secretstypes.QuerySecretRequest{SecretId: secretID})
		if err != nil {
			if isNotFound(err) {
				return fmt.Errorf("secret %s: %w", secretID, ErrNotFound)
			}
			return err
		}
		if resp.Secret == nil {
			return fmt.Errorf("secret %s: %w", secretID, ErrNotFound)
		}
		s := secretFromView(resp.Secret)
		out = &s
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListSecretsForGuardian retrieves all secrets assigned to a guardian
// (client-side filtering across the paginated secrets list — see the
// event-filtering resolution in the improvements plan §7.2).
func (c *Client) ListSecretsForGuardian(ctx context.Context, guardianAddress string) ([]Secret, error) {
	var out []Secret
	err := c.withRetry(ctx, "secrets list", func(ctx context.Context) error {
		out = out[:0]
		var nextKey []byte
		for {
			resp, err := c.secrets.Secrets(ctx, &secretstypes.QuerySecretsRequest{
				Pagination: &query.PageRequest{Key: nextKey, Limit: 200},
			})
			if err != nil {
				return err
			}
			for _, view := range resp.Secrets {
				if view == nil {
					continue
				}
				secret := secretFromView(view)
				if secret.assignedTo(guardianAddress) {
					out = append(out, secret)
				}
			}
			if resp.Pagination == nil || len(resp.Pagination.NextKey) == 0 {
				return nil
			}
			nextKey = resp.Pagination.NextKey
		}
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// assignedTo reports whether this guardian has an assignment on the secret.
func (s *Secret) assignedTo(guardianAddress string) bool {
	for _, assignment := range s.GuardianAssignments {
		if assignment.GuardianAddress == guardianAddress {
			return true
		}
	}
	return false
}

// GetBalance retrieves the account balance for a denom.
func (c *Client) GetBalance(ctx context.Context, address string, denom string) (*Balance, error) {
	var out *Balance
	err := c.withRetry(ctx, "balance query", func(ctx context.Context) error {
		resp, err := c.bank.Balance(ctx, &banktypes.QueryBalanceRequest{Address: address, Denom: denom})
		if err != nil {
			return err
		}
		out = &Balance{Denom: denom, Amount: "0"}
		if resp.Balance != nil {
			out.Denom = resp.Balance.Denom
			out.Amount = resp.Balance.Amount.String()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetCurrentBlockHeight retrieves the current block height.
func (c *Client) GetCurrentBlockHeight(ctx context.Context) (int64, error) {
	var height int64
	err := c.withRetry(ctx, "latest block query", func(ctx context.Context) error {
		resp, err := c.cmt.GetLatestBlock(ctx, &cmtservice.GetLatestBlockRequest{})
		if err != nil {
			return err
		}
		if resp.SdkBlock == nil {
			return fmt.Errorf("empty block response")
		}
		height = resp.SdkBlock.Header.Height
		return nil
	})
	if err != nil {
		return 0, err
	}
	return height, nil
}

// GetGuardianAddress resolves a key name to its address via the in-process
// keyring.
func (c *Client) GetGuardianAddress(_ context.Context, keyName string) (string, error) {
	if keyName == c.cfg.KeyName {
		return c.signer.Address(), nil
	}
	return ResolveKeyAddress(c.cfg, keyName)
}

// GuardianRegisterOptions carries the MsgGuardianRegister parameters.
type GuardianRegisterOptions struct {
	EncryptionPublicKey []byte
	AvailableFrom       int64 // relative blocks (0 = current block + 1)
	AvailableUntil      int64 // relative blocks from available_from
	Deposit             sdk.Coin
	AcceptingSecrets    bool
}

// GuardianRegister submits a guardian registration transaction signed by the
// configured key.
func (c *Client) GuardianRegister(ctx context.Context, opts GuardianRegisterOptions) (string, error) {
	deposit := opts.Deposit
	msg := &secretstypes.MsgGuardianRegister{
		Guardian:            c.signer.Address(),
		EncryptionPublicKey: opts.EncryptionPublicKey,
		AvailableFrom:       opts.AvailableFrom,
		AvailableUntil:      opts.AvailableUntil,
		Deposit:             &deposit,
		AcceptingSecrets:    opts.AcceptingSecrets,
	}
	return c.signer.SubmitTx(ctx, msg)
}

// GuardianUpdateOptions carries the MsgGuardianUpdate parameters. Nil/zero
// fields mean "no change" (the chain treats them presence-aware).
type GuardianUpdateOptions struct {
	AvailableFrom    int64
	AvailableUntil   int64
	Deposit          *sdk.Coin
	AcceptingSecrets *bool
}

// GuardianUpdate submits a guardian update transaction signed by the
// configured key.
func (c *Client) GuardianUpdate(ctx context.Context, opts GuardianUpdateOptions) (string, error) {
	msg := &secretstypes.MsgGuardianUpdate{
		Guardian:       c.signer.Address(),
		AvailableFrom:  opts.AvailableFrom,
		AvailableUntil: opts.AvailableUntil,
		Deposit:        opts.Deposit,
	}
	if opts.AcceptingSecrets != nil {
		msg.AcceptingSecrets = &gogotypes.BoolValue{Value: *opts.AcceptingSecrets}
	}
	return c.signer.SubmitTx(ctx, msg)
}

// GuardianRotateKey submits a forward-only key rotation: newKey becomes the
// next epoch for future assignments, effective from the next block. The
// caller is responsible for the backup ceremony BEFORE submitting (the
// rotate-key command enforces it).
func (c *Client) GuardianRotateKey(ctx context.Context, newKey []byte) (string, error) {
	msg := &secretstypes.MsgGuardianRotateKey{
		Guardian: c.signer.Address(),
		NewKey:   newKey,
	}
	return c.signer.SubmitTx(ctx, msg)
}

// GuardianConfirmShares submits a share confirmation transaction.
func (c *Client) GuardianConfirmShares(ctx context.Context, secretID string, accept bool) (string, error) {
	msg := &secretstypes.MsgGuardianConfirmShares{
		Guardian: c.signer.Address(),
		SecretId: secretID,
		Accept:   accept,
	}
	return c.signer.SubmitTx(ctx, msg)
}

// GuardianRevealShare submits a share revelation transaction.
func (c *Client) GuardianRevealShare(ctx context.Context, secretID string, share []byte) (string, error) {
	msg := &secretstypes.MsgGuardianRevealShare{
		Guardian:       c.signer.Address(),
		SecretId:       secretID,
		DecryptedShare: share,
	}
	return c.signer.SubmitTx(ctx, msg)
}
