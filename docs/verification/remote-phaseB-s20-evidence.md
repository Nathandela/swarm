# S20 evidence — closure, runbooks, and the certificate pin

**Requirements owned: PB-DOC-2, PB-DOC-3, PB-DOC-4, PB-DOC-7, PB-OPS-1, PB-OPS-2, PB-OPS-3,
PB-OPS-5.** Eight, all of which this file names.

**Uncommitted and unstaged**, per the slice brief. Nothing in `docs/specifications/`,
`docs/adr/` or `android/` was touched; two amendments those files need are **drafted here** in
§9 and §10 for their owner to apply.

| | |
|---|---|
| `go build ./...` | green |
| `go vet ./...` | green |
| `~/go/bin/golangci-lint run --max-same-issues=0 --max-issues-per-linter=0 ./...` | **0 findings** |
| `go test ./...` | see §8 — one **deliberate** red, which is PB-DOC-7 doing its job |

---

## 1. What is tested and what is executed — the split, stated first

| Requirement | Kind | Artifact |
|---|---|---|
| **PB-OPS-5** | **Tested**, RED first | `internal/remote/transport/pin_renewal_test.go` (6 tests), `internal/remote/relay/security.go` |
| **PB-DOC-7** | **Tested**, RED first | `internal/verify/phaseb_manifest_test.go` (13 negative controls + 1 positive + strict), `scripts/check-phaseb-manifest.py`, `.github/workflows/ci.yml` |
| **PB-DOC-2** | **Tested**, *green on arrival* — stated plainly in §4 | `internal/verify/phaseb_coverage_test.go` |
| **PB-OPS-1** | **Executed once**, transcript in §5 | `docs/operations/relay-runbook.md`, `scripts/relay-tls-terminator.py` |
| **PB-OPS-2** | **Executed once per step**, transcript in §6 | `docs/operations/operator-runbook.md` |
| **PB-OPS-3** | **Authored**; its ADR half is drafted for its owner | `docs/operations/metadata-disclosure.md` + §9 |
| **PB-DOC-3** | **Authored** | `docs/verification/remote-phaseB-residuals.md` |
| **PB-DOC-4** | **Authored** | `docs/research/remote-v1-roadmap.md:282-307` |

---

## 2. PB-OPS-5 — the pin survives renewal, and the requirement's own claim needed qualifying

> *"The certificate pin must survive renewal. […] Pinning the SPKI hash rather than the full leaf
> DER survives renewal at the same security level."* Acceptance: *"Runbook states the renewal
> hazard; either the pin is SPKI-based or the operational consequence is documented and accepted."*

**The pin is now SPKI-based.** `relay.Security` gains `PinnedSPKISHA256`; either pin alone admits
the peer, both may be set, and a pin that is not 32 bytes is refused **before the dial** with
`ErrPinMalformed` rather than inside `VerifyPeerCertificate`, where a truncated pin would read as
"the relay is down" and a zero-padded one would silently weaken the check.

### 2a. FAILING FIRST (verbatim)

```
$ go test -run 'TestPBOPS5' -count=1 ./internal/remote/transport/
# github.com/Nathandela/swarm/internal/remote/transport_test [github.com/Nathandela/swarm/internal/remote/transport.test]
internal/remote/transport/pin_renewal_test.go:150:18: unknown field PinnedSPKISHA256 in struct literal of type relay.Security
internal/remote/transport/pin_renewal_test.go:169:18: unknown field PinnedSPKISHA256 in struct literal of type relay.Security
internal/remote/transport/pin_renewal_test.go:193:18: unknown field PinnedSPKISHA256 in struct literal of type relay.Security
internal/remote/transport/pin_renewal_test.go:227:21: unknown field PinnedSPKISHA256 in struct literal of type relay.Security
internal/remote/transport/pin_renewal_test.go:229:54: unknown field PinnedSPKISHA256 in struct literal of type relay.Security
FAIL	github.com/Nathandela/swarm/internal/remote/transport [build failed]
FAIL
```

### 2b. GREEN (verbatim), including the pre-existing PB-NET-2 tests

