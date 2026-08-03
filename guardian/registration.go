package guardian

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	secretstypes "github.com/timeflareio/chain/x/secrets/types"
	"github.com/timeflareio/guardian/blockchain"
	"github.com/timeflareio/guardian/config"
	"go.uber.org/zap"
)

// RegistrationManager handles guardian registration
type RegistrationManager struct {
	config *config.Config
	client blockchain.ClientInterface
	logger *zap.Logger
}

// NewRegistrationManager creates a new registration manager
func NewRegistrationManager(cfg *config.Config, client blockchain.ClientInterface, logger *zap.Logger) *RegistrationManager {
	return &RegistrationManager{
		config: cfg,
		client: client,
		logger: logger,
	}
}

// RegistrationStatus represents the current registration status
type RegistrationStatus struct {
	IsRegistered bool
	Address      string
	Stake        string
	Accepting    bool
}

// RegisterWithOptions registers (or updates) the guardian on chain.
//
// Defaults follow the config-default + flag-override resolution: an empty
// StakeAmount falls back to the configured stake_amount; an AvailableUntil of
// zero falls back to the chain's MaxAvailabilityWindow constant — the longest
// commitment the chain accepts and the reach required to cover a
// maximum-horizon secret (single-sourced from x/secrets, not re-declared).
func (rm *RegistrationManager) RegisterWithOptions(ctx context.Context, opts *RegistrationOptions) error {
	stakeAmount := opts.StakeAmount
	if stakeAmount == "" {
		stakeAmount = rm.config.StakeAmount
	}

	// The registration message takes RELATIVE offsets from the current block —
	// the chain converts them to absolute heights itself (x/secrets keeper
	// WindowCalculator). Pass the configured offsets straight through.
	availableFrom := opts.AvailableFrom // 0 = chain default (current block + 1)
	availableUntil := opts.AvailableUntil
	if availableUntil == 0 {
		availableUntil = secretstypes.MaxAvailabilityWindow
	}

	rm.logger.Info("Starting guardian registration",
		zap.String("stake_amount", stakeAmount),
		zap.Int64("available_from", availableFrom),
		zap.Int64("available_until", availableUntil),
		zap.Bool("is_update", opts.IsUpdate))

	// 1. Validate guardian key exists and resolves to a valid address
	address, err := rm.validateGuardianKey(ctx)
	if err != nil {
		return fmt.Errorf("guardian key validation failed: %w", err)
	}

	rm.logger.Info("Guardian key validated", zap.String("address", address))

	// 2. Check existing registration (unless forcing)
	if !opts.Force {
		status, err := rm.checkRegistrationStatus(ctx, address)
		if err != nil {
			return fmt.Errorf("failed to check registration: %w", err)
		}
		if status.IsRegistered && !opts.IsUpdate {
			rm.logger.Info("Guardian already registered",
				zap.String("address", address),
				zap.String("stake", status.Stake))
			return fmt.Errorf("guardian already registered. Use --update for updates or --force to re-register")
		}
	}

	deposit, err := sdk.ParseCoinNormalized(stakeAmount)
	if err != nil {
		return fmt.Errorf("invalid stake amount %q: %w", stakeAmount, err)
	}

	// 3. Validate balance for new registrations or deposit increases
	if !opts.IsUpdate || !deposit.IsZero() {
		if err := rm.validateBalance(ctx, address, deposit); err != nil {
			return fmt.Errorf("insufficient balance: %w", err)
		}
	}

	// 4. Submit the transaction
	var txHash string
	if opts.IsUpdate {
		accepting := true
		txHash, err = rm.client.GuardianUpdate(ctx, blockchain.GuardianUpdateOptions{
			AvailableFrom:    availableFrom,
			AvailableUntil:   availableUntil,
			Deposit:          &deposit,
			AcceptingSecrets: &accepting,
		})
	} else {
		if opts.EncryptionPublicKeyHex == "" {
			return fmt.Errorf("encryption public key is required. Generate and securely store your encryption key pair before registration")
		}
		var encryptionKey []byte
		encryptionKey, err = hex.DecodeString(opts.EncryptionPublicKeyHex)
		if err != nil || len(encryptionKey) != 32 {
			return fmt.Errorf("encryption public key must be 64 hex characters (32 bytes)")
		}

		txHash, err = rm.client.GuardianRegister(ctx, blockchain.GuardianRegisterOptions{
			EncryptionPublicKey: encryptionKey,
			AvailableFrom:       availableFrom,
			AvailableUntil:      availableUntil,
			Deposit:             deposit,
			AcceptingSecrets:    true,
		})
	}
	if err != nil {
		return fmt.Errorf("registration transaction failed: %w", err)
	}

	rm.logger.Info("Guardian registration successful",
		zap.String("tx_hash", txHash),
		zap.String("address", address),
		zap.String("stake_amount", stakeAmount))

	return nil
}

// validateGuardianKey validates that the guardian key exists and is accessible
func (rm *RegistrationManager) validateGuardianKey(ctx context.Context) (string, error) {
	address, err := rm.client.GetGuardianAddress(ctx, rm.config.KeyName)
	if err != nil {
		return "", fmt.Errorf("guardian key not found or inaccessible: %w", err)
	}

	if err := config.ValidateGuardianAddress(address); err != nil {
		return "", fmt.Errorf("invalid guardian address %s: %w", address, err)
	}

	return address, nil
}

// checkRegistrationStatus checks if the guardian is already registered
func (rm *RegistrationManager) checkRegistrationStatus(ctx context.Context, address string) (*RegistrationStatus, error) {
	guardian, err := rm.client.GetGuardian(ctx, address)
	if err != nil {
		if errors.Is(err, blockchain.ErrNotFound) {
			return &RegistrationStatus{IsRegistered: false, Address: address}, nil
		}
		// If the chain is unreachable we cannot distinguish "not registered"
		// from "cannot check" — surface the error and let the caller decide.
		return nil, err
	}

	return &RegistrationStatus{
		IsRegistered: true,
		Address:      address,
		Stake:        guardian.Stake.Amount + guardian.Stake.Denom,
		Accepting:    guardian.AcceptingSecrets,
	}, nil
}

// validateBalance checks the account can cover the deposit plus a fee buffer
// (fee_buffer_percent of the deposit), using sdk coin arithmetic — no
// hand-rolled digit parsing.
func (rm *RegistrationManager) validateBalance(ctx context.Context, address string, deposit sdk.Coin) error {
	balance, err := rm.client.GetBalance(ctx, address, rm.config.Denom)
	if err != nil {
		return fmt.Errorf("failed to get balance: %w", err)
	}

	balanceAmount, ok := math.NewIntFromString(balance.Amount)
	if !ok {
		return fmt.Errorf("failed to parse balance amount %q", balance.Amount)
	}

	buffer := deposit.Amount.MulRaw(int64(rm.config.FeeBufferPercent)).QuoRaw(100)
	required := deposit.Amount.Add(buffer)

	rm.logger.Info("Account balance",
		zap.String("address", address),
		zap.String("balance", balance.Amount),
		zap.String("required", required.String()))

	if balanceAmount.LT(required) {
		return fmt.Errorf("insufficient balance: have %s%s, need %s%s (including %d%% fee buffer)",
			balance.Amount, rm.config.Denom, required.String(), rm.config.Denom, rm.config.FeeBufferPercent)
	}

	return nil
}
