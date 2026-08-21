# Committee fix wave -- Task B: fence gaps (Opus H2, M5, bead 65bj/M4, L4 + stale comments)

Date: 2026-08-21. Branch main at b688097 (uncommitted working tree). All mutation runs
below follow the fence protocol: `cp` backup, mutate production, run the fence (must
FAIL), restore from backup, `cmp` byte-verify, re-run (must PASS).

## 1. Opus H2 -- behavioural fence for the parked-control CodeNotImplemented arm

New test: `internal/protocol/r8r4_nosink_behaviour_test.go`
(`TestR8R4Parked_NoSinkInputRefusedWithoutAnyDelivery`). Drives the real assembled
remote-tier server (serveRemoteAPIStableSrv) over a backend with capability record,
device registry and kill switch but NO TerminalInput method; mints a live generation via
terminal_control_begin; sends terminal_input; asserts op=error code=op_not_implemented
AND zero Attach/SessionStream/Launch/Kill/Delete calls at the backend.

RED (mutation-first, which is this fence's failing-first run): backed up
`internal/protocol/remote_terminal.go` to /tmp/remote_terminal.go.bak, mutated the
`sink, ok := cc.srv.d.(TerminalInputSink); if !ok` arm to reroute the bytes around the
interface (Attach + SessionStream.Input + Close + replyOK -- exactly the evasion Opus H2
named). Run:

    --- FAIL: TestR8R4Parked_NoSinkInputRefusedWithoutAnyDelivery (0.02s)
        r8r4_nosink_behaviour_test.go:112: terminal_input on a sink-less backend = op "ok" code "" error ""; want op "error" code "op_not_implemented".
    FAIL    github.com/Nathandela/swarm/internal/protocol   0.952s

(The same mutation also trips the zero-Attach assertion; the reply assertion fires
first.) Restore: `cmp` returned identical (RESTORE-BYTE-VERIFIED), then:

    ok      github.com/Nathandela/swarm/internal/protocol   0.760s

The interface-level ledger row (internal/verify/r8r4_parkedcontrol_test.go) is
unchanged; this fence is its behavioural complement.

## 2. Opus M5 -- relay-container.yml gates asserted as parsed step INPUTS

Rewrote `TestRelayContainerWorkflowWiresTheGates` in `deploy/relay/hardening_test.go`:
the workflow is now unmarshalled with gopkg.in/yaml.v3 (added to go.mod; v3.0.1 was
already pinned in go.sum) and the assertions are structural: exactly one ENABLED job
carries an aquasecurity/trivy-action step; that step is not disabled (`if:` handling for
bool/expression false); its `exit-code` input is "1", `severity` is "HIGH,CRITICAL",
`image-ref` is set, `trivyignores` is deploy/relay/.trivyignore (file stat kept); the
build step (docker build -f deploy/relay/Dockerfile) precedes the scan; an enabled step
after the build runs `docker inspect` on `.Config.User` and has a failing path
(`exit 1`); an enabled step re-asserts `read_only` over `docker compose config`. Every
fact the old substring test checked is retained, strengthened; the Dockerfile and
docker-compose tests in the same file are untouched.

Mutations (backup /tmp/relay-container.yml.bak):

  a. `exit-code: '1'` -> `'0'`:
        --- FAIL: TestRelayContainerWorkflowWiresTheGates
            hardening_test.go:201: trivy exit-code input = "0", want "1" (a scan that cannot fail the job gates nothing)
     restore cmp-verified (RESTORE-BYTE-VERIFIED), suite green.
  b. `if: false` on the trivy step:
        --- FAIL: TestRelayContainerWorkflowWiresTheGates
            hardening_test.go:192: the trivy step is disabled (if: false)
     restore cmp-verified (RESTORE-BYTE-VERIFIED); `go test ./deploy/relay/` ok.

## 3. Bead 65bj / Opus M4 -- the five R8 wire keys pinned on BOTH declarations + parity

Changes:
  - `internal/phonecore/snapshot_test.go` -- `TestSnapshotFrame_WireShape` extended: it
    still pins the legacy four-key plaintext byte-exactly, and now ALSO pins the frame
    with all five sibling keys (session_instance, view_epoch, revision, reset,
    rendered_at) populated, byte-exactly. No existing assertion touched.
  - `internal/remotegw/terminal_out_test.go` -- new
    `TestRelaySink_ForwardsVersionedTerminalSnapshot`: the real RelaySink seals a fully
    populated TerminalViewV1; plaintext pinned byte-exactly to the SAME five-key JSON.
  - `internal/phonecore/snapshot_parity_test.go` -- new cross-package fence
    `TestSnapshotWireParity_GatewayMarshalMatchesPhoneFrame`: identical data through
    BOTH independent snapshotFrame declarations (real remotegw.RelaySink seal ->
    crypto.OpenMailbox vs phonecore's own marshal) must be byte-identical; on drift it
    names the drifted key(s) before failing on the bytes.
  - Production comments corrected: relaysink.go (was: the legacy pin "pins these exact
    bytes") and snapshot.go (was: WireShape "pins on this exact plaintext") now name the
    legacy pin, the five-key pin and the parity fence.

Mutation A (gateway side): backup /tmp/relaysink.go.bak; tag `view_epoch` ->
`viewepoch` in relaysink.go:

    --- FAIL: TestRelaySink_ForwardsVersionedTerminalSnapshot (plaintext byte mismatch)
    --- FAIL: TestSnapshotWireParity_GatewayMarshalMatchesPhoneFrame
        gateway ...: {"...","viewepoch":3,...}  phone ...: {"...","view_epoch":3,...}

Restore cmp-verified (RESTORE-A-BYTE-VERIFIED).

Mutation B (phone side): backup /tmp/snapshot.go.bak; tag `view_epoch` -> `viewepoch`
in phonecore/snapshot.go:

    --- FAIL: TestSnapshotFrame_WireShape (versioned snapshot wire shape mismatch)
    --- FAIL: TestSnapshotWireParity_GatewayMarshalMatchesPhoneFrame
        key "view_epoch": sealed by the gateway, unknown to the phone frame (tag drift)
        key "viewepoch": expected by the phone frame, never sealed by the gateway (tag drift)

Restore cmp-verified (RESTORE-B-BYTE-VERIFIED); both packages re-run ok.

CLOSE-WORTHY: yes. Both declarations now carry byte-exact pins over all five keys, the
parity fence ties them together, and a tag drift on either side fails two tests naming
the exact key. The blast radius Opus M4 proved (view_epoch reading 0 disabling the
revision-monotonicity guard) is fenced at the wire.

## 4. Stale comments (committee-verified false; no fence -- comment-only corrections)

  - `internal/skeleton/capability.go` (sessionDegradedFile doc, was ~:104): no longer
    claims "nothing in production calls deriveSessionCapabilities yet". It now states
    the R8 reality already recorded at the :289 routing note: authorSessionCapabilities
    is reached from five production seams, a live session normally HAS a record, and the
    degrade-before-record case survives only in the backendPlaneDecided dialling window
    -- which is exactly why the marker still exists separately.
  - `internal/protocol/server.go` (Opus L4), three sites:
      * maxPeekCols/maxPeekRows const doc: the frame carries the clipped grid TWICE
        since R8 (Terminal + TerminalView); an oversized frame is rejected by WriteFrame
        with wire.ErrFrameTooLarge and the write-error arm cancels the render loop --
        NOT "silently dropped". True headroom stated: worst case ~2 x 200x300 runes at
        ~6 escaped bytes, ~0.7 MiB against the 1 MiB wire.MaxFrame.
      * the clipPeek call-site comment in the peek emission path: same correction.
      * the clipPeek function doc: two bodies, true numbers, true consequence.

## Gates (after all edits, mutations restored)

    go build ./...                                    OK
    go vet ./...                                      OK
    PATH=$HOME/go/bin:$PATH golangci-lint run         0 issues
    go test -race -count=1 ./internal/protocol/ ./internal/phonecore/
        ./internal/remotegw/ ./deploy/relay/ ./internal/skeleton/     all ok
    go test -count=1 -run TestR8R4Parked_TheControlSinkHasNoProductionImplementation
        ./internal/verify/                            ok (interface ledger row unaffected)

go.mod/go.sum: gopkg.in/yaml.v3 v3.0.1 added as a direct requirement (was already
pinned in go.sum); `go mod tidy` run, diff is exactly those two lines.

Process hygiene: 26 orphaned (ppid==1) test-spawned `swarm shim` processes found after
the race runs (14 from this session's skeleton run, 12 leftovers from earlier runs);
all killed by explicit pid, zero remaining.

Nothing staged, nothing committed.
