package blockchain

import (
	secretstypes "github.com/timeflareio/chain/x/secrets/types"
)

// Coin is a denom/amount pair in string form for display and comparison.
type Coin struct {
	Denom  string `json:"denom"`
	Amount string `json:"amount"`
}

// Guardian is the guardian's on-chain state, mapped from the typed gRPC
// response — no string-typed heights, no JSON scraping.
type Guardian struct {
	Address             string `json:"address"`
	EncryptionPublicKey []byte `json:"encryption_public_key"` // current epoch's key
	CurrentKeyEpoch     uint64 `json:"current_key_epoch"`     // 0 at registration, +1 per rotation
	AvailableFrom       int64  `json:"available_from"`
	AvailableUntil      int64  `json:"available_until"`
	Stake               Coin   `json:"stake"`        // float total (deposited working capital)
	LockedStake         Coin   `json:"locked_stake"` // portion locked as per-secret bonds
	AcceptingSecrets    bool   `json:"accepting_secrets"`
	// BondK is the live reputation multiplier in hundredths (400 = 4.00x): it
	// prices every new bond, so it is the single number that most affects what
	// this guardian can afford. ActiveBondCount is the concurrency position
	// against the per-guardian cap.
	BondK           int64 `json:"bond_k"`
	ActiveBondCount int64 `json:"active_bond_count"`
}

// KeyEpoch is one entry of a guardian's append-only key history. The epoch in
// force at height h is the newest entry with EffectiveFromHeight <= h — the
// derivation the daemon runs (from a secret's creation height) to resolve
// which key an assignment was encrypted to.
type KeyEpoch struct {
	Epoch               uint64 `json:"epoch"`
	PublicKey           []byte `json:"public_key"`
	EffectiveFromHeight int64  `json:"effective_from_height"`
}

// AvailableAt reports whether the guardian's availability window covers the
// given height.
func (g *Guardian) AvailableAt(height int64) bool {
	return height >= g.AvailableFrom && height < g.AvailableUntil
}

// Secret is a secret's state as the guardian consumes it.
type Secret struct {
	ID                  string               `json:"id"`
	Creator             string               `json:"creator"`
	State               string               `json:"state"`
	Threshold           int64                `json:"threshold"`
	RevealStartBlock    int64                `json:"reveal_start_block"`
	RevealEndBlock      int64                `json:"reveal_end_block"`
	CreatedAt           int64                `json:"created_at"` // creation (= selection) height: the key-epoch derivation input
	GuardianAssignments []GuardianAssignment `json:"guardian_assignments"`
	RevealedShares      []RevealedShare      `json:"revealed_shares"`

	// Economics carried for the operator dashboard. Already returned by the
	// secret query and simply dropped before — the guardian's own work needs
	// none of it, but an operator cannot see what a secret is worth or what it
	// costs them without it. No protocol surface changes by keeping it.
	CommitDeadline int64 `json:"commit_deadline"`
	MinShares      int64 `json:"min_shares"`
	MaxShares      int64 `json:"max_shares"`
	AcceptedCount  int64 `json:"accepted_count"`
	RewardPool     Coin  `json:"reward_pool"`
	// OurBondUveil is this daemon's own frozen bond for the secret, resolved
	// from guardian_bond_amounts by position in selected_guardians. Zero when
	// we are not among the selected — the alignment is positional, so reading
	// it by index without matching the address would silently report another
	// guardian's bond as our own.
	OurBondUveil int64 `json:"our_bond_uveil"`

	// bondsByGuardian backs BondFor. Unexported: the positional wire alignment
	// is an encoding detail, and exposing it would invite index-based reads.
	bondsByGuardian map[string]int64
}

// BondFor returns the frozen bond for one guardian address, and whether the
// secret carries a bond for it at all.
func (s Secret) BondFor(address string) (int64, bool) {
	v, ok := s.bondsByGuardian[address]
	return v, ok
}

