# Guardian custody hardening

| | |
|---|---|
| **Priority** | P0 |
| **Status** | refining |
| **Created** | 1 August 2026 |
| **Updated** | 11 November 2026 |
| **Protocol surface** | none |
| **Repositories** | guardian, chain |
| **Depends on** | — |
| **Related** | [PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md](PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md); [PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md](PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md); [DONE_DAEMON_SURFACE_SEPARATION_PLAN.md](done/DONE_DAEMON_SURFACE_SEPARATION_PLAN.md) |
| **Open questions** | 5 |

**Why this priority**: contains the sweep's only Critical finding: a hostile backup
bundle can write arbitrary files as the user that owns every guardian key.
**Origin**: [PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md](PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md)
findings 1, 19, 21, 36, 40; the rotation design in phase 2 is the owner's ruling
of 11 November 2026.
**Components**: `internal/custody/` (`bundle.go`, `keyfile.go`,
`custody_test.go`, `rotation_test.go`); `internal/cli/` (`key.go`,
`rotate_key.go`, `rotate_key_test.go`, `config_init.go`, `ui/ui.go`);
`internal/chain/keys.go`; `internal/guardian/service.go` (the startup
key-binding message only); `docs/guides/GUARDIAN_KEY_CUSTODY.md`;
`timeflareio/chain` — `devnet/guardians.sh`, `devnet/docker/init-guardians.sh`.

Every command named here is a `guardianctl` verb. `guardiand` runs the service
and, by the rule the two-binary split exists for, carries no code that can mint,
export or rewrite key material (`CLAUDE.md`; `make verify-daemon-symbols`).

---

## The issue

Guardian key custody is otherwise the best-designed area of the daemon — the
envelope format, the epoch keyring and the rotation ordering all hold up under
scrutiny. Four defects sit on top of it, one of them severe.

### 1. Backup-bundle restore escapes the keyring directory (Critical)

`RestoreKeyringFiles` (`internal/custody/bundle.go:200-217`) writes every entry
of the bundle's `KeyringFiles` map to `filepath.Join(keyringDir, rel)`:

```go
for rel, content := range files {
    target := filepath.Join(keyringDir, rel)
    if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil { ... }
    if err := os.WriteFile(target, content, 0600); err != nil { ... }
}
```

`rel` comes from inside the bundle (`bundle.go:45`) and is therefore
attacker-controlled. `filepath.Join` cleans a path but does not contain it, and
`Bundle.Validate()` (`bundle.go:58-85`) inspects versions and key lengths but
never the map keys. Verified:

```
filepath.Join("/home/g/.timeflare/guardian", "../../.ssh/authorized_keys")
  → /home/g/.ssh/authorized_keys
```

The write lands as the guardian user — the user that owns `private_key`, every
`private_key.epoch<N>`, both passphrase files and the signing keyring. So a
bundle offered to an operator as a recovered backup converts one
`guardianctl key restore` into code execution and full custody compromise. The
existence guard at `bundle.go:203` blocks overwrites but not creations, and
`--force` (`internal/cli/key.go:253`, a documented flag) disables even that.

The threat model is not exotic. Bundles are designed to be moved between hosts
and stored off-box; "here is your backup, restore it" is the exact social
context the format exists for.

### 2. Rotation cutover leaves the configured key path empty mid-ceremony

`cutoverEpochKeys` (`internal/cli/rotate_key.go:29-42`) installs the new key
with two renames: current key → `.epoch<N>`, then `.next` → current. Everything
around it is right — the bundle is written first, the new key is staged at
`.next` before anything is broadcast, and the on-chain record is confirmed to
have advanced past CheckTx before any local file is touched. The residual is
the window between the two renames.

A crash there leaves **no file at the configured private-key path**: the old
key is at `.epoch<N>`, the new key at `.next`, and the chain has already
advanced. Neither startup guard catches it — `GetEncryptionPrivateKey()` fails,
so `verifyShareKeyBinding` returns nil (`internal/guardian/service.go:161`: an
unloadable current key is deliberately a health signal, not a startup failure),
and `verifyEpochKeyring` hard-fails only on a missing *retired* epoch. The
daemon runs unhealthy, decrypts nothing, and misses every window until a human
notices.

Nothing reclaims `.next` either. `guardianctl rotate-key` opens by requiring the
key at the configured path to load and to match the registered record, so in
the crash state the one command an operator would retry refuses to run, and the
recovery instructions live only inside `cutoverEpochKeys`' error returns, which
a hard crash never reaches.