```
--- PASS: TestPBOPS5_DERPinIsBrokenByRenewal (0.11s)
--- PASS: TestPBOPS5_SPKIPinSurvivesRenewalWithTheSameKey (0.06s)
--- PASS: TestPBOPS5_SPKIPinRefusesAnUnrelatedCertificate (0.03s)
--- PASS: TestPBOPS5_SPKIPinIsBrokenByARenewalThatROTATESTheKey (0.03s)
--- PASS: TestPBOPS5_ErrPinRequiredNamesTheRenewalSafeForm (0.00s)
--- PASS: TestPBOPS5_AnSPKIPinAloneSatisfiesTheAndroidPinRequirement (0.05s)
    --- PASS: .../DER_pin_only  --- PASS: .../both_pins  --- PASS: .../SPKI_pin_only
--- PASS: TestTLS_DefaultVerificationFailsClosedOnUntrustedCert (0.05s)
--- PASS: TestTLS_PinnedCertIsAnExplicitOptIn (0.03s)
--- PASS: TestTLS_PinAcceptsOnlyThePinnedCert (0.03s)
--- PASS: TestTLS_RedirectToCleartextIsRefused (0.03s)
ok  	github.com/Nathandela/swarm/internal/remote/transport	1.367s
```

Each test stands up a **real TLS terminator in front of the real relay** and dials it through
`relay.DialSecure`, so "survives renewal" is a completed relay-auth handshake against a reissued
certificate, not a unit test of a comparison function.

### 2c. **THE REQUIREMENT'S CLAIM IS NOT ACCURATE AS WRITTEN, and this is a finding**

> *"Pinning the SPKI hash rather than the full leaf DER survives renewal at the same security
> level."*

True **only if the renewal reuses the key.** A fresh keypair is a fresh SubjectPublicKeyInfo, which
breaks an SPKI pin on exactly the 60-90 day cadence it was adopted to survive — and **certbot
generates a fresh keypair on every renewal by default** (`--reuse-key` / `reuse_key = True` opt
out of that). An SPKI pin is a **necessary half** of the fix, not the fix.

I refused to repeat the unqualified claim. It is pinned in the opposite direction by
`TestPBOPS5_SPKIPinIsBrokenByARenewalThatROTATESTheKey`, which **asserts the pin FAILS** on a
rekeyed renewal, and the key-reuse step is §8b of the relay runbook. **Suggested amendment to
PB-OPS-5 is in §10.**

**NOT VERIFIED HERE:** the statement about certbot's default is about its documented behaviour, not
a run against Let's Encrypt. There is no ACME account, no public DNS name and no handset in this
project. What was executed is the reissue-and-recompare loop against a self-signed certificate
(§5) and the six tests above.

### 2d. The ADR note the brief asked me to propose

Changing the pin format is security-relevant. Draft entry in §9b.

---

## 3. PB-DOC-7 — the checker could not fail, and now it can, and it found two live defects

> *"A test parses §11 and the requirement tables and fails on any unowned id, duplicate owner,
> wildcard, dangling edge, or cycle."*

### 3a. What was wrong before this slice

`scripts/check-phaseb-manifest.py` exits 0 on this repository and has since round 5. Two clauses of
the requirement were **not enforced at all**:

1. **It never read §11.** It read `remote-phaseB-manifest.tsv`. §11 is where the wildcard defect
   PB-DOC-7 names actually lived — v3 gave S19 and S20 the dependency `"all"`, which read literally
   made each depend on the other — and nothing that reads only the TSVs can see it. The spec itself
   carries this as unclosed hygiene: *"§11's readable table not cross-checked against the
   authoritative manifest"*.
2. **It read the repository unconditionally**, taking no root argument. So the negative control the
   spec credits it with (*"verified against a negative control (deleting a row fails the run)"*)
   was a thing a human did once by hand, and **no automated mutation test was even possible.**

### 3b. FAILING FIRST (verbatim, abridged — all 13 controls failed identically)

```
$ go test -run 'TestPBDOC7' -count=1 ./internal/verify/
--- FAIL: TestPBDOC7_EveryEnforcedClauseCanFail (2.84s)
    --- FAIL: .../unowned_id (0.22s)
        phaseb_manifest_test.go:218: PB-DOC-7: the checker ACCEPTED a tree with a unowned id:
            slices: 29 (27 on the S19 exit path, terminal: ['S20', 'S21'])
            spec: 143 active requirements (1 withdrawn) | manifest: 143 owned
            manifest OK: every requirement owned exactly once
    --- FAIL: .../duplicate_owner            [same output]
    --- FAIL: .../manifest_names_an_id_the_spec_does_not_define
    --- FAIL: .../manifest_owns_a_withdrawn_requirement
    --- FAIL: .../manifest_names_an_unknown_slice
    --- FAIL: .../dangling_edge
    --- FAIL: .../cycle
    --- FAIL: .../orphan_slice
    --- FAIL: .../wildcard_ownership_in_section_11's_requirements_column
    --- FAIL: .../wildcard_dependency_in_section_11_(the_v3_S19/S20_defect)
    ...
```

