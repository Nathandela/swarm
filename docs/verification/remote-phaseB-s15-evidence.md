# S15 evidence — which tier seals which state (PB-STATE-9, PB-STATE-6, PB-SEC-10)

**Commit**: `82eb7e6`. **Requirements**: PB-STATE-9, PB-STATE-6, PB-SEC-10.
**Decisions**: PB-STATE-9 amended three ways at `e649b4b` before implementation.

## What this proves

That the phone's durable state is split across the two PB-KEY-2 tiers **measured from the bytes on
disk**, that a locked process can read only the wake tier plus the coordinates the load path needs to
open anything, and that the state directory is excluded from both Android backup paths.

It does **not** prove anything about a real device backup: PB-E2E-5 remains deferred and nothing here
attempts an ADB backup.

## The defect it closed

`persistState` wrote **everything in the clear** except the two epoch keys. `Sessions`, `Snapshots`
and `OpOutcomes` — decrypted journal entries, terminal snapshots and command outcomes — went to disk
as plain JSON, as did `PendingOps`, which carries session ids and, for a launch, **the command line
the user typed**.

PB-KEY-7 purges those from *memory* at the screen lock. Nothing sealed them at rest. The `android/gate`
PB-SEC-1 pair does not catch it, because it searches for epoch keys and device private scalars, not
for session content.

## PB-STATE-9 was unimplementable as written — three amendments, all from measurement

Found during RED, amended before implementation rather than discovered by the implementer:

1. **"Only the wake-tier state" is literally unsatisfiable.** Seven of `State`'s 22 exported fields
   get a tier from the requirement. Of the rest, `Machine` and `EpochID` must be read **before any
   unseal** (the load path filters another machine's blob wholesale; the tier carry decision keys on
   the epoch), and `RoutingID`/`MachineRelayAuthPub` are what the wake path uses to reach the relay
   with no user present. The criterion is now "only the wake-tier state **and the coordinates the
   load path must read in order to open it**", with that list pinned.
2. **The content tier cannot be ONE sealed blob** — the load-bearing amendment. A Save taken while
   locked must **preserve** the send-seq ceiling and receive high-waters it cannot read, or the phone
   renumbers from 1 and the gateway stale-drops everything for the life of the epoch. But `PurgeKeys`
   runs **at** the screen lock, with the content tier locked by definition, and PB-KEY-7 requires it
   to **destroy** the decrypted caches — which carry-verbatim cannot do. Jointly unsatisfiable by one
   container.
3. **`PendingOps` is content tier and NON-purgeable**: user content by any reading, but not a
   *decrypted* cache, so the lock purge must leave it.

## What shipped

**Schema v4 -> v5.** Three sealed containers replace eight cleartext fields:

| Container | KEK | Fields | Lifetime at lock |
|---|---|---|---|
| `wake_state` | wake | `PushToken`, `WakeReplay` | readable while locked |
| `content_kept` | content | `SendSeq`, `Receive`, `PendingOps` | **preserved** byte for byte |
| `content_purgeable` | content | `Sessions`, `Snapshots`, `OpOutcomes` | **destroyed** |

The purge writes `content_kept` and `wake_state` back verbatim without reading either — it cannot,
and does not need to.

Left in the clear, and this is exactly amendment (1)'s pinned list: `schema_version`, `machine`, the
three machine public keys, `routing_id`, `epoch_id`, `push_preference`, `reconciled_epoch`,
`grant_epoch`, `grant_seq`, `relay_cursor`, `stale`, `stale_streams`.

**`ErrContentTierLocked`** resolves the send-seq question honestly. A Save that would put non-empty
content-tier state into a container this process could not open is **refused**, so the reservation
issues no sequence number rather than silently carrying over and handing out seq 1. The facade
refuses earlier (`resolveSend` returns no-content-key before any seq is drawn), so the path stays
unreachable in production — the fence is at the core, where the guard did not exist.

**PB-SEC-10**: a new `data_extraction_rules.xml` with `<cloud-backup>` and `<device-transfer>`, each
carrying `<exclude domain="root" />` and no `<include>` anywhere. The pre-existing
`allowBackup="false"` does **not** cover device-to-device transfer on all manufacturers, which is
what the requirement names.

**No new key crossing.** The three containers go through the two `Sealer`s slice S14 already
delivered; `Config` is untouched, and both the no-crossing fence and the zero-cleartext-call-sites
fence still pass.

## How it is measured — the standard this slice set

