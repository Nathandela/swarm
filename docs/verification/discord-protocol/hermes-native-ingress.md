# Hermes native message ingress qualification

2026-10-26, macOS arm64. The new `internal/hermesinput` package is a native
runtime primitive; this evidence does not claim daemon/shim integration or an
all-five Discord release gate has passed. The existing classic adapter remains
unchanged.

## Qualified native boundary

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

`TestNativeSourcePermissionAndOwnedLifecycle` starts two independent private
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

## Contract details that matter to integration

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
- `Start` requires an already configured, private independent home and refuses
  a named profile or a home containing other profiles. It never adopts or
  replaces an unrelated global gateway. Unconfigured homes produce an explicit
  setup error rather than claiming readiness.

## Open integration and setup boundaries

Daemon/shim launch fields, durable incarnation binding, native observation,
receipt mapping, and normal stop/reconnect wiring are not implemented by this
package alone. The package's `MinimumVersion` is v0.22.5; production admission
still needs an explicit modern runtime selection and detected-version gate.

Native profile cloning is not yet a complete shared-auth solution for an
independent owner. Hermes's normal profile fallback can share root OAuth
custody, but profile creation notifies the shared multiplexer. Current upstream
calls `gateway.standalone` a temporary compatibility shim and ignores the old
`multiplex_profiles: false` opt-out. This implementation does not depend on that
topology or manually read/copy real credentials. The owner-facing normal native
authentication/setup flow and its storage custody need integration review;
per-agent forced login is not an accepted workaround.

No Linux/VM, background tool/subagent containment, process-crash recovery,
production credential refresh, or installed-release-artifact qualification is
claimed here. The source test proves normal shutdown of its own native owner
and survival of another; it does not prove that arbitrary native descendants
cannot leave that owner's process group.

## Reproduce

```bash
SWARM_HERMES_NATIVE_BINARY=/path/to/isolated/venv/bin/hermes \
SWARM_HERMES_NATIVE_SOURCE=/path/to/pinned/hermes/source \
SWARM_HERMES_NATIVE_TUI_DIR=/path/to/pinned/hermes/source/ui-tui \
SWARM_HERMES_NATIVE_NODE=/path/to/pinned/node \
go test ./internal/hermesinput -run TestNativeSource -v -count=1 -timeout=120s
```

With no native binary variable, that source qualification is explicitly skipped.
The deterministic package fixtures still run. `go test -race`, `go vet`, and
the package lint gate are separate from the opt-in native source proof.