Three adjacent gaps in the same ceremony:

- **Durability.** `SaveEncryptedShareKey` (`internal/custody/keyfile.go`) does
  `WriteFile(tmp) → Rename` with no `f.Sync()` and no directory sync, so the
  rename is atomic for visibility but not durability — on a crash before
  writeback the renamed file can be zero-length. The temporary name is fixed
  (`path + ".tmp"`), so two concurrent writers collide.
- **The confirmation timeout deletes the staged key.** When the guardian record
  has not advanced within the 90-second poll, `runRotateKey` removes `.next` and
  reports local keys unchanged. The transaction passed CheckTx and may still be
  included after the poll gives up, at which point the chain holds a key whose
  only copy is the backup bundle.
- **A declined rotation leaves the bundle on disk.** Answering no at the
  submission prompt prints that the backup bundle "can be deleted" and leaves
  it there — a file containing the freshly generated private key, for a key
  that was never registered.

This sits beside, but is not covered by, CHAIN_MECHANICS.md Trade-off §14
(`timeflareio/chain` — `docs/CHAIN_MECHANICS.md`) — that accepts a *lost* key as
unrecoverable, not that the rotation ceremony may be what loses it.

### 3. Passphrases are accepted as command-line arguments

`guardianctl config init --keyring-passphrase` and `--encryption-key-passphrase`
(`internal/cli/config_init.go:89-90`) take the secrets themselves as argv. The
command's `Example` block teaches the pattern (`config_init.go:62-77`), and both
devnet scripts use it (`timeflareio/chain` — `devnet/guardians.sh:219-220`,
`devnet/docker/init-guardians.sh:100-101`).

Both land in shell history, `ps` output and `/proc/<pid>/cmdline`. Together
they are the entire at-rest defence.

This contradicts the configuration's own design. The config *fields* of the
same names hold file paths (`internal/config/config.go`,
`encryption_key_passphrase` and `keyring_passphrase`), the container
environment variables carry paths rather than secrets, and every other
passphrase the tool takes non-interactively arrives as a file
(`--backup-passphrase-file` on `rotate-key`, `--passphrase-file` on
`config migrate-key`).

There is a second trap in the same place: the flag takes a secret while the
identically named config key takes a path.

### 4. Mnemonic echo and key-material hygiene

`guardianctl key restore --from-mnemonic` reads the 24-word recovery phrase
through `PromptInput` (`internal/cli/key.go:278`; `internal/cli/ui/ui.go:156`),
a plain line read with no echo suppression. Every other secret uses
`ReadPassword` (`ui.go:178`) — including the backup passphrase eighteen lines
below at `key.go:296`. The words *are* the raw key, as the command's own
`--show-mnemonic` warning says. Typed with echo they land in scrollback,
`script`/`tmux` capture and screen recordings.

Alongside it, four cheap hygiene gaps:

- Retired keys are never zeroed. `CollectRetiredKeys`
  (`internal/custody/bundle.go`) returns raw copies and neither `runKeyBackup`
  (`key.go:126`) nor `runRotateKey` zeroes the map, so every retired epoch key
  stays resident for the process lifetime. The current key is handled correctly
  in both.
- `SaveEncryptedShareKey`'s `key [32]byte` parameter is a by-value copy that is
  never zeroed.
- `Bundle.CurrentKeyEpoch` is inferred from disk as `max(retired) + 1`
  (`key.go:131-136`). Deleting a settled epoch's file is documented as normal,
  so a guardian at epoch 5 whose epoch-3 and epoch-4 files were deleted records
  `CurrentKeyEpoch: 3`. Inert today — nothing reads it on restore — but wrong
  in a persisted artefact. `runRotateKey` takes the same value from the chain
  record instead.
- `suppressKeyringTTYPrompts` swaps `os.Stdin` process-wide
  (`internal/chain/keys.go:66`). The invariant that keeps it safe — every
  interactive prompt happens before the first keyring is constructed — holds
  across all current commands but is enforced only by a comment.

---

## Design

### Phase 1 — bundle path containment

Reject unsafe keys before any write, and reject them in `Bundle.Validate()` so
a malformed bundle fails at `OpenBundle` rather than part-way through
restoring:

```go
func validateKeyringPath(rel string) error {
    if rel == "" || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
        return fmt.Errorf("bundle keyring path %q must be relative", rel)
    }
    clean := filepath.Clean(rel)
    if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
        return fmt.Errorf("bundle keyring path %q escapes the keyring directory", rel)
    }
    return nil
}
```

`RestoreKeyringFiles` re-checks containment on the joined result as a
belt-and-braces second gate, because the two checks fail differently
(`Validate` protects the whole bundle up front; the join check protects against
a future caller that bypasses `Validate`).

Tests: a table in `internal/custody/custody_test.go` covering `../x`,
`a/../../b`, `/etc/x`, `` (empty), `.`, and — as the negative control — the
legitimate nested paths the cosmos keyring actually produces (`keyhash`,
`*.info`, `*.address`).

### Phase 2 — rotation installs the new key by a single atomic swap

**Ruling (owner, 11 November 2026)**: key rotation must not be a two-rename
cutover. The new key is created first, and it is then installed by a single
atomic swap, so the configured private-key path holds a usable key at every
instant.

**The sequence.** Write `K` for the configured private-key path and `N` for the
epoch being retired. `cutoverEpochKeys` is replaced by steps 4–5; steps 1–3 are
the existing order, made durable.

| Step | Action | `K` holds | Other files |
|---|---|---|---|
| 1 | Seal the rotation backup bundle (unchanged) | old key | bundle |
| 2 | Write the new key to `K.next` and make it durable: temporary file opened `O_EXCL`, `f.Sync()`, rename to `K.next`, sync the directory | old key | `K.next` = new key |
| 3 | Broadcast `MsgGuardianRotateKey`; poll until the guardian record shows epoch `N+1` with the new public key (unchanged) | old key | `K.next` |
| 4 | Put the retired copy in place while the old key is still at `K`: hard-link `K` to `K.epoch<N>`, falling back to copy-and-`fsync` where the filesystem refuses the link; sync the directory | old key | `K.epoch<N>` = old key, `K.next` |
| 5 | **One** `rename(K.next, K)`; sync the directory | new key | `K.epoch<N>` = old key |
| 6 | Refresh the informational public-key file and `encryption_public_key` in the configuration (unchanged) | new key | — |

`K` is never absent: step 5 replaces it in a single rename, and nothing before
step 5 moves it. The hard link in step 4 is safe because key files are only
ever replaced by rename, never rewritten in place, so `K.epoch<N>` keeps the
old key's contents after step 5 gives `K` a new one. A `K.epoch<N>` that already
exists at step 4 is accepted only if it is byte-identical to `K`; anything else
is refused rather than overwritten.

One consequence for step 3: when the poll times out, `K.next` is **kept**. The
transaction passed CheckTx and may still be included, and deleting the staged
key then would leave the chain holding a key whose only copy is the bundle. The
reconciliation below removes it once the chain shows the rotation did not land.

**What an interrupted rotation looks like.** Each row is the state a crash
leaves after the step named, and what tells them apart is always the same two
comparisons — which key `K` derives and which key `K.next` derives, against the
guardian record's current public key.

| Interrupted after | On disk | Chain | Effect on a running or restarted daemon | Detected by |
|---|---|---|---|---|
| step 1 | `K` old; a bundle for an unregistered key | epoch `N` | none | nothing to detect; the bundle is the operator's to delete |
| step 2, or step 3 where the transaction never lands | `K` old; `K.next` new | epoch `N` | none — `K` matches the record | `K.next` exists and `K` derives the registered key: an orphan |
| step 3 where the transaction lands (including after a poll timeout) | `K` old; `K.next` new | epoch `N+1` | `K` no longer derives the registered key; startup refuses (`verifyShareKeyBinding`) | `K.next` derives the registered key and `K` does not |
| step 4 | as above, plus `K.epoch<N>` identical to `K` | epoch `N+1` | as above | as above; the existing `K.epoch<N>` matches `K` |
| step 5 | `K` new; `K.epoch<N>` old | epoch `N+1` | correct | `encryption_public_key` in the configuration differs from the key at `K` (`guardianctl key backup` already refuses on that mismatch) |

In every row `K` holds a loadable key, and in every row the old key is still on
disk under `K` or `K.epoch<N>`, so in-flight assignments encrypted to epoch `N`
remain decryptable throughout.

