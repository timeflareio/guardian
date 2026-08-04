// Package dashboard serves the guardian operator's read-only dashboard: a
// single embedded page plus the JSON snapshots it polls (dashboard plan §2).
//
// It is a package inside the guardian module, not a new component — the
// monitoring service already runs HTTP listeners and the daemon already holds
// every fact shown here.
//
// # Import direction
//
// This package must NOT import guardian/guardian. The daemon imports
// monitoring, monitoring mounts these handlers, so a dependency the other way
// would close a cycle. Instead the data arrives through Source, which the
// guardian service implements — the same inversion SetObservability already
// uses for metrics and health.
//
// # Read-only, and safe to serve unauthenticated
//
// Nothing here signs, and nothing mutates daemon state — every route is a GET,
// which is why no CSRF defence is included. Everything that changes a guardian
// lives in guardianctl: registration updates, key rotation, configuration. The
// page says so in its footer rather than offering any of it.
//
// There is no credential and no TLS, and that is a constraint on the content
// rather than an omission. The rule is:
//
//	Every field served here must be something the chain already publishes
//	about this guardian, or plain liveness. Nothing host-local.
//
// So: address, assignments, bonds, balances, key fingerprints, epochs and the
// registration record — all of which anyone can already query — plus uptime,
// heights and whether the daemon is running. No filesystem paths, no RPC or
// gRPC endpoints, no local configuration dump, and nothing about key custody
// posture. That last one is the sharpest: whether the share key is encrypted at
// rest names the guardians worth attacking, a targeting signal no amount of
// authentication makes safe to compute.
//
// A field that breaks the rule does not get a password put in front of it — it
// gets left out, or the rule gets revisited deliberately. Auth on an operator
// page is one shared credential, which is a delay rather than an access model.
package dashboard

import (
	"context"
	"time"
)

// Source is everything the dashboard needs from the daemon. The guardian
// service implements it.
//
// Every method must be safe to call from an HTTP handler goroutine while the
// daemon works, and must not block on the chain for long: handlers serve a
// polling UI, so a slow method shows as a stalled panel. Implementations
// return whatever they have and report staleness rather than waiting.
type Source interface {
	// Vitals is process- and connection-level state (panel 1).
	Vitals(ctx context.Context) Vitals
	// Assignments is the active-secret cache snapshot (panels 2, 3, 5).
	Assignments(ctx context.Context) Assignments
	// Economics is float, bond multiplier, exposure and balance
	// (panels 8-12). It reads the chain, so it may report Unavailable.
	Economics(ctx context.Context) Economics
	// Keys is key identity and rotation state (panels 13, 14). Fingerprints
	// and epochs only — never where the key lives or how it is stored.
	Keys(ctx context.Context) Keys
	// Registration is the guardian's on-chain record: its availability window
	// and the two settings the local daemon can disagree with it about
	// (panel 15).
	Registration(ctx context.Context) Registration
	// Activity is the since-start observation buffers (panels 4, 6, 7).
	Activity(ctx context.Context) Activity
}

// Unavailable marks a section the daemon could not assemble — almost always a
// chain query that failed. Panels render the reason rather than showing zeros,
// because a zeroed float panel and an unreachable node look identical
// otherwise, and one of them is an emergency.
type Unavailable struct {
	Unavailable bool   `json:"unavailable"`
	Reason      string `json:"reason,omitempty"`
}

// Vitals — panel 1.
type Vitals struct {
	Unavailable `json:",inline"`

	GuardianAddress string `json:"guardian_address"`
	ChainID         string `json:"chain_id"`
	Version         string `json:"version"`
	// The endpoints this daemon dials and the paths it reads belong to the
	// operator's infrastructure rather than the guardian's chain-visible state,
	// so they stay on the host: `guardianctl config doctor` reports them.
	StartedAt       time.Time     `json:"started_at"`
	Uptime          string        `json:"uptime"`
	UptimeSeconds   int64         `json:"uptime_seconds"`
	Running         bool          `json:"running"`
	Registered      bool          `json:"registered"`
	AcceptingWork   bool          `json:"accepting_secrets"`
	Healthy         bool          `json:"healthy"`
	LastBlockHeight int64         `json:"last_block_height"`
	ChainHeight     int64         `json:"chain_height"`
	HeightLag       int64         `json:"height_lag"`
	EventStream     string        `json:"event_stream"`
	PollingInterval time.Duration `json:"-"`
	PollingHuman    string        `json:"polling_interval"`
	LastUpdate      time.Time     `json:"last_update"`
}

