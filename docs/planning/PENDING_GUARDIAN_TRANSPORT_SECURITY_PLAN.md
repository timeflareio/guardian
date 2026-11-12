# Guardian transport security

| | |
|---|---|
| **Priority** | P1 |
| **Status** | refining |
| **Created** | 1 August 2026 |
| **Updated** | 11 November 2026 |
| **Protocol surface** | none |
| **Repositories** | guardian, chain |
| **Depends on** | — |
| **Related** | [PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md](PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md); [done/DONE_GUARDIAN_NETWORK_SELECTION_PLAN.md](done/DONE_GUARDIAN_NETWORK_SELECTION_PLAN.md) |
| **Open questions** | 1 (Open questions) |

**Why this priority**: a guardian whose node is not colocated can still be left
on a plaintext link without being told, and that link carries the gas figure
the daemon signs against.
**Origin**: [PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md](PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md)
finding 6, with the port-collision item from finding 41.
**Already landed**: gRPC TLS. Three keys in the `Network` group — `grpc_tls`,
`grpc_tls_ca_file` and `grpc_tls_insecure_skip_verify` — with credentials built
from them by one helper, `chain.Dial` (`internal/chain/transport.go`), which
every gRPC connection in the daemon and the operator tool goes through.
Selecting a non-local network at `config init` turns `grpc_tls` on; skipping
verification warns on every dial. Commit b9d4332,
[done/DONE_GUARDIAN_NETWORK_SELECTION_PLAN.md](done/DONE_GUARDIAN_NETWORK_SELECTION_PLAN.md).
**Components**: `internal/chain/transport.go` (the non-loopback warning);
`internal/guardian/events.go`; `internal/config/config.go` (the port-collision
check), `config_test.go`, `dashboard_config_test.go`; and in
`timeflareio/chain`, `docs/guides/CONTAINERS.md`.

---

## The issue

The gRPC connection can be encrypted, and is when a guardian is set up by
selecting a published non-local network. What remains is everything around that
path: a configuration nobody selected into, the second connection the daemon
makes, a validation check that rejects the remote topology, and a guide that
does not mention any of it.

### 1. A hand-configured remote endpoint is plaintext, silently

`grpc_tls` defaults to false (`internal/config/config.go:160`), which is right
for a colocated node and the devnet. But an operator who types a remote
`grpc_endpoint` — the custom path at `config init`, a later `config set`, or an
environment override — gets `insecure.NewCredentials()`
(`internal/chain/transport.go`, `transportCredentials`) with nothing said. The
only warning the dial emits is for `grpc_tls_insecure_skip_verify`.

`timeflareio/chain` — `docs/guides/CONTAINERS.md:193` documents exactly that
case — `-e GUARDIAN_GRPC_ENDPOINT=my-node:9090`, with no `GUARDIAN_GRPC_TLS` —
and nothing stating that the link is in clear.

### 2. The RPC/WebSocket leg does not see the TLS configuration

`internal/guardian/events.go:65` uses
`rpchttp.New(em.cfg.RPCEndpoint, "/websocket")`. That honours an `https://` or
`wss://` URL and verifies against the system root pool, which is what a
published non-local network needs. It does not read `grpc_tls_ca_file` or
`grpc_tls_insecure_skip_verify`, so a node behind a private CA — the case those
two keys exist for — can be reached over gRPC and not by the event monitor.
The daemon then runs on its polling fallback, logging a reconnect failure on
every attempt.

This leg carries no signing material, but it does carry the block headers that
drive reveal timing, so a manipulated feed can delay a reveal.

### What crosses the gRPC channel

- The gas simulation the guardian signs against.
- The guardian record that the startup key-binding self-check compares to.
- The key-epoch history that selects which private key decrypts a share.
- Every outbound signed transaction.

### The load-bearing impact is a fee drain

`submitLocked` takes the gas figure from the wire and applies no ceiling of its
own (`internal/chain/signer.go:214-219`). A forged `SimulateResponse` carrying
a large `gas_used` is multiplied by `gas_adjustment` and signed as the declared
limit, and the ante handler deducts the declared fee up front. The bounds are
the chain's block gas limit — 75,000,000, set at genesis (`timeflareio/chain` —
`DONE_CONSENSUS_BLOCK_GAS_LIMIT_PLAN.md`) — and the account balance, and the
drain repeats on every accept and reveal. The guardian is then unable to cover
reveal fees, which converts the drain into no-reveal slashes across every
in-flight bond.

### The secondary legs are real but self-limiting