**Completing or cleaning up belongs to `guardianctl rotate-key` alone.**
`guardiand` rewrites no key material; it promotes nothing and deletes nothing.
A single reconciliation runs at the head of `guardianctl rotate-key`, before the
existing "current key must match the registered record" check, which would
otherwise refuse in exactly the state that needs finishing:

- **`K.next` absent** — nothing was interrupted; proceed.
- **`K.next` derives the registered key and `K` does not** — the rotation landed
  and the swap did not. Run steps 4–6, report that an interrupted rotation was
  finished, and stop; a fresh rotation is a separate invocation (the chain's
  minimum interval would refuse one anyway).
- **`K` derives the registered key and `K.next` does not** — the rotation never
  landed. Delete the orphan `K.next`, say so, and continue.
- **Neither derives the registered key, or `K.next` does not load** — refuse,
  naming both files and the bundle as the recovery route
  (`guardianctl key restore`).
- **`K` derives the registered key and `encryption_public_key` disagrees** —
  step 6 was interrupted; refresh both and continue.

What `guardiand` does at startup in the third and fourth rows of the table is
open question 1. Today it refuses to start, with a message that names
`'guardiand key restore'` — a verb that binary does not have, and not the
remedy for this state.

**Durability.** `SaveEncryptedShareKey` gains `O_EXCL` on the temporary file
(which also removes the fixed-name collision between concurrent writers),
`f.Sync()` before close, and a parent-directory sync after the rename. Every
writer of a share key goes through it, so `config init`, `config migrate-key`
and `key restore` inherit the same guarantee.

**Declined rotation.** Delete `backupOutput` when the operator answers no. The
key inside it was never registered, so nothing can depend on it.

Tests: `internal/cli/rotate_key_test.go` replaces its two-rename cases with one
per row of the interruption table — construct the on-disk state, run the
reconciliation, assert the outcome and that `K` loads at every point — plus the
refusal cases (neither file matches; an existing `K.epoch<N>` that differs from
`K`). `internal/custody/rotation_test.go` gains the `O_EXCL` collision case.

### Phase 3 — passphrase input paths

Replace both `config init` passphrase flags with `--keyring-passphrase-file`
and `--encryption-key-passphrase-file`, read through the existing
`custody.ReadPassphraseFile`, and update the `Example` block. Pre-launch
breaking changes are permitted, so the old flags are removed rather than
deprecated — leaving them would leave the footgun loaded.

**This is a release-ordered change across two repositories.** The devnet
scripts live in `timeflareio/chain` and drive a *released* `guardianctl` at the
version pinned in `devnet/versions.env` (`GUARDIAN_VERSION`), never a sibling
checkout. So the order is: release a guardian version carrying the new flags;
then, in the chain repository, move `GUARDIAN_VERSION` and switch
`devnet/guardians.sh` and `devnet/docker/init-guardians.sh` to the file flags in
the same change. Until that chain change lands the devnet keeps working against
the older pin; a chain change made before the guardian release would break it.

### Phase 4 — mnemonic echo and hygiene

- `key restore --from-mnemonic` reads the phrase through `ReadPassword` with
  `strings.TrimSpace`; it already handles the non-TTY fallback that scripted
  restores need.
- `defer custody.Zero(...)` over the retired-key map in `runKeyBackup` and
  `runRotateKey`; zero the by-value parameter in `SaveEncryptedShareKey`.
- Source `Bundle.CurrentKeyEpoch` in `runKeyBackup` from the chain record, as
  `runRotateKey` already does.
- Give `ReadPassword` a guard that detects the substituted stdin pipe and
  returns an error rather than an empty passphrase, so the
  `suppressKeyringTTYPrompts` invariant is enforced by code rather than by
  comment.

### Phase 5 — documentation

Sweep `docs/guides/GUARDIAN_KEY_CUSTODY.md` for the changed flags and for the
verbs it still attributes to `guardiand` (`rotate-key`, `key backup`,
`key restore` are `guardianctl`), and document interrupted-rotation recovery:
re-run `guardianctl rotate-key`, which finishes or cleans up before doing
anything else.

---

## What this plan does not solve

- **It does not make rotation a recovery mechanic.** CHAIN_MECHANICS.md
  Trade-off §14 stands: a lost key still means no-reveal slashes on every
  assignment encrypted to it. This plan only stops the ceremony from being the
  cause.
- **It does not defend against a same-host attacker holding daemon
  privileges.** The custody guide is explicit that this is out of scope, and
  the key-zeroing items are hygiene that narrows a window, not a boundary.
