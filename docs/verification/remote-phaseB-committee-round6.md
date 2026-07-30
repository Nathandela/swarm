# Phase B audit committee — round 6

**Convened** 2026-07-30 against the round-5-remediated tree. **Verdict: REVISE.**
**Split 2–1 against a closed test in the form proposed. Unanimous that production is not ready.**

> **CORRECTION, filed after this document was first written.** The first version of this synthesis
> recorded the fences member as having **returned nothing**, and declared the closed-test verdict
> **unanimous**. Both statements were false when written. The report arrived after the synthesis and
> is complete: eleven ranked findings, six refutations, every one marked RAN or READ. **Its verdict
> is YES to a closed test after two small non-security fixes.** So there is no unanimity, the
> blind-spot section below was wrong about its own coverage, and **a synthesis written before its
> last member reported is a synthesis that invented a consensus.** Recorded rather than silently
> rewritten, because the failure mode — declaring agreement from silence — is the one this committee
> exists to prevent.

## Members and emphases

| Member | Emphasis | Verdict | Returned |
|---|---|---|---|
| **GPT-5.6 sol (codex)**, external | Open | Not a tester-distribution candidate; production no | Full report, every finding mutation-proven |
| **Reachability / dead-code reviewer** | Requirements pointed at nothing | Closed test no; production no | Built the residual-4.25 instrument, then refuted its own specification |
| **Timeout remediation** | The round's CRITICAL | — (implementer, not voter) | Fix landed with RED-first evidence |
| **Compositions and fences** | New fences, composition | **Closed test YES after two fixes**; production not close | 11 ranked findings, 6 refutations, all RAN or READ |

**The fences member dissents on the closed test, and its dissent is the best-evidenced position in
the round.** It mutated the direction-binding fix four ways and found all four fences genuinely RED
3/3 — including the two that matter most, where the check is moved *after* `Accept`, since the whole
fix rests on "a refusal that has already advanced the high-water is not a refusal." **The round's
CRITICAL from round 5 is not merely closed but fenced on the property that matters**, which is rare
in this record.

Its two blocking items are **not security**: an owner told `"internal"` while the handset reads
`paired`, and an ack leg cut short by the relay's rendezvous deadline after a slow SAS compare. Both
hit real humans in week one; both are a few lines.

## The headline: the count fell from 142 to 133, and I authored most of the errors

**Six requirements were falsely marked shipped at the start of this round**, on top of the three
found at the end of round 5. Every one was found by someone other than the row's author, and **four
of the nine were closed by my own adjudications or restatements.**

| Requirement | Why it was false | Found by |
|---|---|---|
| `PB-NET-7` | relay.Client had no timeout of any kind | round 5 → fixed this round, **still not met** (B99) |
| `PB-NET-4` | §6.0's backoff exists only in dead code | my adjudication B90, refuted |
| `PB-E2E-3` | restated down to what happened to be true | my restatement B93, refuted |
| `PB-PAIR-4` | the ack attests arrival, not durable commit | codex |
| `PB-PUSH-3` | fence asserts size; requirement asks for schema | codex |
| `PB-SEC-2` | per-prompt identity fixed one site, not the class | codex |
| `PB-NET-3` | entire fence inside a package with no production caller | reachability reviewer |
| `PB-NET-6` | hostile-pagination clause has no live subject at all | reachability reviewer |
| `PB-NET-5` | **my own refutation of it was wrong** (B100) | reachability reviewer |

**133 of 144. 9 NOT MET. 2 hardware-deferred.**

## Consensus

1. **A CRITICAL, now closed.** A silent relay wedged the phone's entire outbound plane: no timeout
   anywhere in `relay.Client`, `roundtrip` holding `c.mu` across write-then-read, every shipped phone
   call site passing `context.Background()`. **The relay is the declared adversary and this was the
   cheapest move it had — do nothing.** A half-open TCP after a network handoff produced it by
   accident. Fixed at `23d1dc1` with RED at `c2b7eb5`; the RED is worth reading, because the test
   binary *could not terminate* and dumped the wedge as a stack trace through `App.Kill`.

2. **Requirements fenced against code with no production caller is the defining defect of this
   project.** It now accounts for four confirmed rows and one refuted suspicion. `unused` cannot see
   the class **by construction** — it does not flag exported identifiers — so a fully dead exported
   subsystem is invisible to the standard toolchain.

3. **Durable relay state is still economically unbounded on a public relay**, unchanged from round 5,
   both debated remedies still measured insufficient.

