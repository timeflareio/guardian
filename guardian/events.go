package guardian

import (
	"context"
	"strings"
	"time"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	cmttypes "github.com/cometbft/cometbft/types"
	"go.uber.org/zap"

	"github.com/timeflareio/guardian/config"
)

// EventMonitor subscribes to the chain over the CometBFT WebSocket so the
// guardian reacts to operations that involve it instead of discovering them
// by polling:
//
//   - new-block headers give exact reveal-window timing (act AT window-open
//     height, not up to one polling interval late), and
//   - secrets-module transaction events trigger an immediate cache resync
//     (client-side filtering: assignment events don't carry guardian
//     addresses, so "does this involve me?" is answered by the resync's
//     assignment check — improvements plan §7.2).
//
// The polling loop remains the fallback: on any subscription failure the
// monitor reconnects with backoff while polling keeps the guardian safe.
type EventMonitor struct {
	cfg    *config.Config
	logger *zap.Logger

	onEvent  func(ctx context.Context)               // secrets-module tx observed
	onHeight func(ctx context.Context, height int64) // new block header
}

// NewEventMonitor creates an event monitor with the given reaction callbacks.
func NewEventMonitor(cfg *config.Config, logger *zap.Logger, onEvent func(context.Context), onHeight func(context.Context, int64)) *EventMonitor {
	return &EventMonitor{
		cfg:      cfg,
		logger:   logger,
		onEvent:  onEvent,
		onHeight: onHeight,
	}
}

// Run maintains the subscriptions until ctx is cancelled, reconnecting with
// the configured backoff whenever the socket drops.
func (em *EventMonitor) Run(ctx context.Context) {
	for {
		if err := em.runOnce(ctx); err != nil {
			em.logger.Warn("Event subscription lost — falling back to polling until reconnect",
				zap.Duration("reconnect_backoff", em.cfg.EventReconnectBackoff),
				zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(em.cfg.EventReconnectBackoff):
		}
	}
}

// runOnce connects, subscribes, and dispatches until an error or cancellation.
func (em *EventMonitor) runOnce(ctx context.Context) error {
	client, err := rpchttp.New(em.cfg.RPCEndpoint, "/websocket")
	if err != nil {
		return err
	}
	if err := client.Start(); err != nil {
		return err
	}
	defer func() { _ = client.Stop() }()

	subscriber := "guardiand-" + em.cfg.EffectiveGuardianID()

	headers, err := client.Subscribe(ctx, subscriber, "tm.event='NewBlockHeader'", 16)
	if err != nil {
		return err
	}
	txs, err := client.Subscribe(ctx, subscriber, "tm.event='Tx'", 64)
	if err != nil {
		return err
	}

	em.logger.Info("Event subscriptions established",
		zap.String("rpc_endpoint", em.cfg.RPCEndpoint))

	for {
		select {
		case <-ctx.Done():
			return nil

		case result, ok := <-headers:
			if !ok {
				return context.Canceled
			}
			if header, isHeader := result.Data.(cmttypes.EventDataNewBlockHeader); isHeader {
				em.onHeight(ctx, header.Header.Height)
			}

		case result, ok := <-txs:
			if !ok {
				return context.Canceled
			}
			if em.isSecretsEvent(result.Events) {
				em.onEvent(ctx)
			}
		}
	}
}

// isSecretsEvent reports whether a tx's indexed events include any
// secrets-module event (lifecycle transitions, assignment responses,
// settlement/slashing). Keys arrive as "<event_type>.<attribute>".
func (em *EventMonitor) isSecretsEvent(events map[string][]string) bool {
	for key := range events {
		if strings.HasPrefix(key, "secret_") ||
			strings.HasPrefix(key, "assignment_") ||
			strings.HasPrefix(key, "guardian_") {
			return true
		}
	}
	return false
}
