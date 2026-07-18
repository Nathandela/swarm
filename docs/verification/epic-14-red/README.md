# Epic 14 — RED evidence (GG-5)

Epic 14 is a VERIFICATION epic: whole-system integration proof over already-shipped
Epics 1-13. Most of its tests are **characterization tests** — they assert
PRE-EXISTING behavior (invariants S1-S12/L1-L3, the 18 spec scenarios, EARS ids)
that earlier epics already implemented and already have their own retained
red-logs under this directory tree. Those tests have no meaningful "red phase" of
their own: they were green from the moment they were written, because the
behavior they check already existed. Claiming a reconstructed RED for them would
be dishonest, so this document does not attempt one.

A handful of Epic 14 tasks DID drive genuinely new code — new seams or new
ordering that did not exist before Epic 14. Those five have a real red phase, and
[seams-red.txt](seams-red.txt) reconstructs it for each:

1. **a7d — shim arm→spawn ordering** (`internal/shim/shim_signal_order_test.go`):
   the `testHookAfterSignalArm` seam is new; the ordering guarantee it proves
   (arm the signal handler before spawning the agent) is new production behavior.
2. **Disk-full injection** (`internal/persist/diskfull_test.go`): the
   `persist.writeTemp` package-var indirection is new — production code used to
   call `*os.File.Write` directly, with no fault-injection seam.
3. **Old-shim x new-daemon compat matrix** (`internal/daemon/compatmatrix_test.go`):
   the build-tagged `shimwire.Version` split (`version_compat_v0.go` /
   `version_compat_v2.go`) is new — it did not exist before Epic 14 and is what
   lets the matrix build and run real adjacent-version shim binaries.
4. **L1 composite / perf gates** (`internal/e2e/l1composite_e2e_test.go` and
   siblings: `firstpaint_gate_test.go`, `latency_gate_test.go`,
   `idlecpu_integration_test.go`): these chain EXISTING sub-behaviors (engine
   normalize, protocol fan-out, TUI render) into one new end-to-end assertion with
   a new hard budget. The gate itself — "does this fail closed when the number is
   exceeded" — is the new thing to prove.

**Conversation-capture is NOT a separate Epic 14 red phase.** The task brief
listed it alongside the four above, but `captureConversationID` and
`internal/e2e/capture_c1_e2e_test.go` were entirely authored in Epic 11 (commits
`8b2e4ac`, `8c9ae67`, `b933bee`, `381cc6a` — confirmed via `git log --oneline --
internal/e2e/capture_c1_e2e_test.go` and `-- internal/skeleton/serve.go`, both
showing only Epic 11 commits touching this code). Epic 14 does not add or change
capture code; it only continues to exercise it as part of the whole-system proof.
Its reconstructed RED is already recorded under
[../epic-11-red/status-red.txt](../epic-11-red/status-red.txt) (seam d,
`captureConversationID`) and is not duplicated here.

## Method

Each of the five seams above was TEMPORARILY neutralized on top of HEAD (main @
35b1fc0) to reproduce the pre-Epic-14 state, its test was run and the failure
captured verbatim into seams-red.txt, and the file was then restored via `git
checkout` — confirmed by an empty `git diff` on every touched file and a green
`go build ./... && go test ./internal/engine/ ./internal/persist/ ./internal/shim/
-count=1` afterward. No test file's assertions were weakened; the one test-file
edit (the L1 bound, tightened from 1s to 1ms to force the gate closed) was also
restored.
