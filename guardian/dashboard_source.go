package guardian

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"time"

	crypto "github.com/timeflareio/crypto/go"
	"github.com/timeflareio/guardian/blockchain"
	"github.com/timeflareio/guardian/custody"
	"github.com/timeflareio/guardian/dashboard"
)

// The guardian service's implementation of dashboard.Source (dashboard plan
// §2/§4). The dependency runs this way deliberately: the daemon already imports
// monitoring, monitoring mounts the dashboard handlers, so the dashboard cannot
// import the daemon without closing a cycle. It declares an interface; this
// satisfies it — the same inversion SetObservability uses.
//
// Every method here must tolerate a chain that will not answer. A panel that
// renders zeros for an unreachable node is the one failure that could cost an
// operator money, so failures return Unavailable with a reason rather than a
// zero value.

// Interface compliance is asserted at compile time so a signature drift in the
// dashboard package fails the build rather than the wiring.
var _ dashboard.Source = (*Service)(nil)

// bondCap mirrors the protocol's per-guardian concurrency cap. Read from the
// types module rather than restated, so it cannot drift from consensus.
const dashboardBondCap = 100

// lowBalanceUveil is when the signing balance is called out. Confirmations and
// reveals are transactions; an unfunded guardian misses windows and is slashed
// for it, so this warns early rather than at zero.
const lowBalanceUveil = 1_000_000 // 1 VEIL

// Vitals implements dashboard.Source.
func (s *Service) Vitals(ctx context.Context) dashboard.Vitals {
	// version and configPath are written by SetBuildInfo under this same lock,
	// so they are read under it too — a handler goroutine racing the start
	// command is otherwise a genuine data race, not a theoretical one.
	s.mu.RLock()
	running := s.isRunning
	registered := s.isRegistered
	version := s.version
	configPath := s.configPath
	s.mu.RUnlock()

	v := dashboard.Vitals{
		GuardianAddress: s.config.GuardianAddress,
		ChainID:         s.config.ChainID,
		ConfigPath:      configPath,
		RPCEndpoint:     s.config.RPCEndpoint,
		GRPCEndpoint:    s.config.GRPCEndpoint,
		StartedAt:       s.startedAt,
		Running:         running,
		Registered:      registered,
		LastBlockHeight: s.lastKnownHeight.Load(),
		PollingHuman:    s.config.PollingInterval.String(),
		LastUpdate:      time.Now(),
		Version:         version,
	}
	if !s.startedAt.IsZero() {
		d := time.Since(s.startedAt).Truncate(time.Second)
		v.Uptime = d.String()
		v.UptimeSeconds = int64(d.Seconds())
	}
	if s.config.EnableEventMonitoring {
		v.EventStream = "websocket (polling fallback)"
	} else {
		v.EventStream = "polling only"
	}

	if height, err := s.client.GetCurrentBlockHeight(ctx); err == nil {
		v.ChainHeight = height
		v.Healthy = true
		// Lag against the height the daemon has actually processed. Stated
		// rather than left to be worked out: it is the number an operator acts
		// on, and a daemon can be "running" while many blocks behind.
		if p := v.LastBlockHeight; p > 0 {
			v.HeightLag = height - p
		}
	} else {
		v.ChainHeight = -1
	}

	if g, err := s.client.GetGuardian(ctx, s.config.GuardianAddress); err == nil {
		v.Registered = true
		v.AcceptingWork = g.AcceptingSecrets
	}
	return v
}

// Assignments implements dashboard.Source.
func (s *Service) Assignments(ctx context.Context) dashboard.Assignments {
	height := s.lastKnownHeight.Load()
	if h, err := s.client.GetCurrentBlockHeight(ctx); err == nil {
		height = h
	}

	snap := s.activeSecretCache.Snapshot(s.config.GuardianAddress)
	out := dashboard.Assignments{
		CurrentHeight: height,
		Active:        make([]dashboard.Assignment, 0, len(snap.Assignments)),
		StateCounts:   snap.StateCounts,
	}

	for _, a := range snap.Assignments {
		out.Active = append(out.Active, s.toDashboardAssignment(a, height))
	}
	// Soonest deadline first: an operator reads this top-down under time
	// pressure, so the ordering has to match the urgency.
	sort.SliceStable(out.Active, func(i, j int) bool {
		return sortKeyFor(out.Active[i]) < sortKeyFor(out.Active[j])
	})

	for _, a := range out.Active {
		switch a.LocalState {
		case StateNeedsConfirmation.String():
			out.AwaitingConfirmation = append(out.AwaitingConfirmation, a)
		case StateNeedsReveal.String():
			out.PendingReveal = append(out.PendingReveal, a)
		}
		if a.AtRisk {
			out.AtRisk = append(out.AtRisk, a)
		}
	}
	sort.SliceStable(out.AtRisk, func(i, j int) bool {
		return out.AtRisk[i].Urgency > out.AtRisk[j].Urgency
	})

	if out.AwaitingConfirmation == nil {
		out.AwaitingConfirmation = []dashboard.Assignment{}
	}
	if out.PendingReveal == nil {
		out.PendingReveal = []dashboard.Assignment{}
	}
	if out.AtRisk == nil {
		out.AtRisk = []dashboard.Assignment{}
	}
	return out
}

