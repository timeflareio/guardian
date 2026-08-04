# Guardian transaction-path resilience

**Priority**: P0 — every defect here converts an ordinary operational event
(a stalled node, a restart, a copied config) into a missed reveal window, and a
missed window is 40% of the bond burned plus 10% to the creator.
**Status**: refining (1 August 2026)
**Origin**: [PENDING_GUARDIAN_PRE_TESTNET_SWEEP.md](PENDING_GUARDIAN_PRE_TESTNET_SWEEP.md)
findings 3, 15, 20, 22, 24, and the sequence-detection item in 41.
**Components**: `guardian/blockchain/signer.go`, `client.go`, `interface.go`,
`errors.go`; `guardian/guardian/service.go`; `guardian/cmd/guardiand/cmd/start.go`;
`guardian/utils/shutdown.go`; `guardian/config/config.go`;
`guardian/blockchain/*_test.go`, `guardian/guardian/service_test.go`.

---

## The issue

The daemon's read path is defended and its write path is not. `withRetry`
(`client.go:76-106`) wraps every query in `RequestTimeout` with status-code
retry classification; none of the five transaction methods
(`client.go:294-361`) go through it, and nothing else bounds them.

### 1. No deadline and no keepalive on the transaction path

`SubmitTx` (`signer.go:164-175`) takes the per-account mutex and holds it
across three unbounded network calls: account/sequence retrieval (`:181`), gas
simulation (`:216`) and broadcast (`:239`). The context it receives is the
daemon's **root** run context:

```
service.go:437 processSecrets(ctx)
  → processReveals → reveal.go:194 GuardianRevealShare(ctx, …)
  → client.go:354-361 → signer.SubmitTx(ctx, msg)
```

The connection is dialled with defaults and no keepalive (`client.go:41`), so a
half-open socket — node freeze, NAT drop with no RST — blocks a broadcast for
the OS TCP retransmission timeout, on the order of fifteen minutes.

The failure cascades rather than staying local. The blocked worker holds
`s.mu`; every other reveal worker blocks on it; `processReveals`'s `wg.Wait()`
(`service.go:561`) blocks its caller — including `onNewHeight`, which runs
*synchronously* inside the event monitor's select loop (`events.go:98`), so
block-header processing stops as well. Every reveal window open during that
period is a no-reveal slash.

The one existing mitigation is indirect: `/ready` trips after
`max(3 × polling_interval, 30 s)` of no recorded activity
(`monitoring/health.go:100`). It only helps if a supervisor acts on it, and
nothing shipped probes `/ready` — see
[PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md](PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md).

### 2. Shutdown cancels in-flight submissions instead of draining them

`cmd/guardiand/cmd/start.go:184-190` calls `cancel()` **before**
`GracefulShutdown`. That run context is the one flowing into `BroadcastTx`, so
a reveal mid-broadcast is aborted at the gRPC layer rather than completed.

`Service.Stop` (`service.go:320-334`) then only flips `isRunning`. It waits for
neither `runSecretMonitoring`, nor the reveal worker pool, nor the event
monitor, so `GracefulShutdown` returns "clean" immediately and the process
exits with workers possibly mid-flight. The 30 s `ShutdownTimeout` is consumed
solely by the HTTP servers (`monitoring/service.go:306-312`).

An operator restarting for a binary upgrade just as a window opens can
therefore have a signed reveal cancelled between signing and mempool
acceptance.

### 3. No ceiling on the declared gas limit

`signer.go:216-223` declares `max(simulated, reimbursed)` with no upper bound.
[CHAIN_MECHANICS.md Trade-off §17](../../CHAIN_MECHANICS.md) accepts that declared gas
exceeds consumed gas and that the daemon declares the larger simulated figure
where a handler outgrows reimbursement — but §17 reasons from an *honest*
simulation. It does not accept an unbounded figure.

One bad simulation — a hostile or wrong node, a MITM (see
[PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md](PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md)),
or a chain-side regression — costs the entire signing balance in a single
transaction, because the ante handler deducts the declared fee up front and no
`max_gas` is configured anywhere in the repo. The guardian is then unable to
cover reveal fees, converting the drain into no-reveal slashes across every
in-flight bond. Measured consumption is ~89.5k and ~95.5k gas against the
120k/130k declared (`signer.go:36`).

### 4. The node's chain-id is never asserted

`signer.go:150` and `:189` bind `WithChainID(s.cfg.ChainID)`; nothing verifies
it against the node. `Ping` (`client.go:109-112`) only fetches the block
height, so with a wrong `chain_id` the daemon starts, queries succeed and
health goes green while every signed transaction is rejected for signature
failure.

There is no replay exposure — SIGN_MODE_DIRECT binds chain-id, account number
and sequence, so the signature is simply invalid elsewhere. The risk is silent,
total economic failure: a guardian whose chain-id is wrong after bonds are
locked misses every window and is slashed on all of them.

The codebase already defends this exact class elsewhere.
`verifyShareKeyBinding` (`service.go:158-175`) refuses to run against the wrong
share key precisely because "running with the wrong key means missing every
reveal". The chain-id has an identical failure mode and no equivalent guard,
despite `cmtservice.ServiceClient` already being held at `client.go:31`.

### 5. Single endpoint, no failover