// Assignment is one row of the active-assignments table (panel 5), also
// feeding the work queue (2) and at-risk view (3).
type Assignment struct {
	SecretID         string `json:"secret_id"`
	ChainState       string `json:"chain_state"`
	LocalState       string `json:"local_state"`
	AssignmentStatus string `json:"assignment_status"`

	Threshold      int64 `json:"threshold"`
	MinShares      int64 `json:"min_shares"`
	MaxShares      int64 `json:"max_shares"`
	AcceptedCount  int64 `json:"accepted_count"`
	CommitDeadline int64 `json:"commit_deadline"`

	RevealStartBlock int64 `json:"reveal_start_block"`
	RevealEndBlock   int64 `json:"reveal_end_block"`
	// Countdowns in blocks from the current height; negative means passed.
	BlocksToCommitDeadline int64 `json:"blocks_to_commit_deadline"`
	BlocksToWindowOpen     int64 `json:"blocks_to_window_open"`
	BlocksToWindowClose    int64 `json:"blocks_to_window_close"`

	BondUveil       int64  `json:"bond_uveil"`
	RewardPoolUveil string `json:"reward_pool_uveil"`
	// RewardFloorUveil is pool ÷ max_shares: the least this assignment pays if
	// the roster fills. A floor, not a promise — a smaller accepted roster
	// divides the same pool fewer ways, so the actual share can only be higher.
	RewardFloorUveil int64 `json:"reward_floor_uveil"`

	Revealed bool `json:"revealed"`
	// Urgency ranks the at-risk view: higher is more urgent. Only meaningful
	// for unrevealed shares whose window is open or approaching.
	Urgency  int    `json:"urgency"`
	AtRisk   bool   `json:"at_risk"`
	RiskNote string `json:"risk_note,omitempty"`
}

// Assignments — panels 2, 3 and 5.
type Assignments struct {
	Unavailable `json:",inline"`

	CurrentHeight int64        `json:"current_height"`
	Active        []Assignment `json:"active"`
	// AwaitingConfirmation and PendingReveal are the work queue: assignments
	// needing offline verification, and accepted shares not yet revealed.
	AwaitingConfirmation []Assignment   `json:"awaiting_confirmation"`
	PendingReveal        []Assignment   `json:"pending_reveal"`
	AtRisk               []Assignment   `json:"at_risk"`
	StateCounts          map[string]int `json:"state_counts"`
}

// Economics — panels 8 to 12.
type Economics struct {
	Unavailable `json:",inline"`

	FloatTotalUveil    string `json:"float_total_uveil"`
	FloatLockedUveil   string `json:"float_locked_uveil"`
	FloatUnlockedUveil string `json:"float_unlocked_uveil"`
	Denom              string `json:"denom"`

	// BondK is hundredths (400 = 4.00x) — the multiplier pricing every new
	// bond, hence the number that most constrains what this guardian can take.
	BondK         int64  `json:"bond_k"`
	BondKDisplay  string `json:"bond_k_display"`
	BondKAtFloor  bool   `json:"bond_k_at_floor"`
	RevealsToward string `json:"reveals_to_floor,omitempty"`

	ActiveBondCount int64 `json:"active_bond_count"`
	BondCap         int64 `json:"bond_cap"`
	BondHeadroom    int64 `json:"bond_headroom"`

	// AffordableBonds estimates how many more bonds the unlocked float covers
	// at the recent typical bond size. Empty when nothing has been bonded yet
	// — an estimate from no observations would be invention.
	AffordableBonds  string `json:"affordable_bonds,omitempty"`
	TypicalBondUveil int64  `json:"typical_bond_uveil,omitempty"`

	TotalBondedUveil  int64  `json:"total_bonded_uveil"`
	LargestBondUveil  int64  `json:"largest_bond_uveil"`
	SigningBalance    string `json:"signing_balance"`
	SigningBalanceLow bool   `json:"signing_balance_low"`
}

// KeyEpoch is one entry of the on-chain key history (panel 14).
type KeyEpoch struct {
	Epoch               uint64 `json:"epoch"`
	FingerprintHex      string `json:"fingerprint"`
	EffectiveFromHeight int64  `json:"effective_from_height"`
	Current             bool   `json:"current"`
}