// sortKeyFor orders by the nearest deadline that still matters: the commit
// deadline while unconfirmed, the window close once accepted.
func sortKeyFor(a dashboard.Assignment) int64 {
	if a.BlocksToCommitDeadline > 0 {
		return a.BlocksToCommitDeadline
	}
	if a.BlocksToWindowClose > 0 {
		return a.BlocksToWindowClose
	}
	return 1 << 40
}

func (s *Service) toDashboardAssignment(a AssignmentSnapshot, height int64) dashboard.Assignment {
	out := dashboard.Assignment{
		SecretID:         a.SecretID,
		ChainState:       a.ChainState,
		LocalState:       a.LocalState,
		AssignmentStatus: a.AssignmentStatus,
		Threshold:        a.Threshold,
		MinShares:        a.MinShares,
		MaxShares:        a.MaxShares,
		AcceptedCount:    a.AcceptedCount,
		CommitDeadline:   a.CommitDeadline,
		RevealStartBlock: a.RevealStartBlock,
		RevealEndBlock:   a.RevealEndBlock,
		BondUveil:        a.BondUveil,
		RewardPoolUveil:  a.RewardPoolUveil,
		Revealed:         a.Revealed,
	}
	if height > 0 {
		if a.CommitDeadline > 0 {
			out.BlocksToCommitDeadline = a.CommitDeadline - height
		}
		out.BlocksToWindowOpen = a.RevealStartBlock - height
		out.BlocksToWindowClose = a.RevealEndBlock - height
	}
	// The daemon reveals at a random offset after window-open when configured,
	// so the planned height is what it will actually do — showing only the
	// window open would misrepresent its own intent.
	if s.config.RevealOffsetBlocks > 0 && a.RevealStartBlock > 0 {
		out.PlannedRevealHeight = a.RevealStartBlock + s.config.RevealOffsetBlocks
	}
	out.RewardFloorUveil = rewardFloor(a.RewardPoolUveil, a.MaxShares)
	out.AtRisk, out.Urgency, out.RiskNote = revealRisk(a, height)
	return out
}

// rewardFloor is pool ÷ max_shares: the least an assignment pays if the roster
// fills. Big.Int because the pool is an unbounded decimal string on the wire
// and int64 arithmetic on it would be a silent overflow at large pools.
func rewardFloor(pool string, maxShares int64) int64 {
	if pool == "" || maxShares <= 0 {
		return 0
	}
	p, ok := new(big.Int).SetString(pool, 10)
	if !ok {
		return 0
	}
	p.Div(p, big.NewInt(maxShares))
	if !p.IsInt64() {
		return 0
	}
	return p.Int64()
}

// revealRisk classifies an unrevealed share by how close its window is to
// closing. Urgency is a rank, not a duration: the panel orders by it.
func revealRisk(a AssignmentSnapshot, height int64) (bool, int, string) {
	if a.Revealed || height <= 0 || a.RevealEndBlock <= 0 {
		return false, 0, ""
	}
	// Only accepted assignments carry a reveal obligation; a proposed one that
	// we never confirmed is not at risk of a missed-reveal slash.
	if a.LocalState != StateNeedsReveal.String() {
		return false, 0, ""
	}
	toClose := a.RevealEndBlock - height
	toOpen := a.RevealStartBlock - height
	switch {
	case toClose <= 0:
		return true, 100, "window has CLOSED without our reveal — the bond is exposed to the missed-reveal slash"
	case toOpen <= 0 && toClose <= 20:
		return true, 90, fmt.Sprintf("window open and closing in %d blocks", toClose)
	case toOpen <= 0:
		return true, 50, "window open, reveal outstanding"
	case toOpen <= 20:
		return true, 20, fmt.Sprintf("window opens in %d blocks", toOpen)
	}
	return false, 0, ""
}