One `GRPCEndpoint` and one `RPCEndpoint` (`config/config.go:133-134`) carry all
queries, broadcasts and subscriptions. Height-query failure degrades to
`lastKnownHeight` (`service.go:440-453`), which keeps reveals moving only if
broadcast still works — that is, not when the node itself is down or has forked
off. Settlement grants no liveness excuse: per spec.md "Settlement
(Threshold-Independent)", anything but an HMAC-verified in-window reveal is a
no-reveal.

### 6. Sequence-mismatch detection is string-matched and single-shot

`isSequenceMismatch` (`signer.go:261-263`) matches on the substring
`"account sequence mismatch"`, directly under the file's own doctrine that
retryability is decided "by status codes, never by error-string matching"
(`errors.go:28-30`). `TxResponse.Code` (32, the sdk's `ErrWrongSequence`) is
available and sturdier.

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

**Client keepalive.** Dial with `grpc.WithKeepaliveParams` so a half-open
socket is detected in seconds rather than by TCP retransmission timeout.
Values should be conservative enough not to trip a node's
`GRPC_ARG_KEEPALIVE_PERMIT_WITHOUT_CALLS` policy — see open question 2.

### Phase 2 — drain on shutdown

Three ordered changes:

1. `Service` gains a `sync.WaitGroup` covering `runSecretMonitoring`, the
   reveal worker pool and the event monitor.
2. `Service.Stop` waits on that group, bounded by the caller's context, and
   returns a real error on timeout instead of reporting clean.
3. `start.go` reorders: signal → `Stop` (drain, bounded by `ShutdownTimeout`)
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

Classify by ABCI code rather than substring. `ErrTxRejected` already carries
the code (`signer.go:247`); wrap it so `isSequenceMismatch` can test the code
directly and the string match is deleted.

The pending-own-transaction case is deliberately left to self-heal on the next
cycle: tracking mempool state locally would duplicate what the in-flight
registry already does, for one attempt cycle of latency.

### Phase 6 — endpoint failover (scope decision required, see open question 4)

If approved: `grpc_endpoint` and `rpc_endpoint` become comma-separated lists;
the client rotates to the next endpoint on `ErrUnavailable` after retries are
exhausted, and the event monitor reconnects against the rotated endpoint.

---

## What this plan does not solve

- **It does not make the guardian resilient to being wrong about the chain.**
  The chain-id assertion catches a misconfiguration at startup; a node that
  serves the right chain-id while lagging or having forked is out of scope and
  belongs to the failover discussion.
- **It does not change the gas economics.** CHAIN_MECHANICS.md Trade-off §17 stands
  unchanged: declared gas still exceeds consumed gas, the excess is still
  spent, and an above-floor `gas_price` is still the operator's own cost. The
  ceiling only bounds the pathological case.
- **It does not prevent a reveal from being dropped by the mempool after a
  successful broadcast.** That is
  [PRIORITY_GUARDIAN_ACCEPT_REVEAL_CORRECTNESS_PLAN.md](PRIORITY_GUARDIAN_ACCEPT_REVEAL_CORRECTNESS_PLAN.md)'s
  in-flight-expiry work.
- **It does not secure the transport.** A bounded, keepalive-checked connection
  to a hostile node is still a connection to a hostile node —
  [PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md](PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md)
  owns that, with this plan's gas ceiling as the defence that holds if TLS is
  misconfigured.

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
   confirming against whatever public endpoints the testnet will offer.

3. **Is 10,000,000 the right gas ceiling?** It is ~75× the largest reimbursed
   leg and ~100× measured consumption.
   *Recommendation: yes, as a constant rather than config.* A configurable
   ceiling invites an operator to raise it in response to the very error that
   is protecting them. If a legitimate handler ever approaches it, that is a
   protocol change that should force this constant to be revisited
   deliberately.

4. **Is endpoint failover in scope for testnet, or deferred?** It is the
   largest item here and the only one that grows the config surface
   meaningfully.
   *Recommendation: defer phase 6 to its own plan, and ship phases 1-5 now.*
   The reasoning: failover's value depends on the testnet topology (whether
   operators run colocated nodes or point at shared infrastructure), which we
   do not yet know. Phases 1-5 are unambiguous wins regardless. If deferred,
   the residual should be stated in `docs/operations.md` — "run your own node,
   or accept that its downtime is your slashing exposure" — rather than left
   implicit.

5. **Should drain apply to confirmations as well as reveals?** A confirmation
   aborted at shutdown costs an assignment, not a bond.
   *Recommendation: drain both, but let reveals hold the deadline.* The
   WaitGroup covers all workers uniformly, which is simpler than two-tier
   draining, and a confirmation in flight finishes in the same second.

---

## Related plans

- [PRIORITY_GUARDIAN_ACCEPT_REVEAL_CORRECTNESS_PLAN.md](PRIORITY_GUARDIAN_ACCEPT_REVEAL_CORRECTNESS_PLAN.md)
  — the other half of reveal reliability; it owns what the daemon submits and
  when, this plan owns whether the submission survives the network.
- [PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md](PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md)
  — the gas ceiling in phase 3 is that plan's defence-in-depth companion.
- [PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md](PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md)
  — makes the wedge in issue 1 observable, and wires the `/ready` probe that is
  its only current mitigation.
