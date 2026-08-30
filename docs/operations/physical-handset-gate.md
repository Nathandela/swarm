# The physical-handset gate — Swarm Remote Control

# EVERY ROW IN THIS DOCUMENT IS **UNRUN**

**Nothing below has been performed.** Not once, not partially, not on an emulator. Every row carries
`[UNRUN]`, and **an `[UNRUN]` row is not evidence** — not of a pass, not of a failure, not of a
"should work". The tag is the operator's to remove, one row at a time, and only after performing
that row on hardware and writing what the device reported into §M.

This file replaces the legacy gate wholesale. The playbook is explicit about why: "The existing
physical-handset gate must be rewritten to current product decisions and then executed; old
`[UNRUN]` rows cannot be cited as evidence"
(`docs/specifications/remote-control-product-playbook.md:912`). The rewrite is a Wave R1 deliverable
(`playbook:701-702`) and the execution is a Wave R9 deliverable (`playbook:835`).

> **What the previous version of this file was, so no one cites it.** It described a topology this
> product no longer has: FCM credentials held by the self-hosted relay, a sideloaded debug APK as the
> shipping artifact, one machine per phone, and a per-use biometric authorization tier. All four are
> gone — the first by ADR-015 (the push gateway splits from the relay), the second by ADR-015 P3 and
> P5 (Play-signed Android is the first client; debug builds use a separate non-production
> gateway/project), the third by ADR-018 (one phone, N machine pairings), and the fourth by ADR-007
> B133 (all phone-side user authentication was removed when the trust boundary moved to the wire).
> No row, table or verdict from that document may be carried forward, quoted, or partially reused.

> **An emulator is not a handset.** ADR-007 **B31** makes that a standing prohibition, and the
> playbook restates it for the wave that owns the push behavior: "no fake endpoint or emulator counts
> for the exit" (`playbook:748-749`). An artifact produced in an AVD may not be recorded in §M and may
> not remove an `[UNRUN]` tag. Running a row against an emulator "to check the runbook works" is
> allowed and is worth nothing here.

> **The build under test is the Play-signed build from the closed track.** ADR-015 P5: "Debug builds
> use a separate non-production gateway and Firebase project" (`ADR-015:59`, `playbook:561`), so a
> sideloaded debug APK is physically incapable of discharging any `PH-PUSH`, `PH-LOCK` or `PH-KEY`
> row that names the production gateway. `docs/operations/sideload.md` is the **pre-Play bootstrap
> only**: it exists so a device can be brought up before the closed track exists, and a row run
> against a sideload must be recorded as such and re-run against the closed-track build before it
> counts. R9 owns "Play closed-track upgrade from the current app" (`playbook:839`).

> **Push topology throughout is ADR-015's.** Every push row below assumes: a Keystore-backed
> **installation key** and an opaque installation id (P5); a per-pairing **opaque push address**,
> submit capability and machine-revoke capability (P6); a **phone-generated wake key** conveyed in
> the authenticated pairing transcript and never held by the gateway (P7); `WakeV1` as a 74-byte
> content-free envelope (P8); `swarm-remote` as the **only** submitter, under a durable coalescible
> obligation (P9); and a relay with no push credential, no token map and no push transport (P1).
> A row that observes the relay delivering a push has not passed — it has found that the migration
> did not land.

---

## Executability — which waves must be green before a section can even be attempted

A section whose blocking waves are unbuilt is **not executable**, which is a different verdict from
"failed" and must be recorded as `not executable — <wave>` rather than as a failure or a blank.
Waves are `playbook:654-842`.

| § | Area | Test ids | Blocking waves | Notes |
|---|---|---|---|---|
| A | Preconditions and provenance | `PH-DEV-1..9` | R0 (green CI baseline); **R2** for `PH-DEV-5` (released relay bundle, `swarm relay doctor`); **R3** for `PH-DEV-6`'s `push_transport` value; R9 for the closed-track build | Sideload bootstrap may satisfy §A only, and is recorded as a bootstrap |
| B | Pairing | `PH-PAIR-1..9` | R2 (relay bundle, Web-PKI policy, PNG/manual flow); R4 for `PH-PAIR-8`; R3 for the push objects in `PH-PAIR-9` | |
| C | Lifecycle | `PH-LIFE-1..7` | R3 for every row that requires a wake (`3,4,5,6`); R2 for `1,2,7` | |
| D | Push | `PH-PUSH-1..17` | **R3** in full | Nothing here is executable before the gateway exists |
| E | Locked device | `PH-LOCK-1..5` | **R3** | |
| F | Network | `PH-NET-1..6` | R2 + R3 | `PH-NET-3/4` need R3 for the background half |
| G | Sleep, relay restart, pending approval | `PH-RESIL-1..4` | R2 + R3; `PH-RESIL-3` additionally **R6** (a real pending approval) | |
| H | Keystore, upgrade, corruption, device loss | `PH-KEY-1..8` | R3 (installation key, gateway revoke); R4 (`PH-KEY-5,6`); R9 (`PH-KEY-5` real upgrade) | |
| I | Multi-machine | `PH-MM-1..9` | **R4** | R2 is a precondition: three pairings need two real relays |
| J | Remote launch | `PH-LAUNCH-1..7` | **R5** | |
| K | Structured chat and approvals | `PH-CHAT-1..7` | **R6** (Claude) and **R7** (Codex); `PH-CHAT-3,4,5` need both | |
| L | Retired Android terminal fallback | none — `PH-FALL-1..7` retired 2026-08-30 | — | Machine/wire compatibility remains covered outside the handset UI gate; production Android has one conversation shell. |

**The gate is discharged only when every active section A–K has run** (§N). Section L is retained
as an explicit retirement record, not as seven hidden release obligations. Sections may be executed
as their waves land; a partial run is a partial record, never a partial discharge.

---

## 0. What to record, and the four ways a row can pass while the property is absent

Read this before row 1. It is the difference between running the gate and appearing to.

> **Record what the device reported — per row, per device, per pairing. Never record that a step
> "worked".** A result line reading "OK" is not a result. Where a row has a hardware form and a
> fallback form, record **which one you observed**, not that one of them occurred.

Four failure modes produce a green-looking row over an absent guarantee. Each is named here because
each has bitten this repository's reasoning already:

1. **A quiet machine looks exactly like working delivery.** If no transition fires, no wake is owed,
   and "nothing arrived" is indistinguishable from a healthy idle system. Every push row requires a
   transition you independently confirmed on the machine side, recorded beside the handset
   observation.
