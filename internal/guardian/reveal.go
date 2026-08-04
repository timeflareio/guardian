package guardian

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	crypto "github.com/timeflareio/crypto/go"
	"github.com/timeflareio/guardian/internal/chain"
	"github.com/timeflareio/guardian/internal/config"
	"github.com/timeflareio/guardian/internal/monitoring"
	"go.uber.org/zap"
)

// ErrPrivateKeyUnavailable marks a decryption failure caused by the guardian's
// private key being missing or unreadable (as opposed to a bad ciphertext).
var ErrPrivateKeyUnavailable = errors.New("guardian private key unavailable")

// ShareRevealService handles share confirmation and revelation
type ShareRevealService struct {
	config  *config.Config
	client  chain.ClientInterface
	logger  *zap.Logger
	metrics *monitoring.Metrics // nil-safe sink
	keys    *EpochKeyResolver   // resolves which key epoch each assignment was encrypted to
	// obs records what the operator dashboard shows. Nil-safe, and recording
	// must never affect the outcome it describes — an observation buffer that
	// could fail a reveal would be worse than no dashboard.
	obs *Observations
	// inFlight suppresses a second submission of work already broadcast and not
	// yet on chain. Unlike obs it is constructed here rather than injected: it
	// decides whether a fee is spent, so a caller must not be able to leave it
	// unset and silently lose the guard.
	inFlight *InFlightRegistry

	guardianAddress string
}

// SetObservations supplies the dashboard's observation buffers.
func (srs *ShareRevealService) SetObservations(obs *Observations) { srs.obs = obs }

// InFlight exposes the registry so the service can reconcile it against chain
// state after each cache refresh.
func (srs *ShareRevealService) InFlight() *InFlightRegistry { return srs.inFlight }

// NewShareRevealService creates a new share reveal service
func NewShareRevealService(cfg *config.Config, client chain.ClientInterface, logger *zap.Logger) *ShareRevealService {
	return &ShareRevealService{
		config:          cfg,
		client:          client,
		logger:          logger,
		keys:            NewEpochKeyResolver(cfg, client, logger),
		inFlight:        NewInFlightRegistry(),
		guardianAddress: cfg.GuardianAddress,
	}
}

// ProcessConfirmation processes share confirmation for a secret assignment.
//
// Every path through the body broadcasts — an accept if the share validates, a
// reject if it does not — so the in-flight reservation is taken up front. That
// also spares the decrypt and HMAC verification, which run before the broadcast
// and would otherwise be repeated along with the wasted fee.
func (srs *ShareRevealService) ProcessConfirmation(ctx context.Context,
	secret *chain.Secret, assignment *chain.GuardianAssignment,
	currentHeight int64) error {

	if !srs.inFlight.Reserve(secret.ID, SubmissionConfirm, currentHeight) {
		srs.logger.Debug("Confirmation already broadcast and not yet on chain — skipping",
			zap.String("secret_id", secret.ID),
			zap.Int64("current_height", currentHeight))
		return nil
	}

	err := srs.processConfirmation(ctx, secret, assignment)
	if err != nil {
		// Nothing reached the mempool, so the work is outstanding again — a
		// genuine retry must not wait out the expiry.
		srs.inFlight.Release(secret.ID, SubmissionConfirm)
	}
	return err
}

func (srs *ShareRevealService) processConfirmation(ctx context.Context,
	secret *chain.Secret, assignment *chain.GuardianAssignment) error {

	srs.logger.Info("Processing share confirmation",
		zap.String("secret_id", secret.ID))

	// 1. Validate encrypted share exists
	if len(assignment.EncryptedShare) == 0 {
		srs.logger.Error("No encrypted share provided",
			zap.String("secret_id", secret.ID))
		return srs.rejectAssignment(ctx, secret.ID, "missing_encrypted_share")
	}

	// 2. Validate guardian address is set for cryptographic operations
	if srs.guardianAddress == "" {
		srs.logger.Error("Guardian address not set for cryptographic operations",
			zap.String("secret_id", secret.ID))
		return srs.rejectAssignment(ctx, secret.ID, "guardian_address_unavailable")
	}

	// 3. Decrypt share for validation (epoch-resolved key)
	decryptedShare, err := srs.decryptShare(ctx, secret, assignment.EncryptedShare)
	if err != nil {
		srs.logger.Error("Failed to decrypt share",
			zap.String("secret_id", secret.ID),
			zap.Error(err))

		reason := "decryption_failed"
		if errors.Is(err, ErrPrivateKeyUnavailable) {
			reason = "private_key_unavailable"
		}
		srs.metrics.RecordValidationFailure(reason)

		return srs.rejectAssignment(ctx, secret.ID, reason)
	}

	// 4. Validate share integrity using Rust crypto HMAC
	if err := srs.validateShareIntegrity(secret, assignment, decryptedShare); err != nil {
		srs.logger.Error("Share integrity validation failed",
			zap.String("secret_id", secret.ID),
			zap.Error(err))

		srs.metrics.RecordValidationFailure("hmac")
		return srs.rejectAssignment(ctx, secret.ID, "validation_failed")
	}

	// 5. Accept assignment - all validations passed
	srs.logger.Info("Share validation passed, accepting assignment",
		zap.String("secret_id", secret.ID),
		zap.Int("decrypted_share_size", len(decryptedShare)))

	return srs.acceptAssignment(ctx, secret.ID)
}