Absence is never the whole assertion. Each row writes a sentinel into a `State` field, Saves once,
then reads every regular file under the state dir and searches for that sentinel in **every byte
form** — raw, base64 (what `encoding/json` emits for a byte slice), hex (what the store writes a
bucket sender as), and for integers the decimal ASCII **plus** big- and little-endian fixed width, so
an implementation that seals a binary encoding is measured just as exactly.

Every sealed row is paired with the **positive half**: the material must have been handed to *that
tier's* recording sealer and never to the other. A layout-independent attack additionally feeds every
base64 value in the JSON tree to the wake sealer and requires nothing content-tier to come back out.
**No Go accessor is in the path of any tier assertion.**

The inventory has a reflective completeness check **both ways** — a new `State` field with no row
fails, and a row naming a field that no longer exists fails. Unassigned rows assert their sentinel
**is** present, which is the census's own anti-vacuity control: a run against a truncated state dir
fails there rather than passing every absence check.

## Vacuity probes — the two ways this acceptance could have been faked

- **"Sealed = dropped"**: `persistState` replaced with a write of the schema version and machine id
  only. 10 of 12 tests fail, including all three persistence-side legitimate passers. The two
  survivors touch no persistence at all.
- **"Sealed = declared"**: everything left in the clear plus `"sealed_by_keystore":true` and two
  sibling flags spliced into the blob. **Verdicts identical to the unmutated RED run** — the
  declaration buys nothing, because every tier assertion reads bytes.

The second is the one that has actually bitten this project, which is why the `android/gate` tests
exist at all.

## Gates

`go build ./...` and `go vet ./...` clean. `go test -count=1` and `-race` green on
`internal/phonecore`. `android/gate` PB-SEC-10 and PB-STATE-6 tests 4/4 PASS. `golangci-lint` reports
only the four pre-existing errcheck findings in the journal tests, in files this slice did not touch.

Re-verified independently before commit.

## Accepted residuals

- **The rules file excludes `domain="root"`, not `device_root`.** That covers the app's private data
  directory, which is where the state dir lands today — nothing sets the facade's state directory
  yet. If a later slice moves it to device-protected storage so the wake path can run before first
  unlock (plausible for a push-woken app), the rules need a `device_root` exclusion too. Not added
  speculatively: the accepted-form list in the test is deliberately closed, and AGP lint could not be
  run here to confirm the domain token.
- **`Sealer.Seal` is now called on the content KEK for state, not only for a real key.** If the
  Android content sealer gates *encryption* on user auth as well as decryption, a first-run Save with
  the tier locked fails and no seq is issued. Fail-closed rather than a brick — the phone cannot send
  while locked anyway — and the same exposure the tier carry already had. **Worth confirming against
  the real Keystore wiring, which does not exist yet** (see the S14 evidence scope correction).
- **The state file is no longer byte-stable across rewrites**, because each container takes a fresh
  AEAD nonce. The records inside the containers are still sorted, so the plaintext is stable. Nothing
  depended on file-level stability.
- **The v4 -> v5 migration reads the pre-v5 cleartext fields and reseals on the first Save**, so the
  cleartext copy survives on disk until that Save. Inherent to migrating a file rather than rewriting
  it at load, and theoretical — no Phase B app has shipped, so there is no installed base.
- **The offline op queue is empty while the content tier is locked**, since it now comes from the
  sealed container. That follows directly from amendment (3) and matches how the session and snapshot
  caches already behave. Note the separately recorded finding that the queue has **no production
  writer at all**, so this is moot until that is decided.

## Derivation

**MACHINE-READABLE. `scripts/phaseb-traceability.py` reads this section** (ADR-007 B129). One row
per requirement, verdict `DERIVED` or `NOT DERIVED`, and for `DERIVED` the mutation that was made
to fail, in the same row.

| Requirement | Verdict | The mutation, and its result |
|---|---|---|
| PB-SEC-10 | DERIVED | four mutations of the shipped platform artifacts, all caught. `android:allowBackup="false"` -> `"true"` in `AndroidManifest.xml` -> caught (cloud half). The `android:dataExtractionRules` attribute deleted -> caught by three assertions (device-to-device half). `<device-transfer>` deleted from `res/xml/data_extraction_rules.xml` -> caught. `<cloud-backup>` deleted -> caught. The manifest and the rules file are what Android reads, so these are mutations of the control rather than of a table describing it |
| PB-STATE-6 | DERIVED | Backfilled from ADR-007 B121. Two mutations, both halves separately fenced: `allowBackup=true` -> 2 failures; `dataExtractionRules` dropped -> 3 failures. Subject is the shipped manifest. |
| PB-STATE-9 | DERIVED | Backfilled from B121: the decrypted session cache moved into the WAKE tier -> 2 failures, including the KEK-isolation half. Measured from the bytes on disk. |