Read the diagnostic: the checker **printed its success verdict on a tree with a deleted ownership
row**, because it never looked at the tree it was handed. That is the failing-first run.

### 3c. **THE HEADLINE FINDING: §11 assigns three requirements to the wrong slices, via wildcards**

With §11 parsed, the checker rejects the repository:

```
$ python3 scripts/check-phaseb-manifest.py
slices: 29 (27 on the S19 exit path, terminal: ['S20', 'S21'])
spec: 143 active requirements (1 withdrawn) | manifest: 143 owned
section 11: 27 readable rows cross-checked (contradictions only; --strict-section11 also requires completeness)
S11REQ    S4 claims PB-LIFE-7, which the manifest gives to S4b
S11REQ    S5 claims PB-TOK-1, which the manifest gives to S16
S11REQ    S5 claims PB-TOK-4, which the manifest gives to S13
EXIT=1
```

**These are not parser artefacts. They are the wildcard defect PB-DOC-7 prohibits, happening now.**
§11's S4 row owns `PB-LIFE-*` and its S5 row owns `PB-TOK-*`. Both families later had members
reassigned — PB-LIFE-7 to S4b, PB-TOK-1 to S16, and PB-TOK-4 to S13 *by the S5 reviewer, with the
spec's own PB-TOK-4 row reading "Owned by S13"* — and the wildcards silently re-claimed them. A
reader of §11 is told S5 owns the requirement that exists because S5's own review moved it away.

Seven family wildcards are live in §11 (`PB-LIFE-*`, `PB-TOK-*`, `PB-INPUT-*`, `PB-TIME-*`,
`PB-TOOL-*`, `PB-RUN-*`, `PB-APP-*`). Two of the seven are already wrong. **Recommendation: enumerate
all seven.** The minimum fix for the contradiction is two cells (§10).

### 3d. The negative controls are non-vacuous — proven against an amended §11

Because the repository currently fails, "the checker rejected the mutation" would be weak evidence
on its own. So the 13 controls plus the strict control were re-run against a **temporary copy with
the two-cell §11 amendment applied**, with the unmutated copy as the positive control:

```
POSITIVE CONTROL (amended section 11, unmutated): exit 0 -> PASS
  unowned        exit=1 token=yes -> PASS      wildcard-req   exit=1 token=yes -> PASS
  multiown       exit=1 token=yes -> PASS      wildcard-dep   exit=1 token=yes -> PASS
  phantom        exit=1 token=yes -> PASS      s11req         exit=1 token=yes -> PASS
  withdrawn      exit=1 token=yes -> PASS      s11dep         exit=1 token=yes -> PASS
  badslice       exit=1 token=yes -> PASS      s11slice       exit=1 token=yes -> PASS
  dangling       exit=1 token=yes -> PASS      strict-omission exit=1        -> PASS
  cycle          exit=1 token=yes -> PASS
  orphan         exit=1 token=yes -> PASS
```

Each control asserts the **specific diagnostic token**, not merely a nonzero exit — otherwise a
checker that rejected everything for one reason would satisfy all thirteen.

### 3e. Validated in CI

`.github/workflows/ci.yml`'s `docs` job gains
`python3 scripts/check-phaseb-manifest.py`. The requirement asks for acyclicity "validated in CI"
specifically; the checker existed for two rounds with nothing running it on push.

### 3f. `--strict-section11`, and why it is not the default

§11 states its own contract — *"The slice table above is the readable view; the manifest is the
source of truth"* — so an **omission** there is drift while a **contradiction** is the readable
table lying. The default rejects contradictions. `--strict-section11` also demands completeness and
is how whoever amends §11 knows when they are finished:

```
$ python3 scripts/check-phaseb-manifest.py --strict-section11
S11REQ    S4 claims PB-LIFE-7, which the manifest gives to S4b
S11REQ    S5 claims PB-TOK-1, which the manifest gives to S16
S11REQ    S5 claims PB-TOK-4, which the manifest gives to S13
S11MISS   S10 omits PB-KEY-10
S11MISS   S13 omits PB-TOK-4
S11MISS   S14 omits the dependency edge(s) S14a
S11MISS   S14a has no section 11 row at all
S11MISS   S16 omits PB-TOK-1
S11MISS   S16 omits the dependency edge(s) S14
S11MISS   S17 omits the dependency edge(s) S15
S11MISS   S18 omits the dependency edge(s) S4
S11MISS   S19 omits the dependency edge(s) S4b, S6b
S11MISS   S20 omits PB-OPS-5
S11MISS   S4b has no section 11 row at all
S11MISS   S7 omits the dependency edge(s) S1b
```

**Two slices that own requirements — S4b (PB-LIFE-7) and S14a (PB-KEY-9) — appear nowhere in §11.**
S20's own row omits PB-OPS-5, which §10 of the spec separately calls a phantom id that was deleted;
it was not deleted, it is in the manifest, and it is one of the eight requirements this slice
delivers.

---

## 4. PB-DOC-2 — full ID coverage, and this one was GREEN ON ARRIVAL

> *"Phase B exit criteria in `implementation-goals.md`; a verification file maps every PB-* ID to
> evidence."* Acceptance: *"Full ID coverage."*

**First half: already delivered before this slice** — `### Epic 15` in
`docs/specifications/implementation-goals.md`, criteria E15.1-E15.8. Not mine, not touched.

**Second half: verified rather than assumed.** `docs/verification/remote-phaseB-traceability.md` is
generated and covers all 143 active requirements. I confirmed it is **in sync** with its generator
(regenerating produces a byte-identical file), which nothing previously checked.

**`internal/verify/phaseb_coverage_test.go` passed the first time it ran.** Stating that plainly,
because E15.3 says a file asserting a RED it does not have is worse than one admitting the test was
green on arrival:

```
--- PASS: TestPBDOC2_EveryActiveRequirementIsInTheTraceabilityIndex (0.01s)
--- PASS: TestPBDOC2_TheCoverageRuleRejectsAnIncompleteIndex (0.00s)
    --- PASS: .../a_requirement_with_no_row_is_missing
    --- PASS: .../a_row_for_an_undefined_id_is_a_phantom
    --- PASS: .../a_withdrawn_requirement_is_not_owed_a_row
--- PASS: TestPBDOC2_PhaseBExitCriteriaAreStatedInImplementationGoals (0.00s)
```

**It is nonetheless capable of failing, and is shown failing.** The coverage rule is a pure function
of the two texts specifically so that it can be run on inputs constructed to break it: a spec id
with no row is reported missing, a row for an undefined id is reported as a phantom, and a withdrawn
requirement is correctly owed nothing. A guard over checked-in documentation that has only ever seen
documentation satisfying it has not been tested.

Non-overlap with the two neighbouring guards, since three guards over one table is how a gap
survives all three: the manifest checker compares spec ↔ **manifest** and never opens the generated
document; PB-E2E-3's fence iterates the rows that **exist** and is blind by construction to a
requirement with no row; this compares spec ↔ **generated document**.

**One thing PB-DOC-2 needs that is not mine to assert:** `scripts/phaseb-traceability.py`'s
`SHIPPED` list stops at S18b, so PB-E2E-1..4 read *pending*. S19 has landed (`de2b612`) and has an
evidence file. Adding `"S19"` (and later `"S20"`) is the orchestrator's assertion — the script's own
comment says only the orchestrator knows whether a slice was gated — so I have not made it. **E15.1
requires shipped == evidenced at close; that will not hold until this is updated and the file
regenerated.**

---

## 5. PB-OPS-1 — executed once, with the artifact

> Acceptance: *"Runbook executed once with an artifact as evidence."*

Runbook: `docs/operations/relay-runbook.md`. Every numbered step was run on 2026-07-26.

**The relay serves plain `ws://` only** — `Server.Start` sets `s.url = "ws://"+addr` and
`Config.TLSMode` is read by nothing that serves — so a LAN handset cannot reach it under PB-NET-2's
cleartext refusal without a terminator. `scripts/relay-tls-terminator.py` is a ~90-line stdlib
TLS-to-TCP pipe written so the runbook is executable on a machine with nothing installed. It is
marked **not for production** in its own package comment; production termination is Phase C.

