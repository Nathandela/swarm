# Hermes native message ingress qualification

2026-10-27, macOS arm64. The activation-gated shared-owner Hermes runtime is wired through the daemon,
shim, semantic protocol, native status sampling, and saved-session resume.
The existing classic launch remains the path when activation is disabled or the
installed version is below v0.22.5. The guarded macOS shared-owner source gate
passed on 2026-10-27, followed by the equivalent Linux x86_64 source gate.
All-five release and installed-release-artifact qualification remain separate gates.

## Guarded shared-owner core qualification

[Machine-readable result](hermes-native-shared-owner-2026-10-27.json), macOS
arm64, Go 1.26.10, exact upstream v0.22.5 source
`bf0203a794a07aac3ff764972ab9087baa4e4f4e` with clean tracked source files.
The harness runs a private physical console-script copy, a prepared native TUI,
and a loopback OpenAI-wire provider with synthetic credentials. Native owner,
viewers, helpers and subprocesses inherit scratch-only OS write restrictions
and access only the explicit loopback provider/API ports and private Unix
sockets. Positive and negative OS guard checks run before native execution.
The original console script is checked unchanged after cleanup.

The public Swarm `Client.Launch` creates two arbitrary-name workers on one
already running normal native owner, with distinct canonical session IDs and
the actual `--tui` viewers. Public semantic sends and durable operation reads
produce only `submitted` receipts. Repeating each operation sends no second
native admission. The fake provider starts an actual background process in A,
another in B, and an actual terminal process in A's native asynchronous
delegated child. A subsequent A turn waits on the native tool approval for
`rm -f` of the fixture file, which the actual TUI renders.

Submitting literal `/approve all` through the public semantic endpoint leaves
the exact native permission pending, the file intact, and the external message
in the native queue. Restarting the Swarm daemon preserves owner PID/start and
creates no model turn. Public Kill A requests scoped native cancellation and
stops A's viewer. A's witnessed background and delegated-child process
incarnations disappear, while B's background process and the shared owner
survive. Public List reports A as `NativeWorkUnknown`/NeedsInput; Delete refuses
workspace removal, and B accepts a further semantic submission.

The real run exposed a client deadline race: the native stop budget and the
ordinary client timeout were both ten seconds. The client now reserves 45
seconds for Kill/Delete only, preserving the uncertain reply and usable control
connection. A deterministic delayed-reply regression was red before the fix
and green afterward; connected-client context cancellation remains unchanged.

Reproduction requires an isolated Python 3.14 environment with upstream Hermes
core requirements and `psutil`, prepared pinned TUI assets and its supported
Node runtime. Build `./cmd/swarm` and
`./scripts/verification/hermes_native_probe` into private temporary paths, then
run `scripts/verification/hermes_native_admission.py` with `--root`, `--source`,
`--hermes`, `--node`, `--swarm` and `--probe`. The source pin is enforced. Linux
must wrap the complete harness, fake provider and native processes in the
read-only mount/fresh loopback network namespace required by
`native_fixture_sandbox.py`.

These observations establish this fixture's scoped stop behavior, not an
authoritative native join or arbitrary descendant containment. Native list
projections omit async delegation records and pending process-scope cleanup;
interrupt acknowledgements and detach responses do not prove all writers
joined. `NativeWorkUnknown` therefore remains set and workspace cleanup remains
blocked even after these fixture processes stop.

Astra's review added exact receipt profile/session/authority-epoch checks,
canonical resume identity fencing, and a stop sequence that still requests every
observed queued cancellation and session-scoped background stop after unknown
admissions or native refusals. A failed stop and an ordinary viewer exit detach
without an end reason: ending the native scope could discard an unknown
admission and drain its queued successor. The stable native TUI signal cleanup
only closes its sockets; deliberate native UI quit remains provider-owned.
The guarded source run above was repeated successfully after these fixes.

## Linux shared-owner source qualification