4. **The suite is not deterministically green under load.** Confirmed independently by two members
   and by both-arms sampling: `TestPresence_TransitionsAndSilentPush` fails 11/20 without the
   round's change and 9/20 with it — a pre-existing flake, not a regression.

## What this round taught the committee

**A seventh instrument**, and the first found by chasing the *lowest*-ranked finding to ground:

> **The fence is built, mutation-proven, and never armed in the lane that runs.**
> *Tell:* a flag, strict mode or optional pass whose only caller is its own test.
> *Fix:* arm it where the lane runs. A test proving a mode works is not evidence the mode is used.

`--strict-section11` existed, had a mutation proof that it rejects an incomplete table by name, and
**CI ran the checker without it.** The readable ownership rows then drifted for two days while the
evidence file went on claiming *"default and `--strict-section11`: both exit 0"* — **fossil evidence
manufactured by an unarmed check.** The two instruments compose: a check nothing runs cannot keep its
own evidence honest.

**And a correction to residual 4.25, which is the more useful artifact than the residual.** I proposed
a ~30-line test — "every package named in an evidence file is reachable from a `main`" — as the
instrument that would have caught all four dead-object requirements. **It passes on the very package
that motivated it.** `cmd/swarm-remote` does reach `internal/remote/transport`, via two batching
helpers the gateway uses. Package-level reachability cannot see a dead symbol in a live package,
which is the actual shape of all four. **Implemented literally it would have been the fifteenth fence
here that cannot fail, authored by me one message after naming the class.**

The working formulation is symbol-level reachability over a typed call graph: ~470 lines, not 30, with
a bidirectional ledger so an exemption that becomes reachable *fails*. It honestly records that it
would **not** have caught B90 — a live one-hop reference into a dead subgraph is invisible to it.

## Divergence — recorded, not resolved

**On the closed test, round 6 has no dissent, and that is a change from round 5.** The external
reviewer's round-5 objection was tree hygiene; this round it is defects, and the other members'
findings independently reached the same bar.

**On deleting the dead code, the committee split with itself.** The obvious remedy — delete
`internal/remote/transport` — turns out to **remove a live control over live code**: `relay/client.go`
names that package's `productiondial_test.go` as the enforcement that no production caller reaches
`relay.Dial`, and four of its ten test files carry only live references. **A wholesale delete would
have passed every gate while silently un-fencing `PB-NET-2`.** The B94 class running backwards. The
deletion is deferred behind relocation and replacement fences.

## Blind spots

- **Coverage is 31 of 144 rows deep-derived (22%).** Every tranche anyone re-derives still produces a
  finding.
- ~~No member covered compositions or new fences this round.~~ **Wrong when written** — see the
  correction at the head of this file. That axis was covered, and it produced the round's CRITICAL
  (a fourth push producer the enumeration cannot see) plus two Android gates that hold a *token*
  rather than a property.
- **No Android or gomobile attack-surface work**, in any round, by any member. Now six rounds old.
- **The denominator is still unproven.** Round 5 found a *missing* requirement; nothing this round
  looked for another, and re-deriving existing rows cannot find one.
- **Nobody has measured the phone's echo latency.** The shipped phone polls inbound at 500 ms and the
  numeric criterion stops at the PTY write, so *typing* is measured and fast while *seeing your own
  character come back* is unmeasured by construction.

## Verdict: REVISE

**Closed test on a private, owner-operated relay: NO, 2–1**, in the form proposed. The
critical wedge is closed, which was the first condition, but `PB-PAIR-4` (a pairing window that
leaves the machine enrolled while the phone holds nothing), `PB-SEC-2` (a biometric prompt outside
the ledger that can mint an authorization after invalidation) and the 500 ms echo path are all
first-session experiences.

**An owner-attended bring-up on the owner's own handset is reasonable now and was not before.** That
distinction is the round's practical result.

**Production: unanimous no**, unchanged.

## The trajectory, stated honestly

Round 5 found less that was new and more that was structural, and read as convergence. **Round 6
found more, not less** — the count fell nine — and the reason is that this round finally pointed an
instrument at the class the previous five rounds had only ever found by hand. **The defects were
always there; the tooling to see them is three days old.**

The uncomfortable part is the authorship. **Four of the nine false rows were closed by my own
adjudications**, and the round's last finding was my own refutation being overturned — by instrument
5, the dropped quantifier, which I catalogued in round 5 and then failed to apply to my own work one
round later. **The pattern is not that the reviewers are finding my errors. It is that I close rows
on partial reads and only an adversary reads the rest of the row.**