```
[1] built swarm-relay
[2] issued a 90-day self-signed certificate
subject=CN=swarm-relay.local
notBefore=Jul 26 01:43:16 2026 GMT
notAfter=Oct 24 01:43:16 2026 GMT
[3] SPKI pin (base64 sha256): YKlHE2H7izUKGLWZx8q4pfgbJoWU5nTVZHKlP8k3ex8=
[4] wrote relay.json
[5] relay pid 22735, terminator pid 22736
terminator: wss://127.0.0.1:9443 -> ws://127.0.0.1:9440
swarm-rel 22735 Nathan    6u  IPv4 ...      0t0  TCP 127.0.0.1:9440 (LISTEN)
Python    22736 Nathan    3u  IPv4 ...      0t0  TCP 127.0.0.1:9443 (LISTEN)

[6] the certificate the live endpoint actually serves:
subject=CN=swarm-relay.local
notBefore=Jul 26 01:43:16 2026 GMT
notAfter=Oct 24 01:43:16 2026 GMT
sha256 Fingerprint=E0:58:9A:9E:35:3D:BF:C7:F6:DE:2E:57:A6:17:47:CB:40:AF:F2:D7:60:2E:95:0D:61:BB:57:F3:F1:C4:0E:03

[7] recompute the pin FROM THE LIVE ENDPOINT and compare with the recorded one:
    live:     YKlHE2H7izUKGLWZx8q4pfgbJoWU5nTVZHKlP8k3ex8=
    recorded: YKlHE2H7izUKGLWZx8q4pfgbJoWU5nTVZHKlP8k3ex8=
    MATCH -- this is the value the phone pins

[8] websocket upgrade through the terminator:
    negotiated: TLSv1.3 TLS_AES_256_GCM_SHA384
    HTTP/1.1 101 Switching Protocols
    websocket upgrade through the terminator reached the relay: True

[9] the append quota must not be tightened below the shipped default:
    configured mailbox_append_per_min = 600, shipped default = 600
    OK

[10] tear down
    relay log:
    (empty log = clean run)
```

Step 8 is the load-bearing one: a TLS handshake proves the terminator is up and says nothing about
the relay behind it. `101 Switching Protocols` with a correct `Sec-WebSocket-Accept` proves bytes
reach the real relay and come back.

Step 9 discharges the spec's §6.0 clause that PB-OPS-1 must require the demonstration relay's
configured quota to be ≥ the default: `mailbox_append_per_min` is the **only** cap that applies to
live typing (`ops_per_min` explicitly excludes `mailbox_append`), and a lowered value breaks typing
with nothing reporting a quota problem where a user would see it.

---

## 6. PB-OPS-2 — each step executed once

> Acceptance: *"Each step executed once during verification."*

Runbook: `docs/operations/operator-runbook.md`. Install, pair, revoke, kill switch, device loss,
push configuration — all six.

```
[1] install, WITHOUT swarm-remote on PATH:
$ swarm remote init
remote init: no gateway supervision unit installed (exec: "swarm-remote": executable file not
found in $PATH); the gateway will not start on its own
machineid.Identity{... epoch:1/1}
$ echo $?
0

[1a] install, WITH swarm-remote on PATH:
$ swarm remote init
machineid.Identity{... epoch:1/1}
$ find "$SWARM_DAEMON_STATE" -type f
state/remote/machine.key
state/remote/units/com.swarm.remote.plist

[2] status before pairing:
configuration: initialized (identity; no relay configured)
remote control: OFF (device-derived; no devices paired)
paired devices (0):

[3] after writing relay.json:
configuration: initialized (identity + relay)

[4] kill switch:
$ swarm remote off  -> remote control disabled   (exit 0)
$ swarm remote on   -> remote control enabled    (exit 0)

[5] device loss / revoke:
$ swarm remote revoke deadbeefdeadbeef
revoked device deadbeefdeadbeef
run `swarm remote pair` to pair a device again
  exit=0
$ swarm remote regrant deadbeefdeadbeef
remote regrant: device_regrant: skeleton: no such device "deadbeefdeadbeef"; nothing to re-grant
  exit=1
  (epoch after the bogus revoke: still 1/1 -- nothing rotated)

[6] devices:
DEVICE ID  NAME  CAPABILITY  PAIRED AT      (empty)

[7] push configuration, credential UNSET:
  relay booted, empty log        (supported: no push transport, PB-PUSH-5)

[8] push configuration, credential SET but unreadable:
swarm-relay: read push credentials: open /nonexistent/fcm.json: no such file or directory
  exit=1        (fails closed, as PB-PUSH-5 requires)

[9] the paired-device flow, since there is no handset:
$ go test -run '^TestPBE2E1_PairObserveLaunchTakeControlTypeRevoke$' -count=1 -v ./internal/skeleton/
--- PASS: TestPBE2E1_PairObserveLaunchTakeControlTypeRevoke (17.60s)
ok  	github.com/Nathandela/swarm/internal/skeleton	18.593s
```

