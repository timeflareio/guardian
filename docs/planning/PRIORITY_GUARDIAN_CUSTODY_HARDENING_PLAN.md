# Guardian custody hardening

**Priority**: P0 — contains the sweep's only Critical finding: a hostile backup
bundle can write arbitrary files as the user that owns every guardian key.
**Status**: refining (1 August 2026)
**Origin**: [PENDING_GUARDIAN_PRE_TESTNET_SWEEP.md](PENDING_GUARDIAN_PRE_TESTNET_SWEEP.md)
findings 1, 19, 21, 36, 40.
**Components**: `guardian/custody/` (`bundle.go`, `keyfile.go`),
`guardian/cmd/guardiand/cmd/` (`key.go`, `rotate_key.go`, `config.go`),
`guardian/blockchain/keys.go`, `guardian/config/config.go`,
`guardian/custody/custody_test.go`, `guardian/custody/rotation_test.go`,
`devnet/guardians.sh`, `devnet/docker/init-guardians.sh`,
`docs/guides/GUARDIAN_KEY_CUSTODY.md`.

---

## The issue

Guardian key custody is otherwise the best-designed area of the daemon — the
envelope format, the epoch keyring and the rotation ordering all hold up under
scrutiny. Four defects sit on top of it, one of them severe.

### 1. Backup-bundle restore escapes the keyring directory (Critical)

`custody/bundle.go:200-217` writes every entry of the bundle's
`KeyringFiles` map to `filepath.Join(keyringDir, rel)`:

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
`guardiand key restore` into code execution and full custody compromise. The
existence guard at `bundle.go:203` blocks overwrites but not creations, and
`--force` (`key.go:251`, a documented flag) disables even that.

The threat model is not exotic. Bundles are designed to be moved between hosts
and stored off-box; "here is your backup, restore it" is the exact social
context the format exists for.

### 2. Rotation cutover has an unrecoverable crash state

`cmd/guardiand/cmd/rotate_key.go:245-257` performs the cutover as two renames:
current key → `.epoch<N>`, then `.next` → current. Everything around it is
right — the bundle is written first, and the on-chain record is confirmed to
have advanced past CheckTx before any local file is touched (`:224-243`). The
residual is the window between the two renames.

A crash there leaves **no file at the configured private-key path**: the old
key is at `.epoch<N>`, the new key at `.next`, and the chain has already
advanced. Neither startup guard catches it — `GetEncryptionPrivateKey()` fails,
so `verifyShareKeyBinding` returns nil at `service.go:161` (an unloadable
current key is deliberately a health signal, not a startup failure), and
`verifyEpochKeyring` hard-fails only on a missing *retired* epoch. The daemon
runs unhealthy, decrypts nothing, and misses every window until a human
notices.

Nothing reclaims `.next`: it appears exactly once in the module, at the line
that creates it. The recovery instructions live only inside error-return paths
(`:249-256`), which a hard crash never reaches.

Two adjacent durability gaps: `SaveEncryptedShareKey` (`custody/keyfile.go:84-91`)
does `WriteFile(tmp) → Rename` with no `f.Sync()` and no directory sync, so the
rename is atomic for visibility but not durability — on a crash before
writeback the renamed file can be zero-length. And a declined rotation leaves
the bundle containing the freshly generated private key on disk (`:173`,
`:180`) with nothing deleting it.

This sits beside, but is not covered by,
[CHAIN_MECHANICS.md Trade-off §14](../../CHAIN_MECHANICS.md) — that accepts a *lost*
key as unrecoverable, not that the rotation ceremony may be what loses it.

### 3. Passphrases are accepted as command-line arguments

`cmd/guardiand/cmd/config.go:308-309` — `config init --keyring-passphrase` and
`--encryption-key-passphrase` take the secrets themselves as argv, consumed
literally at `:428-429`, `:526-527`, `:590-596`. The command's `Example` block
teaches the pattern (`:280-298`), and both devnet scripts use it
(`devnet/guardians.sh:172-173`, `devnet/docker/init-guardians.sh:98-99`).

Both land in shell history, `ps` output and `/proc/<pid>/cmdline`. Together
they are the entire at-rest defence.

This contradicts a rule the project has already written down. The config
*fields* of the same names hold file paths by design (`config/config.go:60`,
`:63`), the container environment variables carry paths rather than secrets,
and
[DONE_DASHBOARD_AUTHENTICATION_PLAN.md](../done/DONE_DASHBOARD_AUTHENTICATION_PLAN.md)
§3 states it outright: "It never accepts the password as an argument, because
arguments land in shell history and `ps`." These flags predate that rule.

There is a second trap in the same place: the flag takes a secret while the
identically named config key takes a path.

### 4. Mnemonic echo and key-material hygiene

`cmd/guardiand/cmd/key.go:276` reads the 24-word recovery phrase through
`promptForInput`, a plain `bufio` read with no echo suppression
(`config.go:113-118`). Every other secret uses `readPasswordInput()`
(`term.ReadPassword`) — including the backup passphrase eighteen lines below at
`key.go:294`. The words *are* the raw key, as `key.go:207-209` warns. Typed
with echo they land in scrollback, `script`/`tmux` capture and screen
recordings.

Alongside it, four cheap hygiene gaps:

- Retired keys are never zeroed. `CollectRetiredKeys` returns raw copies
  (`bundle.go:108`) and neither `runKeyBackup` (`key.go:124-128`) nor
  `runRotateKey` zeroes the map, so every retired epoch key stays resident for
  the process lifetime. The current key is handled correctly in both.