2. **A software-only Keystore key does everything a hardware-backed one does.** `KeyGenParameterSpec`
   has no setter that requires TEE backing, so for the ordinary hardware-backed case there is no
   requested value for the platform to contradict. The one exception runs the other way:
   `setIsStrongBoxBacked(true)` does request a specific level and throws
   `StrongBoxUnavailableException` rather than degrading silently, which is why `PH-KEY-1`'s sheet
   carries a `strongBoxBacked` column as well as the security-level constant. `PH-KEY-1` is a human
   observation for that reason; `PH-KEY-2`'s attestation is what turns it into evidence.
3. **A sideloaded debug build talks to a non-production gateway and delivers wakes perfectly.**
   It proves the app; it proves nothing about the production topology, the Play-signed package's FCM
   registration, or Play Integrity. Record the build's track and signing certificate on every push
   row.
4. **A single-machine result generalizes to nothing.** ADR-018's isolation properties are only
   observable with three pairings live at once (§I). A row run against one pairing is recorded as a
   one-pairing observation, not as an isolation result.

**Both devices, unless the row says otherwise.** The matrix requires "one current and one
oldest-supported Android API level" (`playbook:894`), so every row runs on Device A and Device B and
§M has a column for each. Rows marked **[ONE-DEVICE]** are inherently single-handset (device loss,
the three-machine topology) and record which device was used.

**A failure is a result.** Record it, file it, and the row is discharged as *run with findings*. The
only impermissible outcome is a row reported as passed while its `[UNRUN]` tag stands.

---

## A. Preconditions and provenance — `PH-DEV`

**`[UNRUN]` PH-DEV-1. Device A: the current API level.** A physical Android handset at the newest
API level available at execution time. Record make, model, `ro.build.version.sdk`, Android release,
build number, security patch level, ABI (`arm64-v8a`), RAM, and whether it has a hardware
StrongBox.

**`[UNRUN]` PH-DEV-2. Device B: the oldest supported API level.** Read `SWARM_ANDROID_MIN_SDK` from
`android/toolchain.env` at execution time and record the value you read (it is **33** as this file is
written; if it has moved, the gate follows the file, not this sentence). Device B must be at exactly
that API level, not merely below Device A. Record the same fields as `PH-DEV-1`.

**`[UNRUN]` PH-DEV-3. Build provenance, on both devices.** Install the **Play closed-track** build.
Record package id, `versionCode`/`versionName`, the track, the Play App Signing certificate
fingerprint as reported on-device, and the app SHA the build was produced from. If a row is run
against a sideload instead, record `bootstrap sideload` in that row's verdict and re-run it against
the closed-track build before the section is discharged.

**`[UNRUN]` PH-DEV-4. Gateway and Firebase environment.** Record the production push-gateway base URL
and version, the Firebase project id and sender id the installed build is configured against, and the
gateway SHA. Confirm from the build, not from a document, that it is **not** the debug
gateway/project (ADR-015 P5). Any `PH-PUSH`, `PH-LOCK` or `PH-KEY` row run against the debug
environment is void.

**`[UNRUN]` PH-DEV-5. Two relays.** Provision `relay-org` (which will host machines M1 and M2) and
`relay-alt` (M3) from the released R2 bundle. For each: record the URL, the TLS policy in force
(`web_pki` is the default, ADR-016 W1), the certificate issuer and expiry, and the full output of
`swarm relay doctor <url>`. Neither relay holds a push credential; confirm and record that the shipped
config has no such field (ADR-015 P1).

**`[UNRUN]` PH-DEV-6. Three machines.** M1, M2, M3, each with `swarm remote init` completed and
`swarm-remote` on `PATH`. Record for each: OS and version, swarm SHA, relay assignment, and — after
pairing — the persisted `push_transport` value, which must be `gateway` for every pairing in this
gate (`legacy_relay` means §D is testing the transport this gate exists to replace).

**`[UNRUN]` PH-DEV-7. Cellular service on both handsets.** A live cellular data plan on Device A and
Device B. Without it §F cannot run and `PH-LAUNCH-1`'s over-cellular clause cannot be honoured.
Record carrier and radio technology.

**`[UNRUN]` PH-DEV-8. `adb` and logcat on both devices.** Developer options and USB debugging enabled
on a Play-signed build. Record how logcat was captured and retained; several rows below are only
observable through it.

**`[UNRUN]` PH-DEV-9. Clock and measurement discipline.** Record each handset's clock offset against
the machine and the method used to measure it, because `playbook:849-850` requires same-device
monotonic boundaries where possible and a measured offset with an error bound for cross-device spans.
Confirm automatic time is on and record the offset; `PH-PUSH-17` deliberately breaks this later and
restores it.

---

## B. Pairing — `PH-PAIR`

Blocking: **R2**. `PH-PAIR-8` additionally **R4**; `PH-PAIR-9` additionally **R3**.

**`[UNRUN]` PH-PAIR-1. Camera QR pairing, Device A ↔ M1.** Run `swarm remote pair` on M1 and scan
with the handset camera. Record ambient light, distance, screen (terminal or PNG/browser), and
decode time. Repeat on Device B and record separately — the two cameras are different hardware.

**`[UNRUN]` PH-PAIR-2. Which QR rendering actually scanned.** ADR-007 B141 rules that the PNG at
16 px/module is the promised scan target and the 80x24 terminal symbol is best effort. Record whether
the terminal symbol decoded at the terminal's default font size, at what point it stopped decoding,
and whether you fell back to the PNG/browser rendering. A terminal-symbol failure is **not** a gate
failure; a PNG failure is.

**`[UNRUN]` PH-PAIR-3. Manual pairing.** Pair M2 to Device A using the relay-URL-plus-short-code path
instead of a camera (`playbook:190-192`). Record every value typed, the number of attempts, what the
app did with a mistyped code, and how long the flow took. This path exists because the 39-character
`MaxRelayURLLen` ceiling is real and must not be "disguised as terminal too small"; record the actual
relay URL length you used.

**`[UNRUN]` PH-PAIR-4. SAS on both surfaces.** Record both SAS strings verbatim and that they
matched, and that the terminal required a **local** confirmation before completing.

**`[UNRUN]` PH-PAIR-5. Origin display and the negative control.** Confirm the app displayed the relay
origin and required explicit confirmation before joining. Then point the camera at a QR encoding a
**different** relay destination and record that the app displayed that origin and refused to join
silently. The negative control is the half that matters.

**`[UNRUN]` PH-PAIR-6. The post-pairing screen makes no push promise.** `playbook:187-188`: the first
screen "never presents a successful pairing as proof that push or background delivery works". Record
the copy verbatim, and record whether push health is shown as a separate, honest state.