### Three operator-facing defects found by executing the runbook

1. **`swarm remote revoke` reports success for a device id that was never paired**, exit 0, naming
   the id you typed. Verified benign — the machine epoch does not rotate — but during a device-loss
   incident a mistyped id produces exactly the output that says the lost phone is cut off.
   `regrant` refuses the same id properly, so the asymmetry is in `revoke`. Runbook mitigates
   (always confirm with `swarm remote devices`); the behaviour is **open**.
2. **`swarm remote init` warns and exits 0** when `swarm-remote` is absent from `PATH`, installing
   no supervision unit. A scripted install checking the exit status records success for a machine
   whose gateway will never start.
3. **Every `swarm remote` verb starts a daemon if none is running** (`dialClient` →
   `daemon.EnsureDaemon`). There is no "connection refused" to tell you that you are pointed at the
   wrong state directory — you get a fresh daemon there instead.

**NOT EXECUTED HERE, said at the step in the runbook rather than in a footnote:** scanning the
pairing QR with a camera; real biometrics; real FCM delivery (no Google account, no Firebase
project, no `google-services.json` — the sender has **never run against Google**); Doze; hardware
Keystore attestation. PB-E2E-5 stays deferred and an emulator is not a handset.

---

## 7. PB-OPS-3 and PB-DOC-3 and PB-DOC-4

**PB-OPS-3** — `docs/operations/metadata-disclosure.md`. Covers the relay operator (routing ids,
presence timing, sizes and cadence, per-item sender rid, bounded retention) and the push provider
(token, timing, constant 78-byte size), consistent with PB-PUSH-3 and ADR-007 D11 and B20. It
carries the finding the brief named: **a persisted push token is a new durable device identifier at
rest in the untrusted relay's store, correlatable with the mailbox by routing id, and it is the
same identifier the push provider holds — a join key between two parties whose views are otherwise
disjoint.**

> **PB-OPS-3 is NOT fully substantiated by this slice.** Its acceptance criterion is literally
> *"ADR section consistent with PB-PUSH-3 and ADR-007 D11"*, and `docs/adr/` is outside my
> boundary. §9a is the section text; the requirement closes when it is merged.

**PB-DOC-3** — `docs/verification/remote-phaseB-residuals.md`. Consolidates the 24 per-slice
`## Accepted residuals` sections, the progress ledger's open debt, and ADR-007's B-entries into one
document classified by adversary reachability (R relay/network, D handset data, O owner/local
fault, N no adversary), each with what, why, verdict, status and source. **Three entries I could
not settle are listed as such in its §6** rather than defaulted to the reassuring answer.

**PB-DOC-4** — `docs/research/remote-v1-roadmap.md:282-307`. The line is amended, not silently
contradicted, and the amendment reports the discrepancy rather than smoothing it:

- roadmap:286 said *"Implementers are sonnet/opus subagents (never fable/haiku for this work)"*.
- §11 says *"opus and fable subagents only"* and assigns **fable** as implementer for S5, S13, S16,
  S17 and S20. **Two changes, not one: fable was admitted and sonnet was dropped.**
- **What actually happened cannot be checked from the tree**: nothing records per-slice model
  attribution — not the evidence files, not the commit trailers, not the manifest. §11's Model
  column is a plan.
- **And it demonstrably was not followed for at least one slice**: S20 is assigned "fable, opus
  review" and was implemented by opus. The rule worth carrying into Phase C is the verifiable one —
  independent agents per role, plus cross-model review.

---

## 8. Gates

```
go build ./...   green
go vet ./...     green
~/go/bin/golangci-lint run --max-same-issues=0 --max-issues-per-linter=0 ./...   0 findings
gofmt -l  (on the files this slice adds or edits)                                clean
```

`go test ./...` produced exactly two failures. None of the four known flags fired on this run:

```
--- FAIL: TestRunShim_LaunchesAgentPersistsAndLeadsSession (11.66s)
    role_test.go:112: shim pid 28477 never became its own session leader (getsid != pid) —
    setsid was not guaranteed; stderr:
FAIL	github.com/Nathandela/swarm/cmd/swarm	45.345s

--- FAIL: TestPBDOC7_TheRepositoryPasses (0.66s)
    [the three S11REQ findings of §3c]
FAIL	github.com/Nathandela/swarm/internal/verify	4.156s
```

**`TestRunShim_...` is a FIFTH load-dependent flake and it is not on the brief's known list.**
Reproduced only under full-suite load; `-count=3` in isolation passes (`ok 16.439s`). S20 touches
nothing under `cmd/swarm`. Recorded here and in the residuals file so it gets an owner rather than
being absorbed into "the suite is a bit flaky" — a fifth unowned flake is how the fourth stopped
being read.

**The second failure is deliberate: `TestPBDOC7_TheRepositoryPasses` is PB-DOC-7 working on its
first run.** §11 assigns three requirements to slices the manifest gives to someone else (§3c).
**I am boundaried out of `docs/specifications/`, so I cannot clear it.** The two-cell amendment in
§10a turns it green — verified by running the checker and all 14 controls against a copy with the
amendment applied (§3d). Shipping the check disabled instead would have been shipping the defect,
which is the one thing PB-DOC-7 exists to prevent.

`android/gate`'s PB-E2E-2 is another agent's and was not run here.

---

## 9. Drafted for the owner of `docs/adr/` — I did not write these into the ADR

### 9a. PB-OPS-3's ADR-007 section

> ### B32 — Metadata disclosure, restated because push persistence added to it
>
> D11 documents what the relay and the push provider observe and forbids claiming less exposure
> than exists. Two things changed in Phase B and the disclosure must move with them.
>
> **The relay's at-rest footprint gained a durable device identifier.** PB-PUSH-6 made push tokens
> survive a relay restart, so `bucketTokens` now holds a provider-issued, long-lived,
> device-specific identifier **in the clear** — it cannot be encrypted, because the relay must hand
> it to the provider. It is keyed by routing id, which is the same key that indexes the mailbox, so
> it is directly correlatable with that handset's message cadence and presence history. It is also
> **the same identifier the push provider holds**, which makes it a join key between two parties
> whose views are otherwise disjoint. Deletion is as durable as registration (same transaction as
> the revocation), and it lives in its own named bucket rather than in the item log so an operator
> can audit every device identifier in one place — an auditability property, not a
> confidentiality one. Fenced by `TestPBPUSH6_TokenIsNotStoredInTheClearAlongsideTheCiphertext`.
>
> **The provider's view is now what PB-PUSH-3 claims, because B20 made it so.** Key ids zeroed, a
> constant 78-byte payload, an empty plaintext. What remains and is not fixed by an empty payload:
> a token plus wake timing is an **activity trace**. The content is hidden; the rhythm is not.
> D11's honesty rule requires saying so.
>
> The operator-facing form is `docs/operations/metadata-disclosure.md` and the two must not
> diverge.

### 9b. PB-OPS-5's ADR-007 section

> ### B33 — The relay pin is over the SPKI, and key reuse is part of the pin
>
> `Security.PinnedCert` compares the full leaf DER. Android's trust-root source is
> `TrustRootsPinned`, so the pin is the whole of relay TLS verification there, and a reissue —
> which changes the serial and validity window even when nothing else changes — takes every paired
> handset offline. On the Let's Encrypt cadence that is every 60-90 days.
>
> **Decision**: `Security` gains `PinnedSPKISHA256`, SHA-256 over the presented certificate's
> `RawSubjectPublicKeyInfo`. Either pin alone admits the peer; both may be set. Security level is
> unchanged — the digest admits exactly one public key — and a malformed pin is refused **before
> the dial** with `ErrPinMalformed` rather than inside `VerifyPeerCertificate`. `ErrPinRequired`
> now names the renewal-safe field first, because it is the only pin documentation most operators
> read.
>
> **Recorded, because the requirement's own wording glosses it**: an SPKI pin survives renewal only
> when the renewal **reuses the key**. certbot rotates the keypair by default. The runbook carries
> the `--reuse-key` step and
> `TestPBOPS5_SPKIPinIsBrokenByARenewalThatROTATESTheKey` asserts the pin failing on a rekeyed
> renewal, so the pairing cannot be silently dropped.
>
> **Not closed by this entry**: no production caller reaches `DialSecure`, so neither pin is
> applied on the shipping dial path (see the S20 evidence, and the residuals file §1.1). Carrying
> a pin to the handset also has no channel — the pairing QR has no pin field and no room for one at
> `MaxRelayURLLen = 39`.

