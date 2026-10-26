# Mobile terminal touch planning verification

**Historical planning evidence.** The implementation, independent reviews, final local gates and remaining handset qualification boundary are recorded in [implementation verification](implementation.md) and [qualification.json](qualification.json). The observations below describe the preimplementation baseline.

Date: 2026-10-25. Baseline: `fb20750f8bc6d30b5f3379618712a58e62b9e2f0` on macOS. Scope: plan validation for the [mobile terminal dashboard interaction plan](../../specifications/mobile-terminal-touch-plan.md). No production behavior changed.

## Executable probes

[probes_test.go.txt](probes_test.go.txt) is Go test source retained as text so it is excluded from ordinary package discovery. A Go overlay temporarily adds it to `internal/tui`, reusing the actual current model and existing fake client/attach runner. The normal TUI TestMain supervisor still owns scratch and cleanup. No live agent, daemon or SSH connection is used.

These tests assert observed baseline gaps. Their passing result confirms reproduction, not feature correctness. Implementation should reverse the applicable cases in real acceptance tests; do not preserve an assertion that a defect must continue to exist.

Reproduce these historical gaps from an isolated checkout at `4b275c0a`, which contains both the retained probes and their original model/test helpers. They intentionally do not compile against the implemented board. From that checkout's repository root:

```bash
touch_overlay=$(mktemp /tmp/swarm-touch-plan.XXXXXX)
trap 'rm -f "$touch_overlay"' EXIT
python3 - "$touch_overlay" "$PWD" <<'PY'
import json, pathlib, sys
root = pathlib.Path(sys.argv[2])
with open(sys.argv[1], 'w') as f:
    json.dump({'Replace': {
        str(root / 'internal/tui/touch_plan_probe_test.go'):
        str(root / 'docs/verification/mobile-terminal-touch/probes_test.go.txt')
    }}, f)
PY
go test -overlay "$touch_overlay" ./internal/tui -run '^TestTouchPlanProbe_' -count=1 -v
```

Use the existing shared Go caches. The test supervisor sets per-run TMPDIR for the test executable; the overlay manifest is a small temporary build input. The retained source is intentionally coupled to this baseline's test helpers.

The first eight probes passed on their first execution, package runtime 2.438 seconds including the test supervisor. A ninth probe then checked actual renderer output. All nine passed together in 2.349 seconds; [captured output](probe-output.txt) records that final retained-source execution. These package times are not a phone performance measurement.

| Probe | Observation | Consequence for the plan |
|---|---|---|
| Offscreen selection | At 120×8, 15 wheel reports selected `endpoint/15`; its row was absent; Enter attached it | Independent viewport plus reveal-first guard for every contextual command |
| Narrow fit | At 24/32 columns the row occupied 36 cells; at 36 name/path/summary budgets were all zero; at 40 name=1/path=3 | Compact information hierarchy and tests for useful identity, not only width |
| Resize during press | Selected-row press, resize to 72×12, release at the same Y still attached | Geometry-changing resize cancels armed actions |
| Drag away and back | Left-button motion was ignored; release back on the row attached | Route actual drag motion to gesture cancellation |
| Delayed pre-press | Old frame showed A at Y; a name reorder moved selected B there; the delayed tap opened B | Model geometry does not certify the phone-visible frame; retain explicit limit |
| Form clipping | At 40×8 the focused prompt and validation error existed in Launch but neither was visible | Form focus, error and decision context need a height contract |
| Selected provider | At 40 columns, selecting OpenCode at the end of the agent list produced picker text `◂ ○ cl` | Reuse the selected-only compact picker fallback |
| Delta coalescing | In a 0..10 scroll range, down100 then up1 ended at9; summing first ended at10 | Coalesce paint requests; preserve sequential input/clamping semantics |
| Renderer output | A manual tick with identical visible content retransmitted the idle-session marker in 316 captured renderer bytes | Test actual output when removing forced clears; a View-string test misses the mechanism |