**`[UNRUN]` PH-PAIR-7. TLS policy, positive and negative.** Record that the default `web_pki` policy
completed a pairing with no pin anywhere in the flow (ADR-016 W1/W3). Then deliberately break it once
— wrong hostname, or an expired certificate — and record the failure vocabulary the app showed
(ADR-016 W8) and that a **first** pairing failure was non-terminal and offered a retry (B58's rule).
If you exercise the expert `pinned_spki` policy, record it as a separate observation; it is not the
default and a pass under it says nothing about the default.

**`[UNRUN]` PH-PAIR-8. A second computer does not replace the first.** [R4] Pair M2 while M1 is
already paired and record that both survive with independent state (`playbook:186`). Under the
pre-R4 singleton state this row **destroys** the first pairing by design
(`internal/phonecore/state.go`'s machine filter); running it before R4 lands is a data-loss operation,
not a test.

**`[UNRUN]` PH-PAIR-9. The pairing transcript carries the push objects.** [R3] For each pairing,
record that the authenticated transcript conveyed the phone-generated **wake key**, the **push
address**, the **submit capability** and the **machine-revoke capability**, and that the machine
persisted all four **before** confirming pairing (ADR-015 P6/P7, `playbook:140-142`). Record where you
observed it. Record also that the wake key was generated on the phone — a machine-derived
epoch wake key means P7 did not land.

---

## C. Lifecycle — `PH-LIFE`

Blocking: R2 for `1,2,7`; **R3** for `3,4,5,6`.

**`[UNRUN]` PH-LIFE-1. Foreground is a live wait-driven connection.** With the app foregrounded,
record that the connection is `MailboxWait`-driven and that no 500 ms polling loop is running
(`playbook:867-868`). Record how you observed it (logcat, relay-side request rate).

**`[UNRUN]` PH-LIFE-2. Backgrounding disconnects.** Background the app and record that the relay
socket closed (ADR-007 B16 — this is the designed behavior, not an edge case) and that the machine
observed the disconnect. Record the elapsed time from background to observed close.

**`[UNRUN]` PH-LIFE-3. Process death.** `adb shell am kill dev.swarm.phone` mid-session, then trigger
a machine-side transition, then open from the resulting notification. Record: the wake arrived; the
same device id and pairing survived with no re-pair; cursors and sequence coordinates were intact;
and any fail-closed refusal of mutating operations cleared on reconnect rather than persisting.

**`[UNRUN]` PH-LIFE-4. Reboot, app never opened.** Reboot, do **not** open the app, unlock the device,
then trigger a transition. Record whether the notification arrived and how long it took. This is the
row that most often fails on real hardware and is unobservable anywhere else.

**`[UNRUN]` PH-LIFE-5. Reboot, before first unlock.** Repeat `PH-LIFE-4` without unlocking. Record
whether anything arrived before first unlock and, if it did, that it was a generic notification with
no routing and no state mutation (§E's rules apply to it in full).

**`[UNRUN]` PH-LIFE-6. Force-stop proves no promised wake.** `adb shell am force-stop dev.swarm.phone`,
then trigger several machine-side transitions over at least ten minutes. **The required observation is
a negative one**: no wake is promised or expected until the user manually reopens the app
(`playbook:311-312`). Record what actually arrived (Android's behavior here is device- and
OEM-dependent; record it, do not assume it). Then manually reopen and record that reconciliation
completed **with no data loss** and that the app **explained the stale period** in visible copy.
Record that copy verbatim. A force-stopped app that silently catches up without explaining the gap
fails this row.

**`[UNRUN]` PH-LIFE-7. Reconciliation budget.** With the app foregrounded, drop and restore
connectivity and record the time from "Android reports usable connectivity" to "reconciled foreground
state" over enough samples to state a p95 (`playbook:859`, budget p95 ≤ 5 s). Record sample count and
every failure/timeout — excluded samples are not permitted.

---

## D. Push — `PH-PUSH`

Blocking: **R3, in full.** Before R3 the gateway does not exist; every row here is
`not executable — R3`.

**`[UNRUN]` PH-PUSH-1. Installation registration.** On first run, record that the app exchanged an
FCM token plus a Keystore-generated installation public key for an **opaque installation id**, and
that **no machine address was allocated** by that call (ADR-015 P5, `playbook:518`). Record the
installation id, the Play Integrity/App Check verdict, and what the app did if attestation failed
(refusal or foreground-only degradation — never a durable enrollment).

**`[UNRUN]` PH-PUSH-2. Per-pairing allocation, and the ten-minute expiry.** For each of the three
pairings record a **distinct** `push_address`, and that the submit and machine-revoke capabilities are
distinct values. Confirm the capabilities do not appear in any app log, gateway log or trace you can
read. Then allocate an address and **abandon the pairing**; after ten minutes record that the
allocation is gone and that pairing must allocate again (`playbook:979`, ADR-015 P6).

**`[UNRUN]` PH-PUSH-3. Inspect the raw FCM request.** Capture the actual request the gateway sends and
record: data-only body, `android.priority` high, a five-minute TTL and one app-wide collapse key
(`playbook:303`, which ADR-015 P8 cross-references rather than rules), a device token and never a
topic (`ADR-015:17`, where that property is carried forward from the existing sender rather than
decided by P8), and a `WakeV1` payload of **exactly 74 bytes** with empty plaintext — the envelope,
the AAD tuple and the locator ban are the parts ADR-015 P8 itself rules on. Record that the payload
contains no session, interaction, cursor, provider, category, repository, prompt, tool or approval
locator **even encrypted**, and no session name, hostname, agent name or Group label. Record the
cleartext fields that *are* present — version, type, `push_address`,
`wake_seq`, `issued_at` — because that disclosure is conceded by ADR-015 P8 delta 1 and this row is
where it is confirmed rather than assumed.

**`[UNRUN]` PH-PUSH-4. Foreground delivery.** App foregrounded, trigger an independently-confirmed
machine-side transition, record arrival and latency.

**`[UNRUN]` PH-PUSH-5. Background delivery.** App backgrounded (socket closed per B16). Record
FCM-submission-to-notification-callback latency over **at least 50 normal-background samples per
handset** (`playbook:851`). Record the p50/p95, the sample count, every timeout, and the correlation
ids. Report it as an observed distribution, never as a per-delivery guarantee (`playbook:860`).

**`[UNRUN]` PH-PUSH-6. Doze.** `adb shell dumpsys deviceidle force-idle`. Record the exact state
achieved — `IDLE` versus `IDLE_MAINTENANCE` — because a wake delivered during a maintenance window is
not a Doze wake. At least **20 samples**, reported as their own distribution and never merged into
`PH-PUSH-5`'s (`playbook:852-853`).

**`[UNRUN]` PH-PUSH-7. App standby.** Put the app in the `rare` and then the `restricted` standby
bucket (`adb shell am set-standby-bucket`). Record delivery, latency and bucket for each, ≥20 samples
per bucket.

**`[UNRUN]` PH-PUSH-8. Battery saver.** Enable battery saver and repeat, ≥20 samples. Then record that
the Settings screen still displays ADR-007 B143's fixed Doze/battery-saver disclosure sentence,
**verbatim and unconditionally** — it is not a conditional notice and its wording is not a casualty of
the gateway move (ADR-015's invariant list).

**`[UNRUN]` PH-PUSH-9. Notification permission denied.** Deny `POST_NOTIFICATIONS`. Trigger
transitions and record: no notification posted; the app shows an honest degraded state naming the
denied permission; and the app makes **no** claim of reliable background delivery
(`playbook:1027-1028`). Record whether the wake was still processed on the next foreground open, and
whether any state was mutated while the permission was denied (it must not have been beyond what a
foreground open would do anyway).

**`[UNRUN]` PH-PUSH-10. Notification permission granted after denial.** Grant it and record that
delivery resumes without a re-pair or a re-registration ceremony visible to the user, and how long the
recovery took.

**`[UNRUN]` PH-PUSH-11. Token rotation.** Force an FCM token rotation (`FirebaseInstallations.delete()`,
or clear the app's FCM state). Record that the app used the gateway's **rotate-token** operation, that
**no machine pairing was touched** (ADR-015 P5's defining property), and that delivery resumed on all
three pairings. A rotation that triggers N re-pairings is a P5 failure.

**`[UNRUN]` PH-PUSH-12. Wake-obligation durability under crash injection.** With the machine driving a
session, kill `swarm-remote` at each boundary in turn: before the mailbox append, after the append and
before gateway submission, after submission and before the gateway's response, and after the gateway
committed but before local acknowledgement (`playbook:741-743`, ADR-015 P9). Record for each: the
mailbox event was never rolled back or hidden; the obligation was retried byte-identically on restart;
and any duplicate wake was rejected harmlessly by the phone's authenticator and per-address high-water.
Record any lost obligation as a defect — this is the crash-consistency property the whole background
path rests on.

**`[UNRUN]` PH-PUSH-13. Replay and expiry, honestly bounded.** Re-submit a byte-identical wake and
record that the phone rejected it as a replay without any user-visible effect. For expiry: the
five-minute `WakeV1` bound equals the FCM TTL, **so a naturally-delayed wake is dropped by FCM before
the phone can refuse it as expired**. Record what you attempted, what you observed, and whether the
expiry refusal path was reachable at all on hardware. "Not observable through FCM" is a legitimate,
useful entry here; "passed" is not.

**`[UNRUN]` PH-PUSH-14. Gateway disabled and gateway unreachable.** Turn the gateway off from Settings
and record that the app says **"foreground updates only"** and implies nothing about background
delivery (ADR-015 P12, `playbook:148-149`). Separately, make the gateway unreachable (airplane the
machine's egress, or point at a dead host) and record that Settings **and** the affected machine's
health row both show a visible degraded state (`playbook:313`).

**`[UNRUN]` PH-PUSH-15. Category preference is machine-authoritative.** Turn one push category off from
the app. Record that the **machine** stopped submitting that category (observed machine-side, not
merely that the phone stopped showing it), and that suppression failed closed on doubt (ADR-015 P9's
retained `PushNotifier` behavior). Then re-enable and record that a deferred wake was delivered rather
than dropped.

**`[UNRUN]` PH-PUSH-16. Both revocation paths, with the right credential each.** Phone-side "forget this
computer" must revoke the push address **with the installation key** (ADR-015 P6, `playbook:146`);
machine-side "revoke this phone" must use the **machine-revoke capability** and retry deletion durably
**after** local epoch rotation (`playbook:147`). Run each once, on different pairings, and record: which
credential was used, that wakes for that pairing stopped, that the other two pairings were unaffected,
and — for the machine-side path — that the deletion obligation survived the `swarm-remote` process exit
that the epoch rotation causes (ADR-015 P6's M5 edge). A revocation that only succeeds while the
process happens to stay alive has not passed.

**`[UNRUN]` PH-PUSH-17. Clock skew, at the new five-minute bound.** Set a handset's clock **six minutes
fast**, trigger transitions, and record what happens. The reasoned expectation, unobserved: every wake
is refused as expired while the connection state may still read `online`, so the phone goes silently
deaf to the background plane. The bound narrowed from ten minutes to five with `WakeV1` (ADR-015 P8
delta 3), so this failure is now **easier** to reach than it was, not harder. Record what the app
displayed. Restore the clock afterwards and re-verify one delivery.

---

## E. Locked device — `PH-LOCK`

Blocking: **R3**. These five rows are the matrix's "locked-device wake when the Keystore key is and is
not immediately available; neither case routes or mutates before authentication"
(`playbook:900-901`).

**`[UNRUN]` PH-LOCK-1. Locked wake, key available.** With the device locked and the wake key
immediately usable, trigger a transition. Record: a **generic** notification appeared; its content
reveals no session name, hostname, agent name, Group label, machine name or count; and the app
performed **no** routing, no deep-link resolution, no connection and no state mutation before unlock.

**`[UNRUN]` PH-LOCK-2. Locked wake, key NOT available.** Arrange the state where the Keystore key
cannot be used while locked (device policy, or the app's own locked-key path). Record that the envelope
was **stored**, that only a generic notification was posted, and that nothing was routed, connected or
mutated until unlock and validation (`playbook:307-310`). Record how you arranged the state — if the
shipped build has no reachable locked-key-unavailable path on your hardware, record that instead of
inventing one.

**`[UNRUN]` PH-LOCK-3. Neither case derives a target from FCM data.** After unlocking in each of the two
cases above, record that the deep-link target came from **relay reconciliation**, and that a wake
resolved **at most its opaque machine pairing** (`playbook:203-206`, ADR-018 MM5). Record where you
observed the ordering. Then delete the target session on the machine before unlocking and record that
the app opened that machine's inbox with an honest explanation rather than a wrong or blank card.

**`[UNRUN]` PH-LOCK-4. Verify-then-compare-then-persist ordering.** Record evidence that the tag was
verified before any header field was trusted and that the per-address high-water was persisted
**before** routing (`playbook:539-540`, ADR-015's invariant list). If the ordering is not observable
from outside the app on the shipped build, record `not observable on release build` and name what would
make it observable — do not record it as passed.

**`[UNRUN]` PH-LOCK-5. No retroactive reveal.** After unlocking, record that the already-posted
notification's content did not change to reveal session detail on the lock screen or in the shade
history.

---

## F. Network — `PH-NET`

Blocking: R2 + R3.

**`[UNRUN]` PH-NET-1. Wi-Fi foreground latency.** Phone-send to daemon-acceptance, and
accepted-item to visible-update, over **≥200 successful post-warm-up samples on Wi-Fi**
(`playbook:850-851`). Budgets: p50 ≤ 150 ms / p95 ≤ 300 ms, and p95 ≤ 300 ms excluding provider emission
delay. Record distributions, sample counts, failures, and the SHAs/region/carrier metadata
`playbook:847-848` requires.

**`[UNRUN]` PH-NET-2. Cellular foreground latency.** The same, ≥200 samples on cellular.

**`[UNRUN]` PH-NET-3. Wi-Fi → cellular handoff mid-session.** Disable Wi-Fi mid-session. Record:
reconnect time; that any live control generation was **severed** rather than paused (§L, ADR-017 T8);
that buffered input resolved as **undelivered** rather than riding the reconnect; that no keystroke or
message was duplicated; and that nothing was automatically replayed
(`playbook:862-863` budgets both to zero).

**`[UNRUN]` PH-NET-4. Cellular → Wi-Fi handoff.** The same in the other direction.

**`[UNRUN]` PH-NET-5. Captive portal.** Join a Wi-Fi network behind a captive portal without
authenticating. Record that the app reported `reconnecting` or `offline` **honestly** and did not report
`online` over a network that cannot reach the relay, then authenticate to the portal and record the
recovery.

**`[UNRUN]` PH-NET-6. Full offline and restore.** Airplane mode for at least five minutes with machine
activity throughout, then restore. Record the reconciled-state latency, that any in-flight operation
resolved through `operation_status` to `sent`/`refused`/`outcome_unknown` rather than by replay, and
that an `uncertain` or `outcome_unknown` item was **never** auto-retried (`playbook:259-260`).

---

## G. Laptop sleep, relay restart, pending approval — `PH-RESIL`

Blocking: R2 + R3; `PH-RESIL-3` additionally **R6**.

**`[UNRUN]` PH-RESIL-1. Laptop sleep.** Sleep the machine mid-session. Record which state the phone
displayed and — critically — that "machine asleep" was inferred **only from authenticated
presence/liveness evidence, not from a generic timeout** (`playbook:298-299`). Record also that the
phone made no promise of a "machine went silent" wake: ADR-015 P10 removed that wake's transport
deliberately, and the phone learns of silence on reconciliation. A UI that claims otherwise is a
finding.

**`[UNRUN]` PH-RESIL-2. Laptop wake.** Wake the machine. Record reconnect time, that the session resumed
without replacement, and that the agent had been **paused** rather than computing while asleep
(`playbook:300`).

**`[UNRUN]` PH-RESIL-3. Relay restart during a pending approval.** [R6] With an approval card pending on
both surfaces, restart the relay process. Record: the approval re-delivered after reconnect on both
surfaces; it resolved exactly once; the acked cursor was not lost across the restart; and no duplicate
resolution or duplicate semantic operation occurred (`playbook:862` budgets duplicates to zero).

**`[UNRUN]` PH-RESIL-4. Relay restart with a phone-authored operation in flight.** Restart the relay in
the window after a composer send begins. Record the visible state transition — it must reach
`uncertain` and then be resolved by `operation_status` reconciliation to `sent`, `refused` or
`outcome_unknown` — and that the app never auto-retried and never presented an unseen queue as success.

---

## H. Keystore, upgrade, corruption, device loss — `PH-KEY`

Blocking: R3; `PH-KEY-5`/`PH-KEY-6` additionally **R4**; `PH-KEY-5`'s upgrade leg additionally **R9**.

**`[UNRUN]` PH-KEY-1. Keystore-backed key creation, per alias, per device.** After first pairing, read
back `KeyInfo` for **every alias the shipped build actually provisions** and record all fields
verbatim. Do not copy an alias list out of this document into a result sheet: the alias set is R3/R4's
to define (installation key, per-pairing wake and content custody), and a sheet listing aliases the
build does not create is a fabricated record. Record `KeyInfo.getSecurityLevel()` as the constant
(`SECURITY_LEVEL_STRONGBOX` / `SECURITY_LEVEL_TRUSTED_ENVIRONMENT` / `SECURITY_LEVEL_SOFTWARE`), not a
boolean. This is unconditional: `SWARM_ANDROID_MIN_SDK=33` and `PH-DEV-2` pins Device B to exactly that
level, so both handsets are above the API 31 floor where the method exists and there is no pre-31
branch to look for.

> **What would falsely pass:** recording "provisioning succeeded, no downgrade thrown". A software-only
> key provisions cleanly and nothing in the product objects. **A row reading `SECURITY_LEVEL_SOFTWARE`
> is a recorded failure of the hardware-backing claim even though the app works perfectly.**

**`[UNRUN]` PH-KEY-2. Attestation.** Obtain a hardware attestation certificate chain for the
installation key and for the content-custody key, and verify each to a Google root. Record the root the
chain terminates at and the attestation security level. `KeyInfo` is what the platform says;
attestation is what makes it evidence. If a device cannot produce a chain, that is the result.

**`[UNRUN]` PH-KEY-3. Capability probe and its refusals.** Record the platform-capability probe's answer
for **each** capability the shipped `CustodyPlan` requires, individually, as `PRESENT` / `ABSENT` /
`UNKNOWN`. Do this **before** the rest of §H: the plan fails closed, so a non-`PRESENT` answer for any
required capability means the app refuses to provision at all. **A refusal here is a first-class result
and is more valuable than the rest of this section** — record it, file it, and do not work around it.
Record specifically whether any capability that the shipped matrix does not consume is still required as
a canary, and whether it refused on this hardware.

**`[UNRUN]` PH-KEY-4. At-rest custody.** With the app paired and the device unlocked, pull the app's
private data directory and record which files exist and their sizes. Search them for the raw key
material verbatim and record **"no verbatim match"** — never "no key material present". Base64
alignment means a needle buried mid-field is invisible to a byte search, and this row's own weakness is
part of its result.

**`[UNRUN]` PH-KEY-5. App upgrade.** [R4+R9] Install the previous closed-track build, pair all three
machines, then upgrade in place to the current build. Record: no re-pair required; every pairing's keys,
cursors and sequence coordinates survived; the multi-machine migration ran read-verify-namespace-commit
in that order with the registry committed **last** (ADR-018 MM6); the notification opt-in survived; and
**no pairing ever had two live send sequencers** — the failure MM6 calls unrecoverable. Then confirm the
previous build can still open its state read-only (the rollback-readable window).

**`[UNRUN]` PH-KEY-6. Corrupted state, scoped to one pairing.** [R4] Truncate or garble **one** machine
namespace's durable state on-device. Record: that pairing degraded with a per-machine recovery screen
naming what is broken and how stale it is; the other two pairings stayed live; and the aggregate inbox
identified the broken row (ADR-018 MM8, `playbook:575-576`). A corruption that crosses namespaces is a
storage-layout defect, not a recovery bug.

**`[UNRUN]` PH-KEY-7. Keystore invalidation.** Invalidate the device's Keystore material in a way the
platform supports (remove and re-add the screen lock, or restore the app to a different device). Record
the exact refusal the app surfaced, that it read as **repair required** and not as **revoked** — the two
share a remedy and not a cause and must read differently — and that the app offered a bounded recovery
path rather than a loop the user can satisfy forever with no effect.

**`[UNRUN]` PH-KEY-8. Phone loss and revocation drill.** [ONE-DEVICE] Simulate a lost handset: from the
machine, run `swarm remote revoke`. Record: commands from the old device are refused immediately; the
machine's epoch rotated where required; the relay mailbox cleanup was requested and retried durably; the
gateway push address was deleted (retried across the process exit the rotation causes); and re-pairing
**without** revoking first is refused by the single-device registry (ADR-018 MM7, MM1). Then complete a
fresh pairing and record how many steps it took. There is no account recovery and none may appear.

---

## I. Multi-machine — `PH-MM`

Blocking: **R4** (and R2 for two real relays). Topology is fixed by the matrix and by R4's exit
(`playbook:763-766`, `playbook:905-906`): **M1 and M2 on `relay-org`, M3 on `relay-alt`, one handset.**

**`[UNRUN]` PH-MM-1. Three live pairings.** [ONE-DEVICE, then repeat on the second device] All three
paired and live in the foreground. Record the **documented concurrency cap**, and if three exceeds it,
record the deterministic least-recently-viewed policy in action and that the beyond-cap rows visibly
showed their last-sync age (`playbook:200-202`).

**`[UNRUN]` PH-MM-2. Independence audit.** Record, per pairing and by observation rather than by
assertion, that these are distinct and shared with nothing (ADR-018 MM2): device signing key and
recipient key; epoch id and content key; outbound send-seq space and inbound high-water; relay URL,
relay-auth key, routing id and cursor; folded read models; operation-id space and journal; state
directory namespace; push address, wake key, submit capability and machine-revoke capability. Record
**how** each was observed. An unobservable item is recorded as unobserved, not as independent.

**`[UNRUN]` PH-MM-3. Concurrent updates from all three.** Drive session activity on M1, M2 and M3
simultaneously. Record that every event on the aggregate stream was machine-qualified, that no item
appeared under the wrong machine, and that the inbox ordering stayed sane under concurrency.

**`[UNRUN]` PH-MM-4. Duplicate session ids.** Arrange the **same session id** on M1 and M3. Record that
the roster, aggregate inbox, deep-links, operation records and cache keys all kept them distinct, and
that a notification for one never opened the other. Identity is `(machine_id, session_id)` everywhere
(ADR-018 MM4). Record how you forced the collision; if it required a machine-side fixture, record that
too.

**`[UNRUN]` PH-MM-5. Duplicate display names.** Two sessions with identical display titles on two
machines. Record that no surface used the title as an authority.

**`[UNRUN]` PH-MM-6. Isolated revoke.** Revoke the phone from **M2 only**. Record: M2's row reads
revoked with the correct copy; M1 and M3 kept their keys, cursors, seq spaces and push addresses
untouched and unrewritten; wakes for M1 and M3 continued; and no cross-pairing retry loop touched
another machine (ADR-018 MM7's isolation sentence).

**`[UNRUN]` PH-MM-7. Forget versus revoke.** From the phone, "forget this computer" for M3. Record: the
push address was revoked **with the installation key first**; machine keys and cache were then deleted;
the user was warned that the computer still authorizes the old device id and must revoke before
re-pairing; and the copy is visibly different from the machine-side revoke copy. Then exercise **force
forget** and record the second confirmation, the bounded opaque deletion tombstone, and the copy naming
the possible residual generic-notification window (`playbook:319-324`).

**`[UNRUN]` PH-MM-8. Process death restores all three.** `am kill` with three pairings live. Record all
three restored with no re-pair, correct cursors, and — again — no pairing with two live send sequencers.

**`[UNRUN]` PH-MM-9. Per-machine push routing.** Trigger transitions on each machine in turn from a
backgrounded app. Record that each wake opened **that** machine's context and nothing narrower or wider,
and that a wake whose target had disappeared opened that machine's inbox with an honest explanation
(ADR-018 MM5).

---

## J. Remote launch — `PH-LAUNCH`

Blocking: **R5**.

**`[UNRUN]` PH-LAUNCH-1. Launch from a machine-authored preset, over cellular.** For each supported
provider, choose a preset and an initial prompt on the phone, confirm, and record: the confirmation
sheet showed machine, provider, resolved workspace display path, worktree behavior and initial-prompt
presence; one signed `session_launch` was created; the session appeared **immediately** in the machine's
local list; and the terminal **attached to the same session**.

**`[UNRUN]` PH-LAUNCH-2. Stale preset.** Change the preset revision on the machine between the phone
listing it and confirming it. Record the `stale_preset` refusal, named in the six-state delivery
vocabulary, and that **nothing launched** under the old policy.

**`[UNRUN]` PH-LAUNCH-3. Disallowed root.** Attempt a launch whose resolved path escapes the machine's
allowed root — including via a symlink. Record the refusal and that it happened **before argv
composition**, and that the shim received the same fully-resolved real path on the allowed case.

**`[UNRUN]` PH-LAUNCH-4. Disallowed options.** Attempt `dangerously-skip-permissions` and any
full-access option. Record the hard-coded refusal, and record that the phone had no route to supply
argv, environment variables or an arbitrary path at all — a refusal is good, an absent route is better,
and the row records which one you found.

**`[UNRUN]` PH-LAUNCH-5. Crash recovery around reservation and spawn.** Inject faults around the
two-phase reservation and the spawn — kill the daemon before reservation, between reservation and
spawn, and after spawn but before acknowledgement. Record that **at most one process** existed in every
case, and that the phone's result was authoritative or `outcome_unknown`, never a silent retry that
spawned a second process (`playbook:783-785`).

**`[UNRUN]` PH-LAUNCH-6. Refusal matrix.** Record a refusal, with its stable code, for each of:
read-only authorization tier; read+approve tier; kill switch off; offline target machine; unknown preset
id. Each must refuse **before** any argv composition.

**`[UNRUN]` PH-LAUNCH-7. Convergence both ways.** Record that a terminal-launched session appeared
remotely with no new pairing, and that a remotely launched session was attachable from the terminal.
Record also that an **exited** session offered a policy-checked new launch rather than a guessed
provider resume (`playbook:228-229`).

---

## K. Structured chat, co-presence, approvals — `PH-CHAT`

Blocking: **R6** for Claude rows, **R7** for Codex rows; `PH-CHAT-3/4/5` need both.

**`[UNRUN]` PH-CHAT-1. Claude exit demonstration, on hardware.** Run `playbook:620-622` end to end:
start through Swarm, type from the terminal, continue from the phone while tools run, approve from
either side, background the app, receive a **real** wake, reopen into the exact card, switch networks,
and finish from the terminal with no session replacement. Record each leg separately; a single verdict
for the whole demonstration is not a record.

**`[UNRUN]` PH-CHAT-2. Codex, two clients on one thread.** Terminal TUI and phone drive the same Codex
thread concurrently. Record that item events reached both, that neither stole nor mutated the other's
ownership, that token deltas and tool states arrived within the §10 budgets, and that no Codex semantic
operation was implemented by a keystroke into the PTY.

**`[UNRUN]` PH-CHAT-3. Claude and Codex co-presence.** A live Claude session and a live Codex session on
the same machine, both visible on the phone, both attached in the terminal. Record that each session's
capability record named its own provider and detected version, and that the two did not interfere.

**`[UNRUN]` PH-CHAT-4. First answer wins — phone first.** Answer an approval on the phone. Record that
the terminal's card resolved and dismissed, that the resolution was attributed to `phone`, and that
`Approve` was presented as "the machine applied the decision", not as "the tool completed".

**`[UNRUN]` PH-CHAT-5. First answer wins — terminal first.** Answer the same class of approval in the
terminal while the phone's card is visible. Record that the phone's card dismissed, that attribution was
`owner`, and that a late tap on the phone was **refused** rather than applied twice. Record that the
phone never authored an approving keystroke on either path.

**`[UNRUN]` PH-CHAT-6. The six delivery states, observed.** Reach `draft`, `pending`, `sent`, `refused`,
`uncertain` and `outcome_unknown` at least once each on hardware, and record how each was produced.
Record that `uncertain` resolved through `operation_status` and that `outcome_unknown` was **never**
auto-retried.

**`[UNRUN]` PH-CHAT-7. One shell across every availability state.** On one healthy, one offline, one
ended and one no-chat session, record that opening the inbox row always produced the same normal
transcript and pinned composer shell. Record that `AVAILABLE` sent, while `OFFLINE`, `ENDED` and
`NO_CHAT` kept the field and action visible but disabled with the correct inline reason. Create a
visible history gap, then freshly re-prove the exact current sink: sending becomes available again
without removing the gap marker. At no point may a destination value, menu, gesture or developer
setting route Android to terminal view/control or a replacement status-card screen.

---

## L. Retired Android terminal fallback — `PH-FALL`

**PH-FALL-1..7 RETIRED ON ANDROID (2026-08-30).** These rows used to require a production terminal
renderer, watcher and control/input ceremony on a handset. Running them now would require restoring
the product route this release removes, so they are not `UNRUN`, not `not observable`, and not part of
the handset discharge count. Their machine/wire compatibility contract remains: versioned terminal
view fields, verbs, sanitization, authorization, horizons, session-instance binding and live-only
input continue to have Go protocol/core coverage for rolling and non-Android consumers.

The Android hardware obligation moved into `PH-CHAT-7`: every capability and lifecycle state keeps
one conversation shell, unavailable states explain themselves inline, and no handset path issues a
terminal watch, renders a grid, enters terminal control or sends raw terminal input.

---

## M. The result sheets — fill these in; do not summarise them

**A verdict without these tables filled in is not a discharge of this gate.** `not run`,
`not executable — R<n>`, and `not observable on release build` are legitimate entries. **A blank is
not.**

### Devices

| | Device A (current) | Device B (oldest supported) |
|---|---|---|
| Make / model | | |
| API level / Android release / build | | |
| Security patch level | | |
| ABI / RAM / StrongBox present | | |
| Carrier / radio | | |
| Clock offset vs machine, and method | | |

### Artifacts under test

| | |
|---|---|
| App: package, versionCode, track (closed / bootstrap sideload) | |
| Play App Signing certificate fingerprint | |
| App SHA / machine SHA / relay SHA / gateway SHA | |
| Firebase project id + sender id (production, not debug) | |
| Push gateway base URL + version + region | |
| Relays: `relay-org` and `relay-alt` URL, TLS policy, issuer, region | |
| Machines M1 / M2 / M3: OS, relay, `push_transport` | |
| Operator name, dates | |

### Keystore per alias (`PH-KEY-1`) — one row per alias the build actually provisions

| Alias (as provisioned) | securityLevel constant | strongBoxBacked | userAuthRequired | invalidatedByEnrollment | Device |
|---|---|---|---|---|---|
| | | | | | |

### Capability probe (`PH-KEY-3`)

| Capability | PRESENT / ABSENT / UNKNOWN | Consumed by a shipped row? | Provisioning refused? |
|---|---|---|---|
| | | | |

### Push topology (`PH-PUSH-1..3`)

| | Observed |
|---|---|
| Installation id (opaque) | |
| Play Integrity / App Check verdict, and behavior on failure | |
| `push_address` per pairing — M1 / M2 / M3, all distinct? | |
| Submit and machine-revoke capabilities distinct, absent from every readable log? | |
| `WakeV1` size on the wire (expected 74) | |
| Cleartext fields observed in the payload | |
| Locator fields observed (must be none, even encrypted) | |
| FCM priority / TTL / collapse key / data-only | |

### Delivery distributions — one block per device, never merged

| Condition | Samples | p50 | p95 | Failures / timeouts | Doze state achieved |
|---|---|---|---|---|---|
| Foreground Wi-Fi (`PH-NET-1`, ≥200) | | | | | — |
| Foreground cellular (`PH-NET-2`, ≥200) | | | | | — |
| Normal background (`PH-PUSH-5`, ≥50) | | | | | — |
| Doze (`PH-PUSH-6`, ≥20) | | | | | `IDLE` / `IDLE_MAINTENANCE` |
| App standby rare / restricted (`PH-PUSH-7`, ≥20 each) | | | | | — |
| Battery saver (`PH-PUSH-8`, ≥20) | | | | | — |

### Multi-machine independence (`PH-MM-2`) — one row per item, per pairing

| Item | M1 | M2 | M3 | How observed |
|---|---|---|---|---|
| Device signing key / recipient key | | | | |
| Epoch id / content key | | | | |
| Send-seq space / inbound high-water | | | | |
| Relay URL / relay-auth key / routing id / cursor | | | | |
| Operation-id space / journal | | | | |
| State directory namespace | | | | |
| Push address / wake key / submit cap / machine-revoke cap | | | | |

### Provider capability records (`PH-CHAT`; retired `PH-FALL` fields may still be recorded as wire evidence)

| Session | Provider | Detected version | `structured_chat` | `terminal_fallback` | interrupt / steer / approvals / history |
|---|---|---|---|---|---|
| | | | | | |

### Row ledger — one row per test id above

**The single row below is a template, not an entry.** It is expanded at execution time into **88
rows**, one per active test id in §A–§K in document order (`PH-DEV-1..9`, `PH-PAIR-1..9`,
`PH-LIFE-1..7`, `PH-PUSH-1..17`, `PH-LOCK-1..5`, `PH-NET-1..6`, `PH-RESIL-1..4`, `PH-KEY-1..8`,
`PH-MM-1..9`, `PH-LAUNCH-1..7`, `PH-CHAT-1..7`). §O.3's "no blanks" rule is checked against the
expanded active ledger; the retired §L identifiers are not blank rows, and the elided range below
is not itself an entry.

| Test id | Device A: what the device reported | Device B: what the device reported | Verdict (`pass` / `finding` / `not run` / `not executable — R<n>` / `not observable`) |
|---|---|---|---|
| `PH-DEV-1` … `PH-CHAT-7` (expand to one row per active id) | | | |

---

## N. Findings this gate carries in, so the operator meets them as expectations

Recorded now, from source and from the decision records, so that none of them is mistaken for a
discovery or for a gate failure.

1. **The gateway does not exist yet.** ADR-015 is a design commitment with no measurement behind it:
   "Not one wake has left this repository toward Google" (`ADR-015:196`). Every §D and §E row is
   `not executable — R3` until R3 ships, and R3's own exit is physical-handset-only.
2. **The five-minute wake expiry equals the FCM TTL by design** (ADR-015 P8 delta 3), which is why
   `PH-PUSH-13`'s expiry leg may be unreachable through FCM and why `PH-PUSH-17`'s clock-skew failure is
   now easier to reach than it was at ten minutes.
3. **`WakeV1` discloses the opaque push address in the clear**, which the old zeroed key-id fields did
   not, and it survives epoch rotation. Under §I's three pairings, one handset presents three addresses
   against one FCM token, which lets the gateway and Google count and separate that handset's machines
   (ADR-015 P8 delta 1, and its Negative consequence). `PH-PUSH-3` confirms the disclosure; it is not a
   defect to be reported as one.
4. **The machine-silence wake has no transport.** ADR-015 P10 removed it deliberately; laptop sleep is
   discovered on reconciliation. `PH-RESIL-1` records that the UI does not promise otherwise.
5. **Two revocation paths, two credentials, two failure stories.** Phone-side forget uses the
   installation key; machine-side revoke uses the machine-revoke capability and owes a durable deletion
   across the process exit that epoch rotation causes (ADR-015 P6). `PH-PUSH-16` is where a
   memory-only obligation is caught.
6. **A shared relay learns co-ownership.** Two pairings on `relay-org` put two routing ids behind one
   handset, so that relay can infer the machines are co-owned. ADR-018 records this as "a designed
   disclosure, not an incident" (`ADR-018:123`). §I creates it on purpose.
7. **The multi-machine migration is the highest-risk item in R4 and it runs on handsets in the field**
   (ADR-018's Negative consequences). `PH-KEY-5` and `PH-MM-8` exist for its one unrecoverable failure:
   two live send sequencers for one pairing.
8. **Android trust roots are not Go's.** `/system/etc/security/cacerts` is stale or empty on modern
   handsets, which is why ADR-016 W2 delegates the trust decision to Kotlin while Go keeps the name
   check. `PH-PAIR-7` is the only place that mechanism meets a real certificate chain on a real device.
9. **Android owns no terminal-control horizon.** The fifteen-minute value and synchronous-severance
   rules remain machine/wire compatibility obligations for non-Android consumers, verified in Go;
   retired `PH-FALL-5/6` must not be resurrected as handset release rows. `PH-CHAT-7` instead proves
   that Android never acquires this authority and keeps one conversation shell.
10. **`am force-stop` is now inside this gate** (`PH-LIFE-6`), where the legacy document explicitly
    excluded it. The matrix requires it to prove a **negative** — no promised wake until manual reopen —
    which is a different obligation from the one an emulator could discharge.

---

## O. Discharging the gate

The gate is discharged when **all** of the following are true:

1. Every `[UNRUN]` tag above has been removed by the operator who performed that row, on hardware,
   against the Play-signed closed-track build.
2. Every row has been run on **both** API levels, except rows marked `[ONE-DEVICE]`, which record which
   device was used.
3. §M's tables are complete with no blanks, the row ledger among them expanded to its 88 active rows.
   `not run`, `not executable — R<n>` and `not observable on release build` are entries; blanks are
   not.
4. The result is committed under `docs/verification/` with the operator named, the dates recorded, and
   every SHA from §M's artifact table.
5. Findings are filed as tracked work, not summarised in prose.

**A partial run discharges nothing, and a section that is not executable is not a waiver.** The
playbook's R9 exit is unconditional: "every release gate in section 11 passes and no P0/P1 residual is
waived by prose" (`playbook:842`). ADR-007's Accepted status names this gate as one of its open gates
(B144(c)), and it stays open until this document is executed.

**On the legacy requirement id.** B144(c) names the open gate as "the physical-handset gate /
PB-E2E-5" in one breath, and `docs/specifications/remote-phaseB-requirements.md` still carries
PB-E2E-5's own acceptance criteria against the Phase-B topology. **This document does not rule on
whether executing it discharges PB-E2E-5**, because the requirement's text is not this file's to
amend and its subject matter no longer matches: PB-E2E-5 was written about real biometrics, a
relay-held FCM sender and a single pairing, three of which the product no longer has. Until the owner
rules, treat them as two records with one execution: running this gate is what B144(c)'s open gate
asks for, and any PB-E2E-5 clause that survives the topology change must be pointed at a test id
above rather than run separately.

Rows that fail are a **result**, not a blocked gate: record what failed, file it, and the gate is
discharged as *run with findings*. The one outcome that is not permitted is a gate reported as passed
on rows that still carry their `[UNRUN]` tag.
