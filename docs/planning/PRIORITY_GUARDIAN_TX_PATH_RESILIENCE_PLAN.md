# Guardian transaction-path resilience

| | |
|---|---|
| **Priority** | P0 |
| **Status** | refining |
| **Created** | 1 August 2026 |
| **Updated** | 11 November 2026 |
| **Protocol surface** | none |
| **Repositories** | guardian |
| **Depends on** | — |
| **Related** | [PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md](PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md); [DONE_GUARDIAN_NETWORK_SELECTION_PLAN.md](done/DONE_GUARDIAN_NETWORK_SELECTION_PLAN.md) |
| **Open questions** | 5 |

**Why this priority**: every defect here converts an ordinary operational event
(a stalled node, a restart, a copied config) into a missed reveal window, and a
missed window is 40% of the bond burned plus 10% to the creator.
**Origin**: [PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md](PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md)
findings 3, 15, 20, 22, 24, and the sequence-detection item in 41.
**Components**: `internal/chain/signer.go`, `transport.go`, `client.go`,
`interface.go`, `errors.go`; `internal/guardian/service.go`, `shutdown.go`;
`internal/cli/start.go`; `internal/config/config.go`; a test file beside
`signer.go` (new — `internal/chain/` has none for the signer today),
`internal/guardian/service_test.go`.

**A done plan now rests on three phases here.**
[DONE_GUARDIAN_NETWORK_SELECTION_PLAN.md](done/DONE_GUARDIAN_NETWORK_SELECTION_PLAN.md)
has `guardianctl config init` take `chain_id` and both endpoints from the
network registry the chain publishes (`timeflareio/chain` — `networks.json`).
It names this plan's gas ceiling (phase 3) and chain-id assertion (phase 4) as
the only bounds on a substituted endpoint — "both matter more once endpoints
arrive from a published list rather than from the operator" — and it narrows
the registry's endpoint *lists* to their first element until phase 6 here
widens the configuration.

---

## The issue

The daemon's read path is defended and its write path is not. `withRetry`
(`internal/chain/client.go:75`) wraps every query in `RequestTimeout` with
status-code retry classification; none of the five transaction methods
(`client.go:293-360`) go through it, and nothing else bounds them.

### 1. No deadline and no keepalive on the transaction path

`SubmitTx` (`internal/chain/signer.go:162`) takes the per-account mutex and
holds it across three unbounded network calls: account/sequence retrieval
(`:179`), gas simulation (`:214`) and broadcast (`:237`). The context it
receives is the daemon's **root** run context:

```
internal/guardian/service.go:437 processSecrets(ctx)
  → processReveals → internal/guardian/reveal.go:192 GuardianRevealShare(ctx, …)
  → internal/chain/client.go:353 → signer.SubmitTx(ctx, msg)
```

The connection is dialled with transport credentials and nothing else — no
keepalive (`Dial`, `internal/chain/transport.go`) — so a half-open socket —
node freeze, NAT drop with no RST — blocks a broadcast for the OS TCP
retransmission timeout, on the order of fifteen minutes.

The failure cascades rather than staying local. The blocked worker holds
`s.mu`; every other reveal worker blocks on it; `processReveals`'s `wg.Wait()`
(`service.go:561`) blocks its caller — including `onNewHeight`, which runs
*synchronously* inside the event monitor's select loop
(`internal/guardian/events.go`), so block-header processing stops as well.
Every reveal window open during that period is a no-reveal slash.

The one existing mitigation is indirect: `/ready` trips after
`max(3 × polling_interval, 30 s)` of no recorded activity
(`internal/monitoring/health.go:100`). It only helps if a supervisor acts on
it, and nothing shipped probes `/ready` — see
[PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md](PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md).

### 2. Shutdown cancels in-flight submissions instead of draining them

`runStart` (`internal/cli/start.go:190`) calls `cancel()` **before**
`GracefulShutdown` (`:196`). That run context is the one flowing into
`BroadcastTx`, so a reveal mid-broadcast is aborted at the gRPC layer rather
than completed.

`Service.Stop` (`service.go:320-334`) then only flips `isRunning`. It waits for
neither `runSecretMonitoring`, nor the reveal worker pool, nor the event
monitor, so `GracefulShutdown` (`internal/guardian/shutdown.go`) returns
"clean" immediately and the process exits with workers possibly mid-flight.
The 30 s `ShutdownTimeout` is consumed solely by the HTTP servers
(`internal/monitoring/service.go`).

An operator restarting for a binary upgrade just as a window opens can
therefore have a signed reveal cancelled between signing and mempool
acceptance.

