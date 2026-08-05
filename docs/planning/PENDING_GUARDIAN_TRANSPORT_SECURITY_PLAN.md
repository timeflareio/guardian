# Guardian transport security

**Priority**: P1 — a guardian not colocated with its node cannot use TLS at
all, and the plaintext channel carries the gas figure the daemon signs against.
**Status**: refining (1 August 2026)
**Origin**: [PENDING_GUARDIAN_PRE_TESTNET_SWEEP.md](PENDING_GUARDIAN_PRE_TESTNET_SWEEP.md)
finding 6, with the port-collision item from finding 41.
**Components**: `internal/guardian/events.go`; `internal/config/config.go`
(the port-collision check), `config_test.go`; `internal/chain/transport.go` (the
non-loopback warning); `docs/guides/CONTAINERS.md`.

---

## The issue

Every connection the guardian makes to the chain is unconditionally plaintext,
and no configuration can change that.

`blockchain/client.go:41` and `cmd/guardiand/cmd/key.go:437` both hard-code:

```go
grpc.WithTransportCredentials(insecure.NewCredentials())
```

`config.Config` has no TLS field, no CA path and no toggle. The RPC and
WebSocket side (`events.go:65`) has the same property. So an operator whose
node is not on localhost has two options: send everything in clear, or not run
a guardian.

`docs/guides/CONTAINERS.md:183` documents exactly the exposed case —
`-e GUARDIAN_GRPC_ENDPOINT=my-node:9090` — with nothing stating the
run-your-own-local-node assumption that the code silently makes.

### What crosses the channel

- The gas simulation the guardian signs against.
- The guardian record that the startup key-binding self-check compares to.
- The key-epoch history that selects which private key decrypts a share.
- Every outbound signed transaction.

### The load-bearing impact is a fee drain

`submitLocked` takes the gas figure from the wire and applies no ceiling
(`signer.go:216-223`). A forged `SimulateResponse` carrying a large `gas_used`
is multiplied by `gas_adjustment` and signed as the declared limit. No
`max_gas` is configured anywhere in the repo, so the Cosmos genesis default of
`-1` applies and the only bound is the account balance; the ante handler
deducts the declared fee up front. The guardian is then unable to cover reveal
fees, which converts the drain into no-reveal slashes across every in-flight
bond.

### The secondary legs are real but self-limiting

Because the HMAC key derives entirely from public inputs
([CHAIN_MECHANICS.md Trade-off §2](../../CHAIN_MECHANICS.md)), an attacker on the
channel can synthesise an assignment that looks entirely valid — a payload
encrypted to the guardian's on-chain public key with a correctly computed
`share_hmac` — and `processConfirmation` will decrypt it, pass
`validateShareIntegrity` and broadcast an accept. A forged key-history response
likewise steers `EpochKeyResolver.KeyForHeight` at the wrong epoch.

Both are bounded: the real chain rejects the resulting messages, and
`decryptShare` has a trial-decrypt fallback (`reveal.go:337-349`). They are
worth stating because they show the channel is trusted for more than
convenience, but the drain is the finding.

### Adjacent: the port-collision check is wrong for remote nodes

`config.go:246-256` compares the *port* of `grpc_endpoint` against the listener
ports, ignoring the host. So `grpc_endpoint: node.example.com:21100` with
`metrics_port: 21100` is rejected although nothing collides — a false positive
precisely in the remote-node topology this plan exists to enable.

---

## Design

### The TLS configuration surface, which
[PENDING_GUARDIAN_NETWORK_SELECTION_PLAN.md](PENDING_GUARDIAN_NETWORK_SELECTION_PLAN.md) owns

Three keys on `Config`, in the `Network` group — `grpc_tls`, `grpc_tls_ca_file`
and `grpc_tls_insecure_skip_verify` — with credentials built from them by one
helper (`internal/chain/transport.go`, `Dial`) that both `NewClient` and the
`key.go` call site use. The default stays plaintext, so the devnet and every
colocated deployment are unchanged, and selecting a non-local network from the
chain's registry turns `grpc_tls` on so the setting arrives with the deployment
that needs it.

`grpc_tls_insecure_skip_verify` exists because operators do use self-signed
certificates during bring-up, and its absence pushes them back to full plaintext,
which is worse. It warns on every dial that sets it.

That work belongs to the selection plan because selection is what populates the
keys, and offering a network the daemon cannot dial would not have been worth
shipping. What remains below is this plan's.

### Phase 1 — refuse silent plaintext to a remote host

Defaults cannot be trusted to protect an operator who does not know the risk
exists. At startup, when `grpc_tls` is false and `grpc_endpoint` resolves to a
non-loopback host, the daemon warns in the same register as the existing
dashboard warning (`monitoring/service.go:277`):

```
Chain gRPC endpoint <host> is not loopback and TLS is disabled — queries, the
gas figures you sign against, and your signed transactions all cross this link
in clear. Set grpc_tls, or run a colocated node.
```

Whether this should be a refusal rather than a warning is open question 1.

### Phase 2 — the RPC/WebSocket leg

`events.go:65` uses `rpchttp.New(em.cfg.RPCEndpoint, "/websocket")`. An
`https://` endpoint needs a client built with matching TLS settings, so the
same configuration must reach the event monitor. This leg carries no signing
material, but it does carry the block headers that drive reveal timing, so a
manipulated feed can delay a reveal.

### Phase 3 — fix the port-collision check

Compare host and port together, treating an empty or loopback host in
`grpc_endpoint` as colliding with the listeners and any other host as not.

### Phase 4 — documentation

`docs/guides/CONTAINERS.md` gains the TLS keys in its environment table, and
its remote-endpoint example either uses TLS or states plainly that it is a
plaintext link suitable for a trusted network only.

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
  deferred phase 6.
- **It does not verify chain identity.** That is the chain-id assertion, also
  in the transaction-path plan.
- **It does not change the dashboard's exposure.** The unauthenticated operator
  surface is
  [DONE_DASHBOARD_AUTHENTICATION_PLAN.md](../done/DONE_DASHBOARD_AUTHENTICATION_PLAN.md)'s.

---

## Open questions

1. **Should a non-loopback plaintext endpoint be a refusal rather than a
   warning?** The precedent cuts both ways: the daemon refuses to start on a
   wrong share key, but only warns about an unauthenticated dashboard.
   *Recommendation: warn, and revisit before mainnet.* On testnet, operators
   will legitimately point at shared infrastructure over private networks where
   plaintext is a considered choice, and a refusal would push them to
   `insecure_skip_verify` — strictly worse, because it looks secure. The
   distinction from the share-key case is that a wrong key guarantees a slash,
   whereas plaintext is a risk the operator can reasonably accept on a private
   link.

---

## Related plans

- [PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md](PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md)
  — owns the gas ceiling and the chain-id assertion, both of which bound what a
  compromised or wrong node can do. Neither plan is complete protection alone.
- [PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md](PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md)
  — the validation framework the new TLS keys plug into.
- [PENDING_GUARDIAN_DOCS_RESYNC_PLAN.md](PENDING_GUARDIAN_DOCS_RESYNC_PLAN.md)
  — carries the CONTAINERS.md changes if this plan lands after it.