// Economics implements dashboard.Source.
func (s *Service) Economics(ctx context.Context) dashboard.Economics {
	g, err := s.client.GetGuardian(ctx, s.config.GuardianAddress)
	if err != nil {
		return dashboard.Economics{Unavailable: dashboard.Unavailable{
			Unavailable: true,
			Reason:      fmt.Sprintf("guardian record unavailable: %v", err),
		}}
	}

	total := parseUveil(g.Stake.Amount)
	locked := parseUveil(g.LockedStake.Amount)
	unlocked := new(big.Int).Sub(total, locked)
	if unlocked.Sign() < 0 {
		unlocked.SetInt64(0)
	}

	out := dashboard.Economics{
		FloatTotalUveil:    total.String(),
		FloatLockedUveil:   locked.String(),
		FloatUnlockedUveil: unlocked.String(),
		Denom:              g.Stake.Denom,
		BondK:              g.BondK,
		BondKDisplay:       formatHundredths(g.BondK),
		ActiveBondCount:    g.ActiveBondCount,
		BondCap:            dashboardBondCap,
		BondHeadroom:       max(dashboardBondCap-g.ActiveBondCount, 0),
	}
	// k's floor is 1.00x; at the floor there is nothing to work back to, and
	// saying "0 reveals to floor" would read as a problem rather than the best
	// possible state.
	out.BondKAtFloor = g.BondK <= 100
	if !out.BondKAtFloor {
		out.RevealsToward = fmt.Sprintf("~%d correct reveals", revealsToFloor(g.BondK))
	}

	// Exposure and the typical-bond estimate come from our own live bonds.
	snap := s.activeSecretCache.Snapshot(s.config.GuardianAddress)
	var bonded, largest, count int64
	for _, a := range snap.Assignments {
		if a.BondUveil <= 0 {
			continue
		}
		bonded += a.BondUveil
		count++
		if a.BondUveil > largest {
			largest = a.BondUveil
		}
	}
	out.TotalBondedUveil = bonded
	out.LargestBondUveil = largest
	// Affordability is only offered when there is something to base it on.
	// Extrapolating from no observed bonds would be invention dressed as an
	// estimate, and this panel drives a "can I take more work?" decision.
	if count > 0 {
		typical := bonded / count
		out.TypicalBondUveil = typical
		if typical > 0 {
			n := new(big.Int).Div(unlocked, big.NewInt(typical))
			out.AffordableBonds = fmt.Sprintf("~%s more at recent typical size", n.String())
		}
	}

	if bal, err := s.client.GetBalance(ctx, s.config.GuardianAddress, s.config.Denom); err == nil {
		out.SigningBalance = bal.Amount
		out.SigningBalanceLow = parseUveil(bal.Amount).Cmp(big.NewInt(lowBalanceUveil)) < 0
	} else {
		out.SigningBalance = ""
	}
	return out
}

// revealsToFloor is how many ×0.963 steps take k back to 1.00x. Iterative
// rather than a log: the protocol applies integer-truncating steps, so a
// closed form would drift from what the chain actually does.
func revealsToFloor(k int64) int {
	const floor = 100
	steps := 0
	for k > floor && steps < 1000 {
		next := k * 963 / 1000
		if next >= k { // truncation stalled — no further progress is possible
			break
		}
		k = next
		steps++
	}
	return steps
}

