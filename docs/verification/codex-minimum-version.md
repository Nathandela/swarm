# Codex minimum version and identity readiness

Date: 2026-10-13. Baseline installed release: v0.17.6. Implementation target: v0.17.7.

## Minimum-version policy

The previous implementation admitted exactly Codex 0.160.0 and could select its
retained copy after the owner installed a newer release. The corrected policy
admits stable Codex 0.160.0 and later and uses the installed executable. A missing
installation refuses execution instead of silently downgrading to a retained
binary. Version checks retain executable ownership and identity validation.

Old retained executables remain intact for running processes and compatibility
inspection. Claude's retained-runtime policy was unchanged by this Codex version
correction. Protocol and shim wire versions were unchanged.

## Regression and gate evidence

Regression cases covered the minimum boundary, later patch/minor/major versions,
malformed and prerelease rejection, following an npm upgrade, no old-cache
fallback, refusal below the minimum, and rollback compatibility. Automatic
recovery also refused to move a newer running Codex session to an older installed
CLI; equal versions and forward upgrades remained allowed.

The initial policy commit `fda6e464` passed [complete PR CI 38381090281](https://github.com/Nathandela/swarm/actions/runs/38381090281),
including the full race suite. Build, vet and lint passed locally. The first full
normal suite hit temporary-directory exhaustion and sandbox access restrictions,
and exposed stale fixtures that still considered 0.161.0 unsupported. Those
fixtures were updated to versions below the minimum. The full first local suite
was not reported green; complete CI gates remained the merge and release boundary.

## Native identity observation

A read-only check found two running discussions without a recorded conversation
ID or post-spawn observation. Their native thread IDs were independently found,
but native startup occurred 51 seconds after Swarm reserved each discussion while
the unknown-ID resolver accepts only a 30-second window. Automatic identity
association was not fabricated and the window was not widened; explicit identity
recovery was tracked in `swarm-e8j`.