// Keys — panels 13 and 14.
type Keys struct {
	Unavailable `json:",inline"`

	Address string `json:"address"`
	// RegisteredFingerprint is the on-chain key's fingerprint; Local is the
	// key file's. Matches is the startup self-check, shown continuously
	// because a mismatch after a botched restore is silent otherwise.
	RegisteredFingerprint string `json:"registered_fingerprint"`
	LocalFingerprint      string `json:"local_fingerprint"`
	Matches               bool   `json:"fingerprints_match"`
	// Both fingerprints are of public keys the chain already carries, so a
	// mismatch is safe to state. Nothing about the private key belongs here —
	// neither where it lives nor whether it is encrypted at rest, the second
	// being a targeting signal. `guardianctl config doctor` answers both on the
	// host, where whoever is asking can already read the key.

	CurrentEpoch uint64     `json:"current_epoch"`
	Epochs       []KeyEpoch `json:"epochs"`
	// OutgoingEpochAssignments counts active assignments created under an
	// earlier epoch. Rotation wind-down needs no chain call: an assignment is
	// permanently bound to the epoch key it was created under.
	OutgoingEpochAssignments int    `json:"outgoing_epoch_assignments"`
	RotationEligible         bool   `json:"rotation_eligible"`
	RotationNote             string `json:"rotation_note,omitempty"`
}

// RegistrationField is one setting the chain holds and the daemon can disagree
// with, with its drift overlay (panel 15).
//
// Only settings with a genuine on-chain counterpart appear, which is what keeps
// this safe to serve: both sides of every comparison are already public.
// Local-only settings — endpoints, key paths, polling timings — belong to
// `guardianctl config`, not here.
type RegistrationField struct {
	Name  string `json:"name"`
	Local string `json:"local"`
	// Chain is the registered value where one exists; Drift marks disagreement.
	Chain string `json:"chain,omitempty"`
	Drift bool   `json:"drift"`
	Note  string `json:"note,omitempty"`
}

// Registration — panel 15: the guardian's on-chain record.
type Registration struct {
	Unavailable `json:",inline"`

	Fields []RegistrationField `json:"fields"`
	// Availability overlay: the countdown, and the eligibility warning. A
	// shrinking available_until stops long-dated assignments well BEFORE it
	// expires, because selection requires available_until >= reveal_end_block.
	AvailableFrom      int64  `json:"available_from"`
	AvailableUntil     int64  `json:"available_until"`
	BlocksRemaining    int64  `json:"blocks_remaining"`
	EligibilityWarning bool   `json:"eligibility_warning"`
	EligibilityNote    string `json:"eligibility_note,omitempty"`
	DriftCount         int    `json:"drift_count"`
	// ConfigValid reports whether the local configuration passes validation.
	// The complaint itself is not served: validation messages quote the values
	// that failed, which would carry a path or an endpoint onto a page that
	// holds neither. `guardianctl config doctor --config-only` prints it on the
	// host.
	ConfigValid bool `json:"config_valid"`
}

// Activity — panels 4, 6 and 7, all since process start.
type Activity struct {
	Unavailable `json:",inline"`

	StartedAt time.Time `json:"started_at"`
	// Note states the since-start limitation on every response, so a panel
	// cannot present a restarted daemon's empty history as "nothing happened".
	Note string `json:"note"`

	Decisions        []ActivityDecision   `json:"decisions"`
	Submissions      []ActivitySubmission `json:"submissions"`
	Settlements      []ActivitySettlement `json:"settlements"`
	TotalDecisions   int                  `json:"total_decisions"`
	TotalSubmissions int                  `json:"total_submissions"`
	TotalSettlements int                  `json:"total_settlements"`
}

// ActivityDecision mirrors guardian.Decision without importing it.
type ActivityDecision struct {
	At       time.Time `json:"at"`
	SecretID string    `json:"secret_id"`
	Outcome  string    `json:"outcome"`
	Reason   string    `json:"reason,omitempty"`
	Height   int64     `json:"height,omitempty"`
}

// ActivitySubmission mirrors guardian.Submission.
//
// The failure text is not carried: a broadcast error quotes the endpoint it
// could not reach, which would leak the operator's infrastructure. Success is
// enough to see that something is wrong; the daemon log says what.
type ActivitySubmission struct {
	At       time.Time `json:"at"`
	Kind     string    `json:"kind"`
	SecretID string    `json:"secret_id"`
	TxHash   string    `json:"tx_hash,omitempty"`
	Success  bool      `json:"success"`
	Height   int64     `json:"height,omitempty"`
}

// ActivitySettlement mirrors guardian.Settlement.
type ActivitySettlement struct {
	At       time.Time `json:"at"`
	SecretID string    `json:"secret_id"`
	Outcome  string    `json:"outcome"`
	Stalled  bool      `json:"stalled"`
	Height   int64     `json:"height,omitempty"`
}