// GuardianAssignment is one guardian's assignment to a secret.
type GuardianAssignment struct {
	GuardianAddress string `json:"guardian_address"`
	EncryptedShare  []byte `json:"encrypted_share"`
	ShareHMAC       []byte `json:"share_hmac"`
	Status          string `json:"status"` // AssignmentStatus enum name, e.g. ASSIGNMENT_STATUS_PROPOSED
}

// RevealedShare records a share already revealed on-chain for a secret.
type RevealedShare struct {
	GuardianAddress string `json:"guardian_address"`
	RevealedAtBlock int64  `json:"revealed_at_block"`
}

// HasRevealed reports whether the given guardian has already revealed its
// share for this secret.
func (s *Secret) HasRevealed(guardianAddr string) bool {
	for _, rs := range s.RevealedShares {
		if rs.GuardianAddress == guardianAddr {
			return true
		}
	}
	return false
}

// Balance represents account balance information
type Balance struct {
	Denom  string `json:"denom"`
	Amount string `json:"amount"`
}

// secretFromView maps the query-service SecretView onto the guardian's domain
// type.
func secretFromView(v *secretstypes.SecretView) Secret {
	s := Secret{
		ID:               v.Id,
		Creator:          v.Creator,
		State:            v.State,
		Threshold:        v.Threshold,
		RevealStartBlock: v.RevealStartBlock,
		RevealEndBlock:   v.RevealEndBlock,
		CreatedAt:        v.CreatedAt,
		CommitDeadline:   v.CommitDeadline,
		MinShares:        v.MinShares,
		MaxShares:        v.MaxShares,
		AcceptedCount:    v.AcceptedCount,
		RewardPool:       Coin{Denom: v.RewardPool.Denom, Amount: v.RewardPool.Amount.String()},
	}
	for _, a := range v.GuardianAssignments {
		if a == nil {
			continue
		}
		s.GuardianAssignments = append(s.GuardianAssignments, GuardianAssignment{
			GuardianAddress: a.GuardianAddress,
			EncryptedShare:  a.EncryptedShare,
			ShareHMAC:       a.ShareHmac,
			Status:          a.Status.String(),
		})
	}
	for _, r := range v.RevealedShares {
		if r == nil {
			continue
		}
		s.RevealedShares = append(s.RevealedShares, RevealedShare{
			GuardianAddress: r.GuardianAddress,
			RevealedAtBlock: r.RevealedAtBlock,
		})
	}
	s.bondsByGuardian = bondsByGuardian(v.SelectedGuardians, v.GuardianBondAmounts)
	return s
}

// bondsByGuardian pairs selected_guardians with guardian_bond_amounts. The
// wire alignment is POSITIONAL, so this keys by address instead of leaving
// callers to index — indexing by position without checking the address is how
// one guardian ends up reading another's bond as its own. A length mismatch
// yields no bonds rather than a guess.
func bondsByGuardian(addresses []string, amounts []int64) map[string]int64 {
	if len(addresses) == 0 || len(addresses) != len(amounts) {
		return nil
	}
	out := make(map[string]int64, len(addresses))
	for i, addr := range addresses {
		out[addr] = amounts[i]
	}
	return out
}

// guardianFromProto maps the on-chain Guardian record onto the domain type.
func guardianFromProto(g *secretstypes.Guardian) *Guardian {
	out := &Guardian{
		Address:             g.Address,
		EncryptionPublicKey: g.EncryptionPublicKey,
		CurrentKeyEpoch:     g.CurrentKeyEpoch,
		AvailableFrom:       g.AvailableFrom,
		AvailableUntil:      g.AvailableUntil,
		BondK:               g.BondK,
		ActiveBondCount:     g.ActiveBondCount,
		AcceptingSecrets:    g.AcceptingSecrets,
	}
	if g.Stake != nil {
		out.Stake = Coin{Denom: g.Stake.Denom, Amount: g.Stake.Amount.String()}
	}
	if g.LockedStake != nil {
		out.LockedStake = Coin{Denom: g.LockedStake.Denom, Amount: g.LockedStake.Amount.String()}
	}
	return out
}