// ProcessReveal processes share revelation for a secret
func (srs *ShareRevealService) ProcessReveal(ctx context.Context,
	secret *chain.Secret, assignment *chain.GuardianAssignment, currentHeight int64) error {

	// 1. Check reveal timing (window bounds + anti-coordination offset)
	if !srs.shouldRevealNow(secret, currentHeight) {
		return nil // Not time to reveal yet
	}

	// Reserved after the timing gate, so a secret that is merely not due yet
	// does not occupy a slot, and before the decrypt, which is the expensive
	// half of a duplicate.
	if !srs.inFlight.Reserve(secret.ID, SubmissionReveal, currentHeight) {
		srs.logger.Debug("Reveal already broadcast and not yet on chain — skipping",
			zap.String("secret_id", secret.ID),
			zap.Int64("current_height", currentHeight))
		return nil
	}

	err := srs.processReveal(ctx, secret, assignment, currentHeight)
	if err != nil {
		srs.inFlight.Release(secret.ID, SubmissionReveal)
	}
	return err
}

func (srs *ShareRevealService) processReveal(ctx context.Context,
	secret *chain.Secret, assignment *chain.GuardianAssignment, currentHeight int64) error {

	srs.logger.Info("Processing share reveal",
		zap.String("secret_id", secret.ID),
		zap.Int64("current_height", currentHeight),
		zap.Int64("reveal_start", secret.RevealStartBlock),
		zap.Int64("reveal_end", secret.RevealEndBlock))

	// 2. Decrypt share (epoch-resolved key)
	share, err := srs.decryptShare(ctx, secret, assignment.EncryptedShare)
	if err != nil {
		srs.metrics.RecordReveal(false, -1)
		// Recorded as a failed reveal submission even though nothing was
		// broadcast: from the operator's seat the share did not go out, and
		// hiding a decrypt failure here would make the dashboard's reveal
		// history a partial account of why a window was missed.
		srs.obs.RecordSubmission(Submission{
			At: time.Now(), Kind: SubmissionReveal, SecretID: secret.ID,
			Success: false, Err: fmt.Sprintf("share decryption failed: %v", err),
			Height: currentHeight,
		})
		return fmt.Errorf("failed to decrypt share: %w", err)
	}

	// 3. Submit reveal transaction
	txHash, err := srs.client.GuardianRevealShare(ctx, secret.ID, share)
	if err != nil {
		srs.metrics.RecordReveal(false, -1)
		srs.obs.RecordSubmission(Submission{
			At: time.Now(), Kind: SubmissionReveal, SecretID: secret.ID,
			Success: false, Err: err.Error(), Height: currentHeight,
		})
		return fmt.Errorf("reveal transaction failed (height %d, window [%d,%d]): %w",
			currentHeight, secret.RevealStartBlock, secret.RevealEndBlock, err)
	}
	srs.obs.RecordSubmission(Submission{
		At: time.Now(), Kind: SubmissionReveal, SecretID: secret.ID,
		TxHash: txHash, Success: true, Height: currentHeight,
	})

	sinceWindowOpen := time.Duration(currentHeight-secret.RevealStartBlock) * srs.config.BlockTime
	srs.metrics.RecordReveal(true, sinceWindowOpen)

	srs.logger.Info("Share revealed successfully",
		zap.String("secret_id", secret.ID),
		zap.String("tx_hash", txHash),
		zap.Int64("height", currentHeight),
		zap.Int64("blocks_after_window_open", currentHeight-secret.RevealStartBlock))

	return nil
}