---

## 10. Drafted for the owner of `docs/specifications/` — three amendments

### 10a. §11, minimum fix (clears the red gate)

```
- | S4 Gateway supervision (3 states) + release artifacts | PB-LIFE-*, PB-OPS-4 | opus | — |
+ | S4 Gateway supervision (3 states) + release artifacts | PB-LIFE-1..6, PB-OPS-4 | opus | — |

- | S5 Design tokens | PB-TOK-* | fable | — |
+ | S5 Design tokens | PB-TOK-2, 3 | fable | — |
```

Verified: with these two cells changed, `python3 scripts/check-phaseb-manifest.py` exits 0 and all
14 mutation controls pass against it.

### 10b. §11, full fix (`--strict-section11` clean)

Additionally: enumerate the remaining five family wildcards; add rows for **S4b** (PB-LIFE-7) and
**S14a** (PB-KEY-9); add PB-KEY-10 to S10, PB-TOK-4 to S13, PB-TOK-1 to S16, **PB-OPS-5 to S20**;
add the dependency edges S1b→S7, S14a→S14, S14→S16, S15→S17, S4→S18, and S4b, S6b→S19. The exact
list is the `--strict-section11` output in §3f. Separately, §10's traceability row reading *"v3's
PB-OPS-5 was deleted by the §6.18 scope correction; this row pointed at a phantom id"* is **stale**
— PB-OPS-5 is defined at line 624, owned by S20 in the manifest, and delivered by this slice.

### 10c. PB-OPS-5's text

> Pinning the **SPKI hash** rather than the full leaf DER survives renewal at the same security
> level.

suggested:

> Pinning the **SPKI hash** rather than the full leaf DER survives renewal at the same security
> level **provided the renewal reuses the key**. Most ACME clients generate a fresh keypair per
> renewal by default (certbot needs `--reuse-key`), and a fresh key is a fresh SPKI, which breaks
> an SPKI pin exactly as a reissue breaks a DER pin — so the pin format and the key-reuse
> requirement are one decision, and the runbook must carry both.

---

## 11. What I could not substantiate, stated as prominently as what I could

| Requirement | Status |
|---|---|
| PB-OPS-5 | **Delivered**, with the requirement's own claim corrected (§2c) and an ADR note owed (§9b) |
| PB-OPS-1 | **Delivered and executed** (§5) |
| PB-OPS-2 | **Delivered and executed** (§6), with three operator-facing defects found and one still open |
| PB-DOC-3 | **Delivered** |
| PB-DOC-4 | **Delivered**, and the discrepancy reported rather than resolved in prose |
| PB-DOC-2 | **Delivered and verified**, but E15.1 will not hold until `SHIPPED` is updated (§4) — not mine to assert |
| **PB-DOC-7** | **Implementation delivered; the requirement is NOT SATISFIED at this commit.** Its own check finds a live wildcard-ownership defect in §11, which I am boundaried out of fixing. §10a closes it. |
| **PB-OPS-3** | **Document delivered; the requirement is NOT SATISFIED at this commit.** Its acceptance criterion names an ADR section and `docs/adr/` is outside my boundary. §9a closes it. |

**One finding that belongs to no requirement of mine and outranks most of them:** the entire
PB-NET-2 transport-security policy — the pin, the cleartext refusal, the redirect re-check — has
**no production caller**. `mobile/relay.go` dials with `relay.Dial`, `mobile/pairing.go` with
`relay.DialRaw`, `cmd/swarm-remote/main.go` with `relay.Dial`; no non-test file constructs a
`relay.Security` at all. The handset therefore applies no transport policy: a `ws://` URL from a
pairing QR runs in cleartext with nothing refusing it. The SPKI pin this slice adds is correct and
tested and **is not yet on the path a phone takes.** Recorded in the residuals file §1.1 and in
both runbooks. It needs an owner and it is not S20's to fix — carrying a pin to the handset has no
channel today (§9b).
