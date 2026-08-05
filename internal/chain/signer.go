package chain

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/cosmos/cosmos-sdk/client"
	clienttx "github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	secretstypes "github.com/timeflareio/chain/x/secrets/types"
	"github.com/timeflareio/guardian/internal/config"
)

// reimbursedGasLimit returns the gas limit the protocol reimburses for a
// submission, or 0 when the message set is not a reimbursed one.
//
// The creator escrows a guardian's transaction costs at exactly
// MinRequiredFee(GuardianAcceptGas) and MinRequiredFee(GuardianRevealGas), so
// declaring those limits makes reimbursement equal spend BY CONSTRUCTION.
// Simulating instead would leave coverage resting on gas_adjustment and the
// simulator's output — two values that can drift a guardian into working at a
// loss without anything failing. Ample headroom: the measured consumption is
// ~89.5k and ~95.5k against the 120k/130k declared here.
//
// Only a lone message qualifies. A batch would pay one fee for several
// reimbursements and could exceed the limit, so it falls through to simulation.
func reimbursedGasLimit(msgs []sdk.Msg) uint64 {
	if len(msgs) != 1 {
		return 0
	}
	switch msgs[0].(type) {
	case *secretstypes.MsgGuardianConfirmShares:
		return secretstypes.GuardianAcceptGas
	case *secretstypes.MsgGuardianRevealShare:
		return secretstypes.GuardianRevealGas
	default:
		return 0
	}
}

// warnIfPricedAboveReimbursement logs once at startup when the operator has
// set a gas price above the consensus floor.
//
// That is a legitimate choice — a higher price buys mempool priority, and
// refusing to honour it could leave a guardian unable to push a reveal through
// a congested chain, where a missed reveal is a slash. But the creator's
// reimbursement is denominated at the FLOOR, so the difference comes out of
// the guardian's own float on every accept and reveal. Better discovered in a
// startup line than in a slowly shrinking balance.
func warnIfPricedAboveReimbursement(cfg *config.Config, logger *zap.Logger) {
	price, err := sdk.ParseDecCoins(cfg.GasPrice)
	if err != nil || len(price) == 0 {
		return // Validate() owns malformed configuration; this is advisory only
	}
	floor := config.MinGasPrice()
	configured := price.AmountOf(cfg.Denom)
	if configured.LTE(floor) {
		return
	}
	acceptShortfall := configured.Sub(floor).MulInt64(int64(secretstypes.GuardianAcceptGas)).TruncateInt()
	revealShortfall := configured.Sub(floor).MulInt64(int64(secretstypes.GuardianRevealGas)).TruncateInt()
	logger.Warn("Gas price is above the consensus floor — the excess is NOT reimbursed",
		zap.String("configured", configured.String()),
		zap.String("floor", floor.String()),
		zap.String("unreimbursed_per_accept", acceptShortfall.String()+cfg.Denom),
		zap.String("unreimbursed_per_reveal", revealShortfall.String()+cfg.Denom),
		zap.String("note", "the creator escrows these two legs at the floor price; "+
			"paying above it buys mempool priority at your own cost"))
}

// Signer signs and broadcasts transactions in-process: cosmos keyring for the
// key, gas simulation over gRPC, DIRECT signing, sync broadcast over the same
// connection. Sequence numbers are tracked locally and per-account-serialised
// (the mutex), so rapid submissions do not race committed state.
type Signer struct {
	cfg      *config.Config
	logger   *zap.Logger
	conn     *grpc.ClientConn
	kr       keyring.Keyring
	cdc      *codec.ProtoCodec
	registry codectypes.InterfaceRegistry
	txConfig client.TxConfig
	txSvc    txtypes.ServiceClient

	address sdk.AccAddress

	mu          sync.Mutex
	accountNum  uint64
	sequence    uint64
	sequenceSet bool
}

// NewSigner opens the keyring, resolves the signing address, and prepares the
// transaction machinery. It performs no network calls.
func NewSigner(cfg *config.Config, conn *grpc.ClientConn, logger *zap.Logger) (*Signer, error) {
	cdc, registry := newProtoCodec()

	kr, err := NewKeyring(cfg)
	if err != nil {
		return nil, err
	}

	record, err := kr.Key(cfg.KeyName)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrKeyNotFound, cfg.KeyName, err)
	}
	address, err := record.GetAddress()
	if err != nil {
		return nil, fmt.Errorf("failed to derive address for key %s: %w", cfg.KeyName, err)
	}

	warnIfPricedAboveReimbursement(cfg, logger)

	return &Signer{
		cfg:      cfg,
		logger:   logger,
		conn:     conn,
		kr:       kr,
		cdc:      cdc,
		registry: registry,
		txConfig: authtx.NewTxConfig(cdc, authtx.DefaultSignModes),
		txSvc:    txtypes.NewServiceClient(conn),
		address:  address,
	}, nil
}

// Address returns the signer's bech32 account address.
func (s *Signer) Address() string {
	return s.address.String()
}