// validateShareIntegrity validates the integrity of a decrypted share
func (srs *ShareRevealService) validateShareIntegrity(
	secret *chain.Secret, assignment *chain.GuardianAssignment, share []byte) error {

	if !srs.config.EnableHMACValidation {
		srs.logger.Debug("HMAC validation disabled, skipping",
			zap.String("secret_id", secret.ID))
		return nil
	}

	if len(assignment.ShareHMAC) == 0 {
		return fmt.Errorf("no HMAC provided for share validation")
	}

	// Generate HMAC using Rust crypto to match the protocol implementation
	expectedHMAC, err := crypto.GenerateHMAC(secret.ID, srs.guardianAddress, share)
	if err != nil {
		return fmt.Errorf("failed to generate HMAC for validation: %w", err)
	}

	// Use constant-time comparison to prevent timing attacks
	if len(expectedHMAC) != len(assignment.ShareHMAC) {
		return fmt.Errorf("HMAC length mismatch: expected %d, got %d",
			len(expectedHMAC), len(assignment.ShareHMAC))
	}
	if subtle.ConstantTimeCompare(expectedHMAC, assignment.ShareHMAC) != 1 {
		return fmt.Errorf("HMAC validation failed: computed HMAC does not match stored HMAC")
	}

	srs.logger.Debug("HMAC validation passed",
		zap.String("secret_id", secret.ID),
		zap.String("guardian_address", srs.guardianAddress))
	return nil
}

// shouldRevealNow determines if it's time to reveal the share.
func (srs *ShareRevealService) shouldRevealNow(secret *chain.Secret, currentHeight int64) bool {
	if currentHeight < srs.plannedRevealHeight(secret) {
		return false // Too early (window not open, or our anti-coordination offset not reached)
	}

	// The chain's window is [start, end] — BOTH bounds inclusive. A reveal in
	// block end is valid; settlement runs in the EndBlock of block end + 1.
	if currentHeight > secret.RevealEndBlock {
		srs.logger.Warn("Reveal window has passed",
			zap.String("secret_id", secret.ID),
			zap.Int64("current_height", currentHeight),
			zap.Int64("reveal_end", secret.RevealEndBlock))
		return false // Too late
	}

	return true
}

// plannedRevealHeight is window-open plus this guardian's anti-coordination
// offset: a deterministic pseudo-random 0..reveal_offset_blocks delay derived
// from (secret, guardian), capped so the retry budget still fits before the
// window closes. With reveal_offset_blocks = 0 (the default) reveals fire at
// window-open exactly.
func (srs *ShareRevealService) plannedRevealHeight(secret *chain.Secret) int64 {
	offset := srs.config.RevealOffsetBlocks
	if offset <= 0 {
		return secret.RevealStartBlock
	}

	h := sha256.Sum256([]byte(secret.ID + "|" + srs.guardianAddress))
	jitter := int64(binary.BigEndian.Uint64(h[:8]) % uint64(offset+1)) //nolint:gosec // deterministic jitter, not crypto

	planned := secret.RevealStartBlock + jitter
	// Leave at least half the window after the planned height for retries.
	latest := secret.RevealStartBlock + (secret.RevealEndBlock-secret.RevealStartBlock)/2
	if planned > latest {
		planned = latest
	}
	return planned
}