### 3. No ceiling on the declared gas limit

`submitLocked` (`signer.go:214-221`) declares `max(simulated, reimbursed)` with
no upper bound. KNOWN_RISKS.md Trade-off §17 (`timeflareio/chain` —
`docs/KNOWN_RISKS.md`) accepts that declared gas exceeds consumed gas and
that the daemon declares the larger simulated figure where a handler outgrows
reimbursement — but §17 reasons from an *honest* simulation. It does not accept
an unbounded figure.

One bad simulation — a hostile or wrong node, an endpoint substituted in the
published network list, a MITM on a connection left plaintext (see
[PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md](PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md)),
or a chain-side regression — is signed as given, because this daemon applies
no bound of its own and the ante handler deducts the declared fee up front.
The chain bounds a single transaction at its block gas limit of 75,000,000,
set at genesis (`timeflareio/chain` — `DONE_CONSENSUS_BLOCK_GAS_LIMIT_PLAN.md`),
so one forged simulation costs at most that much gas at the configured price
rather than the whole balance — still several hundred times the reimbursed
leg, and repeatable on every accept and reveal. A drained signing balance
cannot cover reveal fees, which converts the drain into no-reveal slashes
across every in-flight bond. Measured consumption is ~89.5k and ~95.5k gas against the 120k/130k
declared (`GuardianAcceptGas`, `GuardianRevealGas`).

### 4. The node's chain-id is never asserted

`clientContext` and `submitLocked` bind `WithChainID(s.cfg.ChainID)`
(`signer.go`); nothing verifies it against the node. `Ping` (`client.go:108`)
only fetches the block height, so with a wrong `chain_id` the daemon starts,
queries succeed and health goes green while every signed transaction is
rejected for signature failure.

There is no replay exposure — SIGN_MODE_DIRECT binds chain-id, account number
and sequence, so the signature is simply invalid elsewhere. The risk is silent,
total economic failure: a guardian whose chain-id is wrong after bonds are
locked misses every window and is slashed on all of them.

Selecting a network at `guardianctl config init` removes the typo — `chain_id`
and the endpoints arrive together from one registry entry — but it cannot
verify the node at the other end, and a hand-edited configuration bypasses it
entirely. The default `chain_id` is still the devnet's (`timeflare-test`,
`internal/config/config.go`).

The codebase already defends this exact class elsewhere.
`verifyShareKeyBinding` (`service.go:158-175`) refuses to run against the wrong
share key precisely because "running with the wrong key means missing every
reveal". The chain-id has an identical failure mode and no equivalent guard,
despite `cmtservice.ServiceClient` already being held at `client.go:30`.

### 5. Single endpoint, no failover

One `GRPCEndpoint` and one `RPCEndpoint` (`internal/config/config.go`) carry
all queries, broadcasts and subscriptions. Height-query failure degrades to
`lastKnownHeight` (`service.go:440-453`), which keeps reveals moving only if
broadcast still works — that is, not when the node itself is down or has forked
off. Settlement grants no liveness excuse: per spec.md "Settlement
(Threshold-Independent)" (`timeflareio/chain` — `docs/spec.md`), anything but
an HMAC-verified in-window reveal is a no-reveal.

The network registry publishes each network's endpoints as lists, "so that one
unreachable host is not an unreachable network"; selection keeps the first of
each and discards the rest, because the configuration has nowhere to put them.

### 6. Sequence-mismatch detection is string-matched and single-shot

