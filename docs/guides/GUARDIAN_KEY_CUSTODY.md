# Guardian Key Custody — Operator Runbook

Implements Phases 1–3 of
[docs/planning/done/DONE_GUARDIAN_KEY_CUSTODY_PLAN.md](../planning/done/DONE_GUARDIAN_KEY_CUSTODY_PLAN.md)
plus forward-only key rotation (see [Key rotation](#key-rotation--the-hygiene-path)).
Read this before running a guardian with real value at stake.

## The two keys, and what losing them costs

A guardian's economic life depends on two keys:

1. **The X25519 share-encryption keyring**: the current epoch's private key
   (`private_key`) plus any retired epochs still serving in-flight
   assignments (`private_key.epoch<N>`). Each epoch's binding is
   **immutable** — the key an assignment was encrypted to can never change;
   `guardiand rotate-key` only advances the epoch for *future* assignments.
   The consequences of custody failure are asymmetric and severe:
   - **Loss of an epoch's key** ⇒ you can never again decrypt the shares
     encrypted to it ⇒ you miss every reveal on every in-flight secret under
     that epoch ⇒ you are **no-reveal slashed on all of them** (40% of each
     bond burned, 10% to the creator, 50% returned). Rotation is **not**
     recovery — nothing protocol-side can rescue in-flight secrets after key
     loss; only your backups can.
   - **Leak of an epoch's key** ⇒ every share ever assigned to you *under
     that epoch* is decryptable by the holder. Combined with the public
     on-chain `encrypted_share` records, a leaked key exposes shares for
     still-locked secrets — enabling early-reveal evidence against you (full
     bond slash) or silent threshold erosion. Rotation bounds this blast
     radius to one epoch: rotate on schedule, and a future leak can never
     expose your address's lifetime history.
2. **The Cosmos signing key** in the keyring. Needed to sign confirmations
   and reveals; replaceable in principle (it is your account key), but losing
   it mid-flight still means missed reveals until restored.

## Custody model

### Share key: encrypted at rest, by default

`guardiand config init` and `config create-encryption-key` always write the
private key as a **versioned encrypted envelope** (argon2id-derived key,
ChaCha20-Poly1305, parameters authenticated in the header). There is no
plaintext generation path, so the unsafe configuration cannot accidentally
ship. Legacy raw 32-byte files still load; upgrade them in place with:

```sh
guardiand config migrate-key            # interactive
guardiand config migrate-key --passphrase-file /secure/kek --accept   # fleet
```

**Passphrase resolution** (the daemon must decrypt unattended):

1. The file at `encryption_key_passphrase` in the config (a **file path** by
   design — the secret itself never sits in a config value or an env value);
2. failing that, the conventional sibling file `encryption_key_passphrase`
   in the same directory as the private key (this is how containers resolve
   it when the config carries paths from an init environment).

The file is 0600 and stores the passphrase verbatim — the file content IS
the passphrase, never encoded (the file mode is the control; encoding was
only ever obscurity, and guessing at it silently mangled real passphrases).
`GUARDIAN_ENCRYPTION_KEY_PASSPHRASE` (an env var carrying the *path*) can
re-point it per environment.

### Honest limits

An unattended daemon must be able to decrypt its own key, so a same-host
attacker with the daemon's privileges always wins. What the encryption
defends is the **backup copies** and **at-rest theft** — stolen disks, leaked
snapshots, mis-scoped backups — which is where most real key theft happens.
The decrypted key is cached in memory for the process lifetime (the daemon
decrypts shares continuously) and zeroed on shutdown.

### Signing key posture

- The keyring is passphrase-encrypted (`file` backend) and opened in-process;
  the passphrase comes from the `keyring_passphrase` **file**, never a config
  or env value. `GUARDIAN_KEYRING_PASSPHRASE=<path to mounted secret file>`
  is the automated-fleet baseline.
- Where a desktop keychain exists, `keyring_backend: os` delegates at-rest
  protection to the operating system.
- The transaction signer sits behind an interface (`blockchain.TxSubmitter`),
  so a remote-signer/KMS backend can be added without refactoring. The
  KMS/HSM path itself is descoped (owner ruling, July 2026).

## Backup — first-class flow

```sh
guardiand key backup                          # prompts for a backup passphrase
guardiand key backup --output /secure/guardian.tfb --passphrase-file /secure/backup-pass
guardiand key backup --show-mnemonic          # also print the 24-word phrase
```

The bundle contains the **whole share-encryption keyring** (the current
epoch's key plus every retired epoch key still on disk), the signing keyring
files, and a fingerprint of the configuration — everything needed to come
back from bare metal and serve in-flight assignments under any epoch. It is
encrypted under a **backup passphrase of its own**, independent of the
at-rest key passphrase, because the bundle travels off-host.

The **24-word mnemonic** (BIP39 over the raw key bytes, per
[CLIENT_CONVENTIONS.md §5](CLIENT_CONVENTIONS.md)) is the human-typable
fallback. The words ARE the key: write them down, never store them digitally
in plaintext. Note the mnemonic is **per key** — `--show-mnemonic` prints the
*current* epoch's phrase, so after a rotation the old phrase still recovers
only the old epoch. The encrypted bundle is the primary artefact; mnemonics
are the last resort.

**Cadence and storage:**

- Take a backup **immediately after registration** — from that block onward
  the key is irreplaceable.
- Re-run after any signing-key change and **after every rotation** (the
  rotate-key ceremony writes a rotation bundle for you — see below).
- Keep at least one copy **off the guardian host** (surviving disk loss is
  the point) and store the backup passphrase **separately from the bundle**.
- The bundle file is safe to place on encrypted cloud storage; the mnemonic
  belongs on paper/metal in a safe.

## Restore — and the restore drill

```sh
guardiand key restore --input /secure/guardian.tfb        # bundle (prompts for its passphrase)
guardiand key restore --from-mnemonic                     # share key only, from the 24 words
guardiand key restore --input ... --offline               # no chain reachable
```

Restore refuses to declare success until the restored key **derives the
public key registered on-chain** for your guardian address (skippable with
`--offline`; `guardiand start` re-enforces it regardless — a daemon holding
the wrong share key refuses to run rather than silently missing reveals).
The share key is re-written encrypted at rest; keyring files are restored
into the configured keyring directory (`--force` to overwrite).

**Drill it before it matters.** On a scratch machine or empty
`--config-path` home:

1. `guardiand config init` (any placeholder signing key; use your real
   `guardian_address`, `chain_id`, `grpc_endpoint`).
2. `guardiand key restore --input <bundle>` — expect "Chain verification
   passed".
3. `guardiand config doctor` — expect all green.

If step 2 or 3 fails on the drill, your backup would have failed you in an
emergency. Fix it now.

## Key rotation — the hygiene path

Professional custody practice is periodic rotation, and the protocol supports
it natively: `MsgGuardianRotateKey` appends a new key epoch that applies to
**selections from the next block**, while every old epoch continues to serve
the assignments encrypted to it. Your address, float, selection history and
entry fee all survive — rotation ended rotation-by-re-registration.

```sh
guardiand rotate-key                          # interactive ceremony
guardiand rotate-key --backup-output /secure/rotation.tfb \
  --backup-passphrase-file /secure/backup-pass --yes     # non-interactive
```

The command is generate → **backup ceremony** → submit, in that order — it
never broadcasts before a bundle carrying the whole keyring (new key
included) is written. After the transaction lands it retires the old key to
`private_key.epoch<N>` beside the new one and updates the configuration.

**On-chain constraints** (anti-spam, not economics):

- A flat **rotation fee of `rate × 14,400`** (one guardian-day) is burned.
- **One rotation per 432,000 blocks (~30 days)**, measured from the current
  epoch's effective height — registration starts the clock. If your current
  key is compromised *inside* the window, `guardiand update
  --accepting-secrets=false` is instant, free, and gives identical forward
  protection; rotate when the window opens.

**Operationally:**

- The clean sequence is stop accepting → drain → rotate, but skipping it
  costs nothing: the daemon resolves the right epoch key for every
  assignment automatically (from the secret's creation height against the
  on-chain key history) — you are never asked which key applies.
- **Restart the daemon** after rotating if it is running (it also
  self-detects the new epoch and reloads from disk, but a restart makes the
  cutover immediate).
- A retired epoch's key file is eligible for **local deletion once its last
  assignment settles** — after settlement, reveals and evidence no longer
  need it. Deleting it earlier means a no-reveal slash on every assignment
  still encrypted to it. When unsure, keep it; the startup self-check
  refuses to run if an in-flight assignment's epoch key is missing.
- Every key ever registered stays **globally reserved forever** — a new
  registration or rotation can never reuse a retired key, yours or anyone
  else's.

## Compromise

If the **share key** (current epoch) may have leaked:

1. **Immediately** set `accepting_secrets` to false (`guardiand update
   --accepting-secrets=false`) — instant, free, and stops any new assignment
   being encrypted to the leaked key.
2. Serve out in-flight secrets honestly. The leak exposes only shares
   encrypted to the leaked epoch; note a holder of the key could submit
   early-reveal evidence against you, so assume those bonds are at risk.
3. `guardiand rotate-key` as soon as the rotation window allows, then resume
   accepting. Your address and float survive.

If a **retired** epoch's key leaks, in-flight secrets under that epoch carry
the same exposure, but no future assignment can be affected — the epoch is
already retired.

If the **signing key** may have leaked, that is account compromise — the key
signs withdrawals, updates and reveals, and it cannot be swapped on the
guardian record:

1. **Immediately** withdraw the unlocked float
   (`timeflared tx secrets guardian-withdraw-stake --from <guardian-key>`)
   and move the proceeds to a fresh account — you are racing whoever holds
   the key, and the withdrawal pays to the compromised address.
2. Set `accepting_secrets` to false so no new bonds are taken on.
3. The registration is unsalvageable: register a **fresh address** with a
   fresh encryption key (a new 1,000 VEIL entry fee — the old one is sunk).
   Bonds still locked for in-flight secrets settle to the compromised
   address as normal; treat whatever the attacker can reach as lost.
4. Prevention is the real defence: keep the signing key in the keyring on a
   hardened host (or an HSM), separate from the share key's storage.