[Linux result](hermes-native-shared-owner-linux-2026-10-27.json): PASS, Ubuntu
x86_64, Go 1.26.10 cross-compiled with CGO disabled, the same v0.22.5 source,
Python 3.14.2, Node 26.12.1 and npm 12.0.2. Upstream core dependencies and their
declared overrides were installed in an owned RAM directory; the TUI was built
from its locked dependencies. Runtime archives were checksum verified. The
source checkout and dependencies were mounted read-only outside the separate
writable runtime root. No global package install, native authentication or
production state change was required.

`scripts/verification/hermes_native_linux.sh` ran the entire harness, fake
provider, native owner, public Swarm daemon, TUI viewers and tool subprocesses
inside Bubblewrap's read-only filesystem and fresh user/PID/network namespaces.
Only loopback was enabled. The guard checked allowed scratch writes, PTY and
provider TCP, rejected outside-root writes and external TCP, and verified the
network namespace differs from its host. All macOS assertions above passed
again on Linux, and the complete namespace fixture exited zero after cleanup.

Two setup details surfaced on Linux. Source qualification needs the exact
upstream release tag as well as its peeled source commit; an untagged shallow
checkout reports a `vgit.*` version and correctly fails the stable floor. The
real stable `--version` command took 3.45 seconds in the guarded VM, exceeding
the ordinary three-second identity probe budget. Hermes now has a bounded
ten-second identity probe; other providers retain three seconds. Its delayed
CLI regression was red before the fix and green afterward, including race
checks. Hermes Kill/Delete still report uncertain completion where no join
proof exists; the longer detection budget changes no native approval policy.

## Historical native API and TUI qualification

The executable was the real upstream Hermes console entrypoint in an isolated
Python 3.14.2 environment. Both source pins passed against a loopback OpenAI-wire
fixture, with no production Hermes home, provider credential, Discord token,
or paid model call:

- stable v0.22.5:
  [`bf0203a794a07aac3ff764972ab9087baa4e4f4e`](https://github.com/NousResearch/hermes-agent/tree/bf0203a794a07aac3ff764972ab9087baa4e4f4e);
- current source:
  [`e5f2b15300a3d04978e4c07cc64adc7bcf8e7b1c`](https://github.com/NousResearch/hermes-agent/tree/e5f2b15300a3d04978e4c07cc64adc7bcf8e7b1c).

The TUI was built from each pin's locked dependencies. Node 26.12.1 and npm
12.0.2 were installed only under `/private/tmp`; their archives matched upstream
`pm/lock.json` SHA-256 values
`18830bf448fe187c650dc28c98aa8c968ed1d40b6dd032c081e70a384a1bf660` and
`5dbb86c71d07a1957f2e90734092dd6a58bdcd9ebc2d8d41ca1c6e6a21d364e1`.
The test Python environment installed upstream core requirements with its
declared Pillow/cryptography overrides; it was not a packaged Hermes install.

The initial source prototype started two independent private
foreground gateways through the native command:

```text
hermes gateway run --no-supervise --force --quiet
```

Each has its own `HERMES_HOME` and `HERMES_GATEWAY_LOCK_DIR`. Swarm connects to
the private control socket, verifies the owned PID/start time, protocol,
instance and single served home, and obtains a native interactive ticket. It
uses `hermes-gateway-v1` WebSocket RPC for `setup.status`, `session.create`,
`session.resume`, and `prompt.submit`. No terminal input is written.

The test then attaches the actual native TUI in a 160x45 PTY:

```text
hermes chat --tui --resume <exact canonical local session ID>
```

The loopback model requests removal of a disposable test file. The native
`approval.pending` response supplies the exact request ID. While it remains
pending, another client submits the literal text `/approve all` through
`prompt.submit`. The native request ID remains pending and the file survives.
The TUI output contains the external prompt and native tool approval command.
An explicit, separate `approval.respond` with the exact native prompt ID and
execution generation denies the test tool. The viewer is stopped before the
owner, the owned process is reaped, and the second gateway remains usable for
a further native prompt submission.

Stable-source result: PASS, 28.71 seconds. Owned PID `97090`, unrelated PID
`97923`, native instance `994b1bb9effd4c4d94113313986e3b1a`, session
`local-60b89c239d543697f79029913b553d1068650322a0c99cd18a24aae097955bc4`,
approval `4488a24d1e4848dfb7dae0550542d0be`. First and second admissions were
`2e5d0295c5074c13b368791a32b8b75d` and
`d07d1213f794493c873b259c2ae1d270`. Both native receipts were `queued`, with
authority epoch `1` and execution generation `0`. The native owner assigns an
execution generation when the admission starts. This is submitted evidence,
not a model-read or completion receipt.

## Historical prototype contract details

- Canonical local identities are `local-` plus 64 lowercase hex characters.
  They differ from the existing 22-character classic IDs and the gateway's
  timestamp-plus-eight-hex route IDs. Displayed shortened hints are not bindings.
- A lost WebSocket answer is uncertain and is never automatically replayed.
  Fixture tests cover this, a permission event preceding the admission receipt,
  changed owner start/instance, and unsafe home permissions.
- Stop the viewer process group before the gateway. The TUI natively attempts
  owner recovery on disconnect and could otherwise restart it after shutdown.
- The classic idle viewer only adopts its own queued admissions or native
  automation cards (`hermes_cli/gateway_tools_notify.py`). The qualified viewer
  is `--tui`; the classic grid/identity heuristics cannot establish its state.
- The removed prototype's `Start` required a configured private independent home and refused
  a named profile or a home containing other profiles. It never adopts or
  replaces an unrelated global gateway. Unconfigured homes produce an explicit
  setup error rather than claiming readiness.

## Open integration and setup boundaries

The implemented route calls the normal provider `gateway ensure` lifecycle
and reuses the configured native home/profile and its shared persistent owner.
It creates one canonical native session per fresh Swarm worker, attaches a native
TUI viewer, and never signals the shared gateway when stopping a worker. The
normal native authentication flow is reused; there is no per-worker home or
profile clone. Native session descriptors bind the worker/shim generation to
profile home, control home, owner PID/start, instance, code SHA and authority epoch.
Schema 5 prevents older daemons from treating this native runtime as a classic
PTY-owned worker.

The deterministic assembled test covers A/B session isolation, literal native
input, durable retry without another submission, restart without another native
session or turn, scoped stop, and retained workspace uncertainty. Native status
sampling bypasses the classic grid classifier. A native viewer exit sets
`NativeWorkUnknown` and NeedsInput; cancellation acknowledgement is not joined
completion, so Delete and workspace removal remain blocked.

Native profile cloning is not yet a complete shared-auth solution for an
independent owner. Hermes's normal profile fallback can share root OAuth
custody, but profile creation notifies the shared multiplexer. Current upstream
calls `gateway.standalone` a temporary compatibility shim and ignores the old
`multiplex_profiles: false` opt-out. This implementation does not depend on that
topology or manually read/copy real credentials. The owner-facing normal native
authentication/setup flow and its storage custody need integration review;
per-agent forced login is not an accepted workaround.

No general background tool/subagent containment, process-crash recovery,
production credential refresh, or installed-release-artifact qualification is
claimed here. The source test proves normal shutdown of its own native owner
and survival of another; it does not prove that arbitrary native descendants
cannot leave that owner's process group.

## Current verification boundary

The historical per-worker-owner prototype and its unguarded source runner were
removed when the shared-owner architecture was approved. Its source receipts
above remain historical API/TUI evidence, not proof of the approved shared-owner
core path. Current and future real-provider qualification must use
`scripts/verification/native_fixture_sandbox.py`: scratch-only native writes,
private physical CLI copies, and only the explicit fake-provider/native-owner
local ports and private Unix sockets. Linux additionally requires a read-only
mount and fresh loopback-only network namespace.

`TestHermesSharedOwnerSemanticSendRestartAndStopIsolation` exercises the assembled
core with a deterministic native wire fixture. Package race checks, vet and lint
are distinct from the guarded real shared-owner A/B background and subagent
qualification recorded above.