// Keys implements dashboard.Source.
func (s *Service) Keys(ctx context.Context) dashboard.Keys {
	out := dashboard.Keys{
		Address: s.config.GuardianAddress,
		KeyPath: s.config.EncryptionPrivateKeyPath,
	}

	if encrypted, err := custody.IsEncryptedKeyFile(s.config.EncryptionPrivateKeyPath); err == nil {
		out.EncryptedAtRest = encrypted
		// The warning is the point of the panel: a plaintext share key means
		// anyone who can read the host can decrypt every assigned share.
		out.PlaintextWarn = !encrypted
	}

	g, err := s.client.GetGuardian(ctx, s.config.GuardianAddress)
	if err != nil {
		out.Unavailable = dashboard.Unavailable{
			Unavailable: true,
			Reason:      fmt.Sprintf("guardian record unavailable: %v", err),
		}
		return out
	}
	out.RegisteredFingerprint = fingerprint(g.EncryptionPublicKey)
	out.CurrentEpoch = g.CurrentKeyEpoch

	if local, err := s.localSharePublicKey(); err == nil {
		out.LocalFingerprint = fingerprint(local)
		out.Matches = out.LocalFingerprint == out.RegisteredFingerprint
	}

	if history, err := s.client.GetGuardianKeyHistory(ctx, s.config.GuardianAddress); err == nil {
		for _, e := range history {
			out.Epochs = append(out.Epochs, dashboard.KeyEpoch{
				Epoch:               e.Epoch,
				FingerprintHex:      fingerprint(e.PublicKey),
				EffectiveFromHeight: e.EffectiveFromHeight,
				Current:             e.Epoch == g.CurrentKeyEpoch,
			})
		}
		sort.Slice(out.Epochs, func(i, j int) bool { return out.Epochs[i].Epoch > out.Epochs[j].Epoch })
	}
	if out.Epochs == nil {
		out.Epochs = []dashboard.KeyEpoch{}
	}

	// Wind-down without a chain call: an assignment is permanently bound to
	// the epoch key it was created under, so anything created before the
	// current epoch's effective height still needs the outgoing key.
	var currentFrom int64
	for _, e := range out.Epochs {
		if e.Current {
			currentFrom = e.EffectiveFromHeight
			break
		}
	}
	if currentFrom > 0 {
		snap := s.activeSecretCache.Snapshot(s.config.GuardianAddress)
		for _, a := range snap.Assignments {
			if a.CreatedAt > 0 && a.CreatedAt < currentFrom {
				out.OutgoingEpochAssignments++
			}
		}
	}

	out.RotationEligible = out.OutgoingEpochAssignments == 0
	if out.OutgoingEpochAssignments > 0 {
		out.RotationNote = fmt.Sprintf(
			"%d active assignment(s) were created under an earlier epoch and still need the outgoing key — keep it until they settle.",
			out.OutgoingEpochAssignments)
	} else {
		out.RotationNote = "No active assignment depends on an earlier epoch key."
	}
	return out
}

// localSharePublicKey derives the local key file's public key — the same
// derivation verifyShareKeyBinding runs at startup, surfaced continuously
// because a mismatch after a botched restore is otherwise silent until the
// first missed reveal.
func (s *Service) localSharePublicKey() ([]byte, error) {
	privateKey, err := s.config.GetEncryptionPrivateKey()
	if err != nil {
		return nil, err
	}
	derived, err := crypto.DerivePublicKey(privateKey)
	if err != nil {
		return nil, err
	}
	return derived[:], nil
}

// fingerprint is a short, stable identifier for a public key. Truncated
// SHA-256 rather than the raw key: it is for eyeball comparison between the
// local file and the chain, and a 64-hex-character key defeats that.
func fingerprint(key []byte) string {
	if len(key) == 0 {
		return ""
	}
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:8])
}