`isSequenceMismatch` (`signer.go:259`) matches on the substring
`"account sequence mismatch"`, directly under the package's own doctrine that
retryability is decided "by status codes … never by error-string matching"
(`client.go:73-74`; `internal/chain/errors.go`). `TxResponse.Code` (32, the
sdk's `ErrWrongSequence`) is available and sturdier.

Separately, the single retry refetches the *committed* sequence, so it cannot
resolve the case where the mismatch arose because our own previous transaction
is still in the mempool — the refreshed value reproduces the same rejection.
Self-heals on the next poll; costs one attempt cycle.

---

## Design

### Phase 1 — bound the transaction path

Two independent changes, both small:

**Per-submission deadline.** `SubmitTx` derives its own bounded context rather
than inheriting an unbounded one. The deadline must survive run-context
cancellation so that phase 2's drain can complete work already signed:

```go
func (s *Signer) SubmitTx(ctx context.Context, msgs ...sdk.Msg) (string, error) {
    ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.RequestTimeout)
    defer cancel()
    ...
}
```

`context.WithoutCancel` is the load-bearing part: a broadcast that has been
signed should either land or time out, never be abandoned because the process
is shutting down. Cancellation still reaches the *caller*, which is what stops
new work being started.

**Client keepalive.** `Dial` in `internal/chain/transport.go` — the one place
every gRPC connection in this repository is opened, daemon and `guardianctl`
alike — adds `grpc.WithKeepaliveParams`, so a half-open socket is detected in
seconds rather than by TCP retransmission timeout. Values should be
conservative enough not to trip a node's
`GRPC_ARG_KEEPALIVE_PERMIT_WITHOUT_CALLS` policy — see open question 2.

### Phase 2 — drain on shutdown

Three ordered changes:

1. `Service` gains a `sync.WaitGroup` covering `runSecretMonitoring`, the
   reveal worker pool and the event monitor.
2. `Service.Stop` waits on that group, bounded by the caller's context, and
   returns a real error on timeout instead of reporting clean.
3. `runStart` reorders: signal → `Stop` (drain, bounded by `ShutdownTimeout`)
   → `cancel()`. Today's ordering cancels first, which is what aborts the
   broadcast.

The drain must be genuinely bounded: an operator sending SIGTERM twice, or a
drain that exceeds `ShutdownTimeout`, gets an immediate exit. A guardian that
refuses to die is worse than one that drops a submission.

### Phase 3 — gas ceiling

A package constant in `signer.go`, checked after the `max(simulated,
reimbursed)` selection, refusing to sign rather than clamping silently:

```go
const maxDeclaredGas = 10_000_000 // ~75× the largest reimbursed leg (130k)

if adjusted > maxDeclaredGas {
    return "", fmt.Errorf("gas simulation returned %d, above the %d ceiling — refusing to sign "+
        "(check grpc_endpoint: a wrong or hostile node can inflate this)", adjusted, maxDeclaredGas)
}
```

Refusing is correct rather than clamping: a simulation that large means the
node is wrong about something, and signing a clamped figure would produce an
out-of-gas failure that looks like a protocol bug.

### Phase 4 — chain-id assertion

Extend `VerifyRegistration` (`service.go:139-150`) with a node-identity check,
in the same register as the existing share-key refusal:

```go
info, err := c.cmt.GetNodeInfo(ctx, &cmtservice.GetNodeInfoRequest{})
if err == nil && info.DefaultNodeInfo.Network != c.cfg.ChainID {
    return fmt.Errorf("configured chain_id %q but %s reports %q — every signed transaction "+
        "would be rejected and every reveal missed (no-reveal slash on each)",
        c.cfg.ChainID, c.cfg.GRPCEndpoint, info.DefaultNodeInfo.Network)
}
```

A query failure is not treated as a mismatch — an unreachable node is already
handled by `Ping`, and failing closed on a transient query would be a new
startup fragility.

### Phase 5 — sequence detection

Classify by ABCI code rather than substring. The rejection is already wrapped
as `ErrTxRejected` with its code in the message (`submitLocked`); carry the
code in the error so `isSequenceMismatch` can test it directly and the string
match is deleted.

The pending-own-transaction case is deliberately left to self-heal on the next
cycle: tracking mempool state locally would duplicate what the in-flight
registry already does, for one attempt cycle of latency.

### Phase 6 — endpoint failover (scope decision required, see open question 4)

If approved: `grpc_endpoint` and `rpc_endpoint` become lists; the client
rotates to the next endpoint on `ErrUnavailable` after retries are exhausted,
and the event monitor reconnects against the rotated endpoint.
`guardianctl config init` then writes the registry entry's whole endpoint list
instead of its first element, as
[DONE_GUARDIAN_NETWORK_SELECTION_PLAN.md](done/DONE_GUARDIAN_NETWORK_SELECTION_PLAN.md)
("Lists become scalars, for now") anticipates. `grpc_tls` is one setting for
the connection, set from the network's `local` flag, so a list is the
endpoints of one network; phase 4's assertion applies to each endpoint as it is
rotated to.

---

## What this plan does not solve

- **It does not make the guardian resilient to being wrong about the chain.**
  The chain-id assertion catches a misconfiguration at startup; a node that
  serves the right chain-id while lagging or having forked is out of scope and
  belongs to the failover discussion.
- **It does not change the gas economics.** KNOWN_RISKS.md Trade-off §17
  stands unchanged: declared gas still exceeds consumed gas, the excess is
  still spent, and an above-floor `gas_price` is still the operator's own cost.
  The ceiling only bounds the pathological case.
- **It does not prevent a reveal from being dropped by the mempool after a
  successful broadcast.** That is
  [PRIORITY_GUARDIAN_ACCEPT_REVEAL_CORRECTNESS_PLAN.md](PRIORITY_GUARDIAN_ACCEPT_REVEAL_CORRECTNESS_PLAN.md)'s
  in-flight-expiry work.
- **It does not secure the transport.** A bounded, keepalive-checked connection
  to a hostile node is still a connection to a hostile node. TLS for a
  non-local network arrives with network selection; what remains of the
  transport posture is
  [PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md](PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md)'s,
  with this plan's gas ceiling as the defence that holds when the channel is
  authenticated and the node is not honest.

---

## Open questions

1. **Should the submission deadline reuse `request_timeout`, or get its own
   knob?** A broadcast is a different shape of operation from a query — it
   involves a simulation round trip first.
   *Recommendation: reuse `request_timeout` (30 s default) for now.* It is
   already validated positive, already documented, and 30 s is generous for
   three sequential calls. A separate `broadcast_timeout` can follow if testnet
   shows the two want different values; adding a knob nobody has yet needed
   works against the minimalism rule.

2. **What keepalive parameters?** Too aggressive and public RPC providers will
   drop us for policy violations; too slack and the fifteen-minute stall
   remains.
   *Recommendation: `Time: 30s, Timeout: 10s, PermitWithoutStream: false`.*
   That detects a dead peer inside ~40 s while only pinging on active streams,
   which is the setting least likely to trip a provider's policy. Worth
   confirming against the endpoints the network registry publishes for the
   first public network, once it lists one.

3. **Is 10,000,000 the right gas ceiling?** It is ~75× the largest reimbursed
   leg and ~100× measured consumption.
   *Recommendation: yes, as a constant rather than config.* A configurable
   ceiling invites an operator to raise it in response to the very error that
   is protecting them. If a legitimate handler ever approaches it, that is a
   protocol change that should force this constant to be revisited
   deliberately.

4. **Is endpoint failover in scope for testnet, or deferred?** It is the
   largest item here and the only one that grows the config surface
   meaningfully. The done network-selection plan is waiting on the answer: it
   discards all but the first published endpoint until this is settled.
   *Recommendation: defer phase 6 to its own plan, and ship phases 1-5 now.*
   The reasoning: failover's value depends on the testnet topology (whether
   operators run colocated nodes or point at shared infrastructure), which we
   do not yet know. Phases 1-5 are unambiguous wins regardless. If deferred,
   the residual should be stated to operators — "run your own node, or accept
   that its downtime is your slashing exposure" — rather than left implicit.
   Where it is stated is itself unsettled: this repository has no install or
   run guide (the gap
   [DONE_DAEMON_SURFACE_SEPARATION_PLAN.md](done/DONE_DAEMON_SURFACE_SEPARATION_PLAN.md)
   records and [PENDING_RELEASE_STRATEGY_PLAN.md](PENDING_RELEASE_STRATEGY_PLAN.md)
   carries as an open question), and `timeflareio/chain` — `docs/operations.md`
   is the chain's attribute-level API reference, not an operator guide.
   *Recommendation: state it in the guardian operator guide when that exists,
   and until then in the `grpc_endpoint` description `guardianctl config`
   prints.*