The gesture probes call actual model Update paths; they do not decode real phone touch. The delayed-coordinate probe constructs an old-frame sequence and demonstrates ambiguity, not its incidence on a real connection. The wheel-coalescing probe is a pure counterexample, not a replacement scroll implementation. The renderer probe uses Bubble Tea's real test renderer with Init suppressed to isolate a manually delivered repaint tick; it proves retransmission, not exact on-wire SSH size or full-screen terminal painting cost.

## Independent challenges

The owner requested Sol and Terra. The available subagent runtime did not offer Terra; the owner explicitly selected Astra as the replacement. Reviewers ran as `gpt-6.1-sol` and `gpt-6-astra`, independently from fresh contexts. Neither edited product source or the tracker.

### Sol first round

Sol identified all selected-session callers, header-focus loss through `selectedID()` refreshes, More-menu target rebinding, repo basename identity, hidden narrow-screen safety markers, and the gap between dashboard touch and keyboard-driven forms.

The plan resolves these with a single contextual-action visibility guard; semantic session/header focus preserved through apply/remove/tag/regroup/reconcile; a captured menu session ID; unchanged existing grouping identity; compact diagnostic priority; and an explicit keyboard-form release boundary.

Sol compared two collapse options. Keeping all hidden sessions in keyboard navigation would save state but reopen folded groups while browsing. The chosen design makes only collapsed headers keyboard stops, leaves expanded headers out of the stops, and expands to the first member. Per-group selection history and persisted collapse are omitted.

### Astra first round

Astra challenged the assumption that shared layout proves displayed-frame targeting, collapse attention discoverability, offscreen session commands, form disclosure/error visibility, and terminal-wide capture changes.

The plan states the pre-press transport ambiguity, binds armed actions to identity and geometry without renderer acknowledgments, adds a persistent attention aggregate with a reveal action, covers every contextual command, requires target/decision/error context, and keeps free-text forms outside capture.

### Sol second round

Sol judged the design coherent and appropriately scoped, conditional on five concrete clarifications. The plan now specifies touch-operated More entries with their own scroll routing; named captured-menu context as the visibility guard's input; at least one navigable line and explicit minimum-size categories; a visible confirmation stop after a long decision; and all non-Enter mutation callers. Successful asynchronous launch/resume autoattach is an explicit completion exception to size gating.

The suggested cuts were adopted where they preserve the deliverable: one-line items need no within-item offset, collapse has no selection-history map, and disconnected local folding remains keyboard-driven with capture off. Page/Home/End navigation remains a small explicit keyboard enhancement, not a new navigation subsystem.

### Astra second round

Astra gave conditional approval with no architectural blocker, requesting three local clarifications: captured-menu visibility, minimum geometry with simultaneous notices, and duplicate/truncated-name identity. The plan resolves them with a named ID-bound menu context, mandatory remaining navigation space or a nonactivating fallback, and inspectable full target context before consequential commands. It also explicitly prevents an unselected-row tap's release from opening the row selected by its own press.

The remaining conditions belong to implementation/release: prove the proposed minima, future behavior, renderer savings and real-phone compatibility. They are not presented as results of the baseline probes.

### Final clarification check

Both reviewers reread the revised contract. Sol reported no remaining planning blocker and no unnecessary infrastructure. Astra reported the planning blockers resolved and the plan implementation-ready. Their approval is for the written plan; neither claimed implementation correctness, measured minima or handset qualification.

## Limits and future gates

The probes establish no physical touchscreen translation, font-dependent target size, SSH timing, tmux compatibility or attached-agent mode fidelity. The real iOS application remains unidentified. Historical Termius evidence in [ADR-029](../../adr/ADR-029-board-mouse-selection.md) does not qualify every current client.

Full product quality gates, race verification, actual renderer-byte measurements and handset trials remain implementation gates in the plan. Production code, native Android, adapter protocols and CI triggers were not changed by this planning task.