// clientContext assembles the minimal client.Context the sdk tx helpers need,
// routing all queries over the gRPC connection.
func (s *Signer) clientContext() client.Context {
	return client.Context{}.
		WithChainID(s.cfg.ChainID).
		WithGRPCClient(s.conn).
		WithCodec(s.cdc).
		WithInterfaceRegistry(s.registry).
		WithTxConfig(s.txConfig).
		WithKeyring(s.kr).
		WithAccountRetriever(authtypes.AccountRetriever{}).
		WithFromName(s.cfg.KeyName).
		WithFromAddress(s.address)
}

// SubmitTx signs and broadcasts msgs as one transaction, returning the tx
// hash once the chain accepts it into the mempool (CheckTx passed). A wrong-
// sequence rejection refreshes from chain state and retries once.
func (s *Signer) SubmitTx(ctx context.Context, msgs ...sdk.Msg) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	hash, err := s.submitLocked(ctx, msgs...)
	if err != nil && isSequenceMismatch(err) {
		s.logger.Debug("Sequence mismatch — refreshing account state and retrying once", zap.Error(err))
		s.sequenceSet = false
		hash, err = s.submitLocked(ctx, msgs...)
	}
	return hash, err
}

func (s *Signer) submitLocked(ctx context.Context, msgs ...sdk.Msg) (string, error) {
	clientCtx := s.clientContext()

	if !s.sequenceSet {
		num, seq, err := clientCtx.AccountRetriever.GetAccountNumberSequence(clientCtx, s.address)
		if err != nil {
			return "", fmt.Errorf("failed to resolve account/sequence: %w", err)
		}
		s.accountNum, s.sequence, s.sequenceSet = num, seq, true
	}

	factory := clienttx.Factory{}.
		WithChainID(s.cfg.ChainID).
		WithKeybase(s.kr).
		WithTxConfig(s.txConfig).
		WithAccountRetriever(clientCtx.AccountRetriever).
		WithAccountNumber(s.accountNum).
		WithSequence(s.sequence).
		WithFromName(s.cfg.KeyName).
		WithGasAdjustment(s.cfg.GasAdjustment).
		WithGasPrices(s.cfg.GasPrice).
		WithSignMode(signing.SignMode_SIGN_MODE_DIRECT).
		WithSimulateAndExecute(true)

	// Gas: simulate, then declare the LARGER of the simulation and the amount
	// the protocol reimburses for this message.
	//
	// Taking the larger, rather than the reimbursed figure outright, is a
	// safety floor. Reimbursement is a flat constant while the accept handler's
	// cost grows with the secret's band (it rewrites the secret record, which
	// carries one address and one bond per selected guardian), so on a wide
	// band the reimbursement is not enough gas to execute. Declaring it anyway
	// would abort the transaction — and for a reveal that means a no-show
	// slash. Being under-reimbursed is bad; failing to reveal is far worse.
	//
	// Where the reimbursement DOES suffice — every band up to roughly fourteen
	// guardians, which covers the Standard and High presets — this pins the
	// declaration to it exactly, so spend equals reimbursement by construction
	// and no gas_adjustment setting can erode it.
	_, adjusted, err := clienttx.CalculateGas(clientCtx, factory, msgs...)
	if err != nil {
		return "", fmt.Errorf("gas simulation failed: %w", err)
	}
	if reimbursed := reimbursedGasLimit(msgs); reimbursed > adjusted {
		adjusted = reimbursed
	}
	factory = factory.WithGas(adjusted)

	txb, err := factory.BuildUnsignedTx(msgs...)
	if err != nil {
		return "", fmt.Errorf("failed to build transaction: %w", err)
	}

	if err := clienttx.Sign(ctx, factory, s.cfg.KeyName, txb, true); err != nil {
		return "", fmt.Errorf("failed to sign transaction: %w", err)
	}

	txBytes, err := s.txConfig.TxEncoder()(txb.GetTx())
	if err != nil {
		return "", fmt.Errorf("failed to encode transaction: %w", err)
	}

	res, err := s.txSvc.BroadcastTx(ctx, &txtypes.BroadcastTxRequest{
		TxBytes: txBytes,
		Mode:    txtypes.BroadcastMode_BROADCAST_MODE_SYNC,
	})
	if err != nil {
		return "", fmt.Errorf("broadcast failed: %w", err)
	}
	if res.TxResponse.Code != 0 {
		return "", fmt.Errorf("%w (code %d): %s", ErrTxRejected, res.TxResponse.Code, res.TxResponse.RawLog)
	}

	s.sequence++
	s.logger.Debug("Transaction broadcast",
		zap.String("tx_hash", res.TxResponse.TxHash),
		zap.Uint64("sequence", s.sequence-1))

	return res.TxResponse.TxHash, nil
}

// isSequenceMismatch detects the chain's wrong-sequence rejection so the
// signer can resync. The sdk surfaces this as ErrTxRejected with the
// account-sequence-mismatch ABCI log (code 32 in the sdk error space).
func isSequenceMismatch(err error) bool {
	return err != nil && strings.Contains(err.Error(), "account sequence mismatch")
}