- `SaveEncryptedShareKey`'s `key [32]byte` parameter (`keyfile.go:78`) is a
  by-value copy that is never zeroed.
- `Bundle.CurrentKeyEpoch` is inferred from disk as `max(retired) + 1`
  (`key.go:129-134`). Deleting a settled epoch's file is documented as normal,
  so a guardian at epoch 5 whose epoch-3 and epoch-4 files were deleted records
  `CurrentKeyEpoch: 3`. Inert today — nothing reads it on restore — but wrong
  in a persisted artefact, and the same field is authoritative at
  `rotate_key.go:147`.
- `suppressKeyringTTYPrompts` swaps `os.Stdin` process-wide
  (`blockchain/keys.go:66-75`). The invariant that keeps it safe — every
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

Tests: a table in `custody_test.go` covering `../x`, `a/../../b`, `/etc/x`,
`` (empty), `.`, and — as the negative control — the legitimate nested paths
the cosmos keyring actually produces (`keyhash`, `*.info`, `*.address`).

### Phase 2 — rotation crash recovery and durability

**Reclaim `.next`.** Add a single reconciliation function called from two
places: the head of `rotate-key`, and daemon startup before the key-binding
self-check. Behaviour when the configured key path is absent and `.next`
exists:

- Load `.next` and derive its public key. If it matches the on-chain record's
  current epoch, complete the promotion (rename into place) and log loudly that
  an interrupted rotation was finished.
- If it does not match, refuse to start with the recovery instructions that
  currently live only in the error paths.
- If both the configured path and `.next` exist, the rotation did not reach the
  cutover: delete the orphan `.next` and log it.

**Durability.** `SaveEncryptedShareKey` gains `O_EXCL` on the temporary file
(which also fixes a fixed-name collision between concurrent writers),
`f.Sync()` before close, and a parent-directory sync after the rename.

**Declined rotation.** Delete `backupOutput` when the operator answers no.

Tests: `rotation_test.go` gains a partial-cutover case — stage `.next`, remove
the current key, assert recovery; and the mismatched-`.next` case, asserting
refusal.

### Phase 3 — passphrase input paths

Replace both `config init` passphrase flags with `--keyring-passphrase-file`
and `--encryption-key-passphrase-file`, read through the existing
`custody.ReadPassphraseFile`. Update the `Example` block, `devnet/guardians.sh`
and `devnet/docker/init-guardians.sh` in the same change. Pre-launch breaking
changes are permitted, so the old flags are removed rather than deprecated —
leaving them would leave the footgun loaded.

### Phase 4 — mnemonic echo and hygiene

- `key.go:276` uses `readPasswordInput()` with `strings.TrimSpace`; it already
  handles the non-TTY fallback that scripted restores need.
- `defer custody.Zero(...)` over the retired-key map in `runKeyBackup` and
  `runRotateKey`; zero the by-value parameter in `SaveEncryptedShareKey`.
- Source `Bundle.CurrentKeyEpoch` from the chain record, as `runRotateKey`
  already does at `rotate_key.go:91`.
- Give `readPasswordInput` a guard that detects the substituted stdin pipe and
  returns an error rather than an empty passphrase, so the
  `suppressKeyringTTYPrompts` invariant is enforced by code rather than by
  comment.

### Phase 5 — documentation

Sweep `docs/guides/GUARDIAN_KEY_CUSTODY.md` for the changed flags, and document
the interrupted-rotation recovery behaviour so an operator who sees the
"finished an interrupted rotation" log knows it was expected.

---

## What this plan does not solve

- **It does not make rotation a recovery mechanic.** CHAIN_MECHANICS.md Trade-off §14
  stands: a lost key still means no-reveal slashes on every assignment
  encrypted to it. This plan only stops the ceremony from being the cause.
- **It does not defend against a same-host attacker holding daemon
  privileges.** The custody guide is explicit that this is out of scope, and
  the key-zeroing items are hygiene that narrows a window, not a boundary.
- **It does not add a hardware or remote signer.** The `TxSubmitter` seam
  exists (`blockchain/interface.go`) and the KMS implementation remains
  descoped by the July 2026 ruling.
- **It does not change the bundle format.** Only validation is added, so
  existing bundles remain readable and no version bump is implied.

---

## Open questions

1. **Should `.next` reclamation run at daemon startup, or only in
   `rotate-key`?**
   *Recommendation: both.* `rotate-key` is where an operator retries, but the
   crash state's whole danger is that the daemon runs on happily without a key,
   so startup is where it must be caught. Startup reclamation should be
   conservative — complete the promotion only on a positive public-key match
   against the chain, and refuse otherwise.

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
   able to create files outside it — that is what phase 1 removes.

4. **Should the passphrase-file flags accept `-` for stdin?**
   *Recommendation: yes, as a follow-on rather than in this plan.* It is the
   natural fit for secret managers that pipe rather than write files, but it
   interacts with `suppressKeyringTTYPrompts` and deserves its own thought.

---

## Related plans

- [PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md](PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md)
  — shares `blockchain/keys.go` and the startup self-check path; the chain-id
  assertion it adds belongs in the same `VerifyRegistration` sequence as this
  plan's `.next` reclamation.
- [PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md](PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md)
  — owns the base64-guessing passphrase *file* reader (`keys.go:97-107`), which
  is the sibling defect to this plan's passphrase *flags*.
- [DONE_DASHBOARD_AUTHENTICATION_PLAN.md](../done/DONE_DASHBOARD_AUTHENTICATION_PLAN.md)
  — the source of the "never accept a password as an argument" rule phase 3
  applies.