- **It does not add a hardware or remote signer.** The `TxSubmitter` seam
  exists (`internal/chain/interface.go`) and the KMS implementation remains
  descoped by the July 2026 ruling.
- **It does not change the bundle format.** Only validation is added, so
  existing bundles remain readable and no version bump is implied. What a
  bundle *contains* is open question 4.
- **It does not make the daemon pick up a rotation without a restart any
  differently.** How a running `guardiand` notices a new epoch is unchanged.

---

## Open questions

1. **What does `guardiand` do at startup when the key at the configured path
   does not derive the registered key, and a staged `.next` is present?** Under
   phase 2 this is the signature of a rotation that landed on chain and was
   interrupted before the swap. Today `verifyShareKeyBinding` refuses to start
   on any mismatch. Refusing is safe against running with a wrong key, but in
   this particular state the key on disk is the correct one for every in-flight
   assignment of the retiring epoch, and a daemon that will not start reveals
   none of them. The alternatives are to keep refusing but recognise the state
   and name `guardianctl rotate-key` as the remedy, or to start in a degraded
   mode that serves the retiring epoch and reports unhealthy.
   *Recommendation: keep refusing, and make the refusal specific — detect the
   staged file read-only and name `guardianctl rotate-key`.* The operator who
   interrupted a rotation is at the keyboard, the remedy is one command, and a
   daemon that starts against a key the chain no longer names is the failure the
   check exists to prevent. Either way the message stops naming a
   `guardiand key restore` verb that does not exist.

2. **Should a refused restore be recoverable, or fatal?** A bundle failing the
   new path validation is either corrupt or hostile.
   *Recommendation: fatal, with the offending key named in the error.* An
   operator holding a genuinely corrupt bundle needs to know which entry is
   wrong; one that has been handed a hostile bundle needs the restore to stop.

3. **Do we keep `--force` on `key restore` at all?** It currently disables the
   only overwrite protection.
   *Recommendation: keep it, but scope it.* With path containment in place,
   `--force` means "overwrite files inside my keyring directory", which is a
   legitimate operator need during disaster recovery. It should no longer be
   able to create files outside it — that is what phase 1 removes. The answer
   depends on question 4.

4. **What should a backup bundle contain?**
   [DONE_DAEMON_SURFACE_SEPARATION_PLAN.md](done/DONE_DAEMON_SURFACE_SEPARATION_PLAN.md)
   ("What this plan does not solve") recorded that `CollectKeyringFiles` sweeps
   the whole of `keyring_dir` into the bundle, and that `keyring_dir` defaults
   to the guardian's data directory — so a bundle carries `config.yaml`, the
   at-rest passphrase file, the keyring passphrase file and anything else left
   there, including a backup passphrase stored beside the bundle. Two
   consequences bear on this plan: `key restore` effectively always needs
   `--force`, because the configuration file it requires in order to run is
   itself in the bundle, which makes question 3's "scoped `--force`" the normal
   path rather than the exceptional one; and phase 1's negative-control list is
   narrower than what real bundles hold. That plan said bundle composition
   "wants its own plan"; none exists.
   *Recommendation: settle it here, before phase 1's tests are written —
   collect only the keyring's own files and exclude the configuration and
   passphrase files — rather than open a further plan for one function.*

5. **Should the passphrase-file flags accept `-` for stdin?**
   *Recommendation: yes, as a follow-on rather than in this plan.* It is the
   natural fit for secret managers that pipe rather than write files, but it
   interacts with `suppressKeyringTTYPrompts` and deserves its own thought.

---

## Related plans

- [PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md](PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md)
  — shares `internal/chain/keys.go` and the startup self-check path; the
  chain-id assertion it adds sits in the same `VerifyRegistration` sequence as
  the key-binding check open question 1 concerns.
- [PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md](PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md)
  — owns configuration validation. The passphrase *file* reader it once listed
  beside this plan's passphrase *flags* already reads its file verbatim
  (`custody.ReadPassphraseFile`, `internal/custody/keyfile.go`), so nothing in
  phase 3 waits on it.
- [DONE_DAEMON_SURFACE_SEPARATION_PLAN.md](done/DONE_DAEMON_SURFACE_SEPARATION_PLAN.md)
  — the two-binary split whose rule phase 2 observes, and the source of open
  question 4.