Because the HMAC key derives entirely from public inputs (`timeflareio/chain` —
`docs/KNOWN_RISKS.md`, Trade-off "HMAC keys derive entirely from public
inputs"), an attacker on the channel can synthesise an assignment that looks
entirely valid — a payload encrypted to the guardian's on-chain public key with
a correctly computed `share_hmac` — and `processConfirmation` will decrypt it,
pass `validateShareIntegrity` and broadcast an accept. A forged key-history
response likewise steers `EpochKeyResolver.KeyForHeight` at the wrong epoch.

Both are bounded: the real chain rejects the resulting messages, and
`decryptShare` has a trial-decrypt fallback (`internal/guardian/reveal.go`).
They are worth stating because they show the channel is trusted for more than
convenience, but the drain is the finding.

### 3. The port-collision check is wrong for remote nodes

`internal/config/config.go:293-303` compares the *port* of `grpc_endpoint`
against the listener ports, ignoring the host. So
`grpc_endpoint: node.example.com:21100` with `metrics_port: 21100` is rejected
although nothing collides — a false positive precisely in the remote-node
topology the TLS keys exist to enable.

---

## Design

### Phase 1 — refuse silent plaintext to a remote host

Defaults cannot be trusted to protect an operator who does not know the risk
exists. At startup, when `grpc_tls` is false and `grpc_endpoint` resolves to a
non-loopback host, the daemon warns in the same register as the existing
skipped-verification warning (`internal/chain/transport.go:77`):

```
Chain gRPC endpoint <host> is not loopback and TLS is disabled — queries, the
gas figures you sign against, and your signed transactions all cross this link
in clear. Set grpc_tls, or run a colocated node.
```

Whether this should be a refusal rather than a warning is open question 1.

### Phase 2 — a private CA for the RPC/WebSocket leg

The event monitor's client is built with the same trust settings the gRPC dial
uses: the CA bundle when one is configured, and the skipped-verification
setting with the same warning. A public `https://` endpoint needs nothing — it
already verifies against the system pool.

### Phase 3 — fix the port-collision check

Compare host and port together, treating an empty or loopback host in
`grpc_endpoint` as colliding with the listeners and any other host as not.

### Phase 4 — documentation (chain repository)

`timeflareio/chain` — `docs/guides/CONTAINERS.md` gains the TLS keys, and its
remote-endpoint example either uses TLS or states plainly that it is a
plaintext link suitable for a trusted network only. This is an edit in the
chain repository.

---

## What this plan does not solve

- **It does not authenticate the guardian to the node.** Client certificates
  (mTLS) are not proposed; the threat being addressed is an attacker on the
  path, not an unauthorised guardian.
- **It does not remove trust in the node.** A TLS connection to a hostile node
  is still a connection to a hostile node. The bound on that is the gas ceiling
  in
  [PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md](PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md)
  phase 3, which holds even when TLS is misconfigured — the two are deliberate
  halves of one defence.
- **It does not add endpoint failover.** Multiple endpoints are that plan's
  phase 6.
- **It does not verify chain identity.** That is the chain-id assertion, also
  in the transaction-path plan.
- **It does not change the dashboard's exposure.** The dashboard carries no
  credential today, and is to gain a login again because it will carry
  state-changing actions (owner, 11 November 2026). That login is not yet
  designed; the question lives in
  [PENDING_DASHBOARD_GUARDIAN_ACTIONS_PLAN.md](PENDING_DASHBOARD_GUARDIAN_ACTIONS_PLAN.md).

---

## Open questions

1. **Should a non-loopback plaintext endpoint be a refusal rather than a
   warning?** The precedent cuts both ways: the daemon refuses to start on a
   wrong share key, but only warns when certificate verification is skipped.
   *Recommendation: warn, and revisit before mainnet.* On testnet, operators
   will legitimately point at shared infrastructure over private networks where
   plaintext is a considered choice, and a refusal would push them to
   `grpc_tls_insecure_skip_verify` — strictly worse, because it looks secure.
   The distinction from the share-key case is that a wrong key guarantees a
   slash, whereas plaintext is a risk the operator can reasonably accept on a
   private link.

---

## Related plans

- [PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md](PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md)
  — owns the gas ceiling and the chain-id assertion, both of which bound what a
  compromised or wrong node can do. Neither plan is complete protection alone.
- [PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md](PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md)
  — the validation framework the `grpc_tls*` keys plug into; it leaves the
  port-collision check to phase 3 here.
- [PENDING_GUARDIAN_DOCS_RESYNC_PLAN.md](PENDING_GUARDIAN_DOCS_RESYNC_PLAN.md)
  — corrects the same CONTAINERS.md recipe's healthcheck; the two edits should
  not conflict.
- [PENDING_DASHBOARD_GUARDIAN_ACTIONS_PLAN.md](PENDING_DASHBOARD_GUARDIAN_ACTIONS_PLAN.md)
  — where the dashboard login is designed.