// decryptShare decrypts an encrypted share with the key of the epoch in
// force at the secret's creation (= selection) height — the key the creator
// encrypted to. Resolution is fully automated; when the derivation-resolved
// key fails (stale local history, unexpected state), the belt-and-braces
// fallback trial-decrypts across the whole epoch keyring and logs loudly
// when it disagrees with the derivation.
func (srs *ShareRevealService) decryptShare(ctx context.Context, secret *chain.Secret, encryptedShare []byte) ([]byte, error) {
	if len(encryptedShare) == 0 {
		return nil, fmt.Errorf("empty encrypted share")
	}

	derivedEpoch := int64(-1)
	privateKey, epoch, err := srs.keys.KeyForHeight(ctx, secret.CreatedAt)
	if err == nil {
		derivedEpoch = int64(epoch) //nolint:gosec // epochs are tiny by construction
		if decryptedShare, derr := crypto.DecryptShareWithPrivateKey(encryptedShare, privateKey); derr == nil && len(decryptedShare) > 0 {
			srs.logger.Debug("Successfully decrypted share",
				zap.Uint64("key_epoch", epoch),
				zap.Int("encrypted_size", len(encryptedShare)),
				zap.Int("decrypted_size", len(decryptedShare)))
			return decryptedShare, nil
		}
		srs.logger.Warn("Derivation-resolved epoch key failed to decrypt — refreshing history and trying the whole keyring",
			zap.String("secret_id", secret.ID),
			zap.Int64("creation_height", secret.CreatedAt),
			zap.Uint64("derived_epoch", epoch))
	} else {
		srs.logger.Warn("Key-epoch resolution failed — trying the whole keyring",
			zap.String("secret_id", secret.ID),
			zap.Int64("creation_height", secret.CreatedAt),
			zap.Error(err))
	}

	// A rotation may have run outside this process (fresh files on disk,
	// stale cache) — refresh before the trial pass.
	if rerr := srs.keys.Refresh(ctx); rerr != nil {
		srs.logger.Debug("Key history refresh failed during fallback", zap.Error(rerr))
	}

	trial := srs.keys.TrialKeys(ctx)
	for _, tk := range trial {
		decryptedShare, derr := crypto.DecryptShareWithPrivateKey(encryptedShare, tk.Key)
		if derr != nil || len(decryptedShare) == 0 {
			continue
		}
		srs.logger.Warn("Trial-decrypt fallback succeeded where derivation did not — investigate local key layout vs on-chain history",
			zap.String("secret_id", secret.ID),
			zap.Int64("creation_height", secret.CreatedAt),
			zap.Int64("derived_epoch", derivedEpoch),
			zap.Uint64("decrypting_epoch", tk.Epoch))
		return decryptedShare, nil
	}

	if len(trial) == 0 {
		return nil, fmt.Errorf("%w: no epoch key could be loaded", ErrPrivateKeyUnavailable)
	}
	return nil, fmt.Errorf("failed to decrypt share with any of %d epoch keys (derived epoch %d)", len(trial), derivedEpoch)
}

// acceptAssignment submits a share acceptance transaction.
//
// NOTE: broadcast success only means the tx passed CheckTx — it can still fail
// in DeliverTx (e.g. out-raced for the last slot: "current status: pending").
// The next cache refresh reconciles against on-chain state, so a failed
// acceptance self-corrects to eviction; the log wording reflects that.
func (srs *ShareRevealService) acceptAssignment(ctx context.Context, secretID string) error {
	txHash, err := srs.client.GuardianConfirmShares(ctx, secretID, true)
	if err != nil {
		srs.metrics.RecordConfirmationFailure()
		srs.obs.RecordSubmission(Submission{
			At: time.Now(), Kind: SubmissionConfirm, SecretID: secretID,
			Success: false, Err: err.Error(),
		})
		return err
	}

	srs.metrics.RecordConfirmation(true)
	// Both a decision and a submission: the decision log answers "why did I
	// take this?", the transaction list answers "did the tx land?". Broadcast
	// success is only CheckTx, which the submission note already reflects.
	srs.obs.RecordDecision(Decision{
		At: time.Now(), SecretID: secretID, Outcome: DecisionAccepted,
		Reason: "share HMAC verified, bond affordable",
	})
	srs.obs.RecordSubmission(Submission{
		At: time.Now(), Kind: SubmissionConfirm, SecretID: secretID,
		TxHash: txHash, Success: true,
	})
	srs.logger.Info("Acceptance transaction broadcast (result reconciled on next poll)",
		zap.String("secret_id", secretID),
		zap.String("tx_hash", txHash))

	return nil
}

// rejectAssignment submits a share rejection transaction
func (srs *ShareRevealService) rejectAssignment(ctx context.Context, secretID string, reason string) error {
	txHash, err := srs.client.GuardianConfirmShares(ctx, secretID, false)
	if err != nil {
		srs.metrics.RecordConfirmationFailure()
		srs.obs.RecordSubmission(Submission{
			At: time.Now(), Kind: SubmissionConfirm, SecretID: secretID,
			Success: false, Err: err.Error(),
		})
		return err
	}

	srs.metrics.RecordConfirmation(false)
	// The reason is the whole value of this entry: "why did I not take that
	// secret?" is unanswerable from logs alone once they have rotated.
	srs.obs.RecordDecision(Decision{
		At: time.Now(), SecretID: secretID, Outcome: DecisionRejected, Reason: reason,
	})
	srs.obs.RecordSubmission(Submission{
		At: time.Now(), Kind: SubmissionConfirm, SecretID: secretID,
		TxHash: txHash, Success: true,
	})
	srs.logger.Info("Assignment rejected",
		zap.String("secret_id", secretID),
		zap.String("reason", reason),
		zap.String("tx_hash", txHash))

	return nil
}