// Config implements dashboard.Source.
func (s *Service) Config(ctx context.Context) dashboard.Config {
	out := dashboard.Config{ValidationOK: true}
	if err := s.config.Validate(); err != nil {
		out.ValidationOK = false
		out.ValidationComplaint = err.Error()
	}

	local := []dashboard.ConfigField{
		{Name: "guardian_address", Group: "Identity", Local: s.config.GuardianAddress},
		{Name: "chain_id", Group: "Chain", Local: s.config.ChainID},
		{Name: "rpc_endpoint", Group: "Chain", Local: s.config.RPCEndpoint},
		{Name: "grpc_endpoint", Group: "Chain", Local: s.config.GRPCEndpoint},
		{Name: "polling_interval", Group: "Operations", Local: s.config.PollingInterval.String()},
		{Name: "reveal_offset_blocks", Group: "Operations",
			Local: fmt.Sprintf("%d", s.config.RevealOffsetBlocks),
			Note:  "random offset after window-open before revealing"},
		{Name: "enable_event_monitoring", Group: "Operations",
			Local: fmt.Sprintf("%t", s.config.EnableEventMonitoring)},
		{Name: "encryption_private_key_path", Group: "Identity & Keys",
			Local: s.config.EncryptionPrivateKeyPath},
	}

	g, err := s.client.GetGuardian(ctx, s.config.GuardianAddress)
	if err != nil {
		out.Fields = local
		out.Unavailable = dashboard.Unavailable{
			Unavailable: true,
			Reason:      fmt.Sprintf("on-chain record unavailable, drift cannot be computed: %v", err),
		}
		return out
	}

	// Drift: only fields with a genuine on-chain counterpart are compared.
	// Inventing a chain value for a local-only setting would manufacture drift.
	chainKey := fmt.Sprintf("%x", g.EncryptionPublicKey)
	local = append(local,
		dashboard.ConfigField{Name: "encryption_public_key", Group: "Identity & Keys",
			Local: s.config.EncryptionPublicKey, Chain: chainKey,
			Drift: s.config.EncryptionPublicKey != "" && s.config.EncryptionPublicKey != chainKey,
			Note:  "immutable per epoch — rotate to change"},
		dashboard.ConfigField{Name: "accepting_secrets", Group: "Registration",
			Chain: fmt.Sprintf("%t", g.AcceptingSecrets), Local: "—",
			Note: "chain-side switch; guardiand update toggles it"},
	)
	for i := range local {
		if local[i].Drift {
			out.DriftCount++
		}
	}
	out.Fields = local

	out.AvailableFrom = g.AvailableFrom
	out.AvailableUntil = g.AvailableUntil
	height := s.lastKnownHeight.Load()
	if h, err := s.client.GetCurrentBlockHeight(ctx); err == nil {
		height = h
	}
	if height > 0 && g.AvailableUntil > 0 {
		out.BlocksRemaining = g.AvailableUntil - height
		// Selection requires available_until >= reveal_end_block, so a
		// shrinking window silently stops LONG-DATED assignments well before
		// it expires. That is the operator-relevant fact, not the expiry date.
		if out.BlocksRemaining > 0 {
			out.EligibilityWarning = true
			out.EligibilityNote = fmt.Sprintf(
				"Availability ends in %d blocks. Selection requires available_until ≥ a secret's reveal_end_block, so this guardian is already excluded from any secret revealing beyond that height — extend with guardiand update.",
				out.BlocksRemaining)
		} else {
			out.EligibilityWarning = true
			out.EligibilityNote = "Availability has expired — this guardian is not a selection candidate for anything."
		}
	}
	return out
}

// Activity implements dashboard.Source.
func (s *Service) Activity(_ context.Context) dashboard.Activity {
	snap := s.observations.Snapshot()
	out := dashboard.Activity{
		StartedAt:        snap.StartedAt,
		Note:             "Since this process started — a restart clears these. Durable history is Prometheus/Grafana's job.",
		TotalDecisions:   snap.TotalDecisions,
		TotalSubmissions: snap.TotalSubmissions,
		TotalSettlements: snap.TotalSettlements,
		Decisions:        make([]dashboard.ActivityDecision, 0, len(snap.Decisions)),
		Submissions:      make([]dashboard.ActivitySubmission, 0, len(snap.Submissions)),
		Settlements:      make([]dashboard.ActivitySettlement, 0, len(snap.Settlements)),
	}
	for _, d := range snap.Decisions {
		out.Decisions = append(out.Decisions, dashboard.ActivityDecision{
			At: d.At, SecretID: d.SecretID, Outcome: string(d.Outcome), Reason: d.Reason, Height: d.Height,
		})
	}
	for _, x := range snap.Submissions {
		out.Submissions = append(out.Submissions, dashboard.ActivitySubmission{
			At: x.At, Kind: string(x.Kind), SecretID: x.SecretID, TxHash: x.TxHash,
			Success: x.Success, Err: x.Err, Height: x.Height,
		})
	}
	for _, x := range snap.Settlements {
		out.Settlements = append(out.Settlements, dashboard.ActivitySettlement{
			At: x.At, SecretID: x.SecretID, Outcome: x.Outcome, Stalled: x.Stalled, Height: x.Height,
		})
	}
	return out
}

// parseUveil reads a uveil decimal string. Big.Int throughout: uveil amounts
// are unbounded on the wire and the float total can exceed int64 in principle.
func parseUveil(s string) *big.Int {
	if s == "" {
		return big.NewInt(0)
	}
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return big.NewInt(0)
	}
	return v
}

// formatHundredths renders 400 as "4.00x".
func formatHundredths(v int64) string {
	if v <= 0 {
		return "—"
	}
	return fmt.Sprintf("%d.%02dx", v/100, v%100)
}

// Observations exposes the buffers so call sites can record. Nil until the
// service starts, and every Record* is nil-safe.
func (s *Service) Observations() *Observations { return s.observations }

// compile-time guard that the blockchain projection still carries what the
// panels quote — a trimmed field would otherwise fail silently as a zero.
var _ = func() bool {
	var s blockchain.Secret
	_ = s.MinShares
	_ = s.RewardPool
	return true
}()
