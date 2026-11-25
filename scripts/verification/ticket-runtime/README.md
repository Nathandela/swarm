# Ticket supervisor qualification on Linux

This is a local verification harness for the Codex launcher used by an isolated ticket supervisor. It uses a deterministic Responses provider, a local controller fixture and genuine Codex and Swarm binaries. It creates its own daemon, socket, home and state under a private temporary directory and supplies synthetic evidence. Nothing here selects or enables a production runtime.

## Run

The native check requires Linux user namespaces, bubblewrap, socat, Python 3.10+, Go as specified by the root module, and a reviewed native Codex ELF (tested with 0.172.0). From the repository root, set `native_codex` to its absolute path. The following commands use only local programs and temporary runtime state.

~~~bash
stage="$(mktemp -d)"
go build -o "$stage/swarm" ./cmd/swarm
go build -o "$stage/owner" ./scripts/verification/ticket-runtime/fixture/owner
go build -o "$stage/rpc" ./scripts/verification/ticket-runtime/fixture/rpc
(cd scripts/verification/ticket-runtime && python3 -m unittest -v test_binding.py)
python3 scripts/verification/ticket-runtime/fixture/qualify.py \
  "$native_codex" "$stage/swarm" "$stage/owner" "$stage/rpc"
~~~

The first check sends a genuine manager OwnerLaunch to an isolated Swarm daemon. With an empty private home it must reject the missing authenticated native identity before creating a shim. The next checks use explicitly synthetic persisted launch records. Swarm’s real WebSocket client connects to each confined app-server and submits a native turn. The dispatcher’s tool goes through the real host-side Swarm MCP binary to the synthetic controller. A worker’s native command uses the real Swarm worker CLI to return a correlated completion receipt. The output identifies which records and service were fixtures.

## Trust boundary

The reference launcher reads a private operator assignment plus the matching supervisor-owned `meta.json` and `shim-launch.json`. It validates the conversation, supervisor generation, workspace, parent role, provider, expiry and exact native wrapper. It rejects ordinary fallback invocations and binds the native socket inode across the namespace boundary. The shim directory and controller credential stay outside. Role-specific native home directories retain the native transcript on the host. The wrapper revalidates the assignment on each host-side tool request and while the backend runs.

Each isolated process has a fixed provider Unix socket and a scoped role capability. The dispatcher uses a fixed MCP-to-controller bridge. The worker holds a disposable capability for the worker-message endpoint. The broker supplies the actual controller credential outside the model process. Test requests never leave the local machine.

## Open production gates

The scheduler for an active lease, a real identity-matched provider broker and the dedicated supervisor service need independent qualification. A supervisor-owned native identity must correspond to the actual upstream principal executing its model requests. The fixture exercises no genuine provider account and never fabricates native credentials. A real manager launch remains an acceptance gate, along with reconnect/resume, lease revocation, bounded resource use and end-to-end machine admission.

Keep the supervisor and its credential store outside the namespaces. Control its launch environment so that only the reviewed ticket wrapper and provider may be selected, including during initial version probing and fallback. This script can refuse only invocations that reach it. Operator-owned configurations, binaries and private run roots must be protected from other processes under the host’s service identity. A connector or model that can reach the general owner socket defeats the intended boundary.

The code under this verification directory is experimental. It does not install a service, alter the ordinary Swarm launch path, expose a provider relay publicly, or grant GitHub, Discord or Cerbere access.