5. **Should drain apply to confirmations as well as reveals?** A confirmation
   aborted at shutdown costs an assignment, not a bond.
   *Recommendation: drain both, but let reveals hold the deadline.* The
   WaitGroup covers all workers uniformly, which is simpler than two-tier
   draining, and a confirmation in flight finishes in the same second.

---

## Related plans

- [PRIORITY_GUARDIAN_ACCEPT_REVEAL_CORRECTNESS_PLAN.md](PRIORITY_GUARDIAN_ACCEPT_REVEAL_CORRECTNESS_PLAN.md)
  — the other half of reveal reliability; it owns what the daemon submits and
  when, this plan owns whether the submission survives the network. Its phase 3
  depends on phase 2 here.
- [DONE_GUARDIAN_NETWORK_SELECTION_PLAN.md](done/DONE_GUARDIAN_NETWORK_SELECTION_PLAN.md)
  — depends on phases 3 and 4 as the bound on a substituted endpoint, and on
  the phase 6 ruling to stop discarding published endpoints.
- [PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md](PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md)
  — the gas ceiling in phase 3 is that plan's defence-in-depth companion.
- [PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md](PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md)
  — makes the wedge in issue 1 observable, and wires the `/ready` probe that is
  its only current mitigation.
- [PRIORITY_GUARDIAN_CUSTODY_HARDENING_PLAN.md](PRIORITY_GUARDIAN_CUSTODY_HARDENING_PLAN.md)
  — shares the startup self-check path that phase 4 extends.
