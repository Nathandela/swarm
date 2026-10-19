# OpenCode minimum version and redeployment status

The owner selected **stable OpenCode >=1.19.3**, replacing the exact-version
admission policy. Implementation is committed at `d86cb92d`; a private candidate
binary is built and tested. Full release qualification remains open. Installed
Swarm 0.18.2 and OpenCode 1.19.3 were not replaced.

All launch, bridge bootstrap/current-view, readiness, health, receipt recovery,
selection, history and doctor gates use the stable minimum. Adapter detection
has the same floor and no upper cap. Prereleases, build metadata, malformed
versions and older versions are refused. Native binding, capability, composer,
operation custody and executable fingerprint checks remain independent.
History provenance records the observed runtime version; native historical
`info.version` is preserved separately. Account pools and automatic native
worker replacement remain disabled.

TDD exposed the old future-version refusals before implementation. Connected
Go race checks passed in five packages: 396 test/subtest pass events, zero
failures or skips. All ten adapter package suites passed with race detection;
all-package build/vet and Linux-target lint passed, with zero lint issues. The
bridge suite passed 162 tests. Three Python regression methods cover the full
duration plan, invalid old intervals, minimum-version cases and argument checks
that create no workers or run directories. Independent architecture review
found no actionable issue in the frozen change.

Actual OpenCode 1.19.3 runs passed with both max and default fixture profiles.
Each executed three genuine inputs, native child work, public permission and
question controls, exact receipts/history, and two daemon crash recoveries.
They used nine scripted loopback requests each, zero external-model requests,
unchanged native configuration and confirmed owned-shim cleanup. Runtime was
24.476 seconds for max and 22.487 seconds for default, including cleanup.

The upstream latest release checked on 2026-10-19 is still
[1.19.3](https://github.com/anomalyco/opencode/releases/tag/v1.19.3).
Future patch/minor/major versions are fixture-tested, not yet live-qualified.
See the [machine-readable gate record](opencode-implementation-2026-10-18/minimum-version-2026-10-19.json).

The previous final-source one-hour workload passed 224 inputs and 11 daemon
recoveries. Its next stage never launched because the queue requested an
interval above the fixture's cap. The corrected queue preflights **every**
planned command before starting; its new frozen candidate is running the
1h -> 24h -> 72h sequence. This is running qualification, not an elapsed-duration
pass. See [overnight evidence](opencode-implementation-2026-10-18/final-hour-2026-10-19.json)
and [current queue monitoring](opencode-implementation-2026-10-18/sustained-qualification.md).

Actual 24h/72h, physical phone/hosted relay and the full real-model interoperability
matrix remain open. Prior complete Linux/Android gates describe the earlier
frozen source; this follow-up records the checks rerun for the version policy.
Existing unrelated Darwin baseline failures remain unchanged. No release tag,
remote publication, installation or deployment was performed.

Upgrade inspection against installed release v0.18.2 found no shimwire or owner
protocol version bump, so no protocol-driven session drain is introduced here.
New managed OpenCode sessions persist schema3; v0.18.2 supports only schema2.
Its older reader cannot reopen those records, and the existing upgrade/rollback
schema guard refuses an incompatible downgrade. Preserve native history and
work before any explicit rollback; no schema downgrade was attempted.
