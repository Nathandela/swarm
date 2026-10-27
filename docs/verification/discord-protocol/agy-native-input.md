# agy native message input

2026-10-27. Implemented input-only messaging with isolated native qualification; not a deployment record.

Enabled installations automatically add a one-time registration instruction to new managed agy sessions. The exact original owner prompt remains the suffix. The registration runs in agy's own `run_command` context through `swarm native-input register-agy <session-owned descriptor>`, using normal native permissions. It grants no tool approval and changes no user/project configuration. A declined or missing registration leaves messaging unavailable with explicit registration/refresh guidance. Ordinary disabled-installation launches are unchanged; old shims are preserved.

The shim owns an authenticated 0600 Unix broker. Registration checks the Swarm session, exact native executable/version/fingerprint, native tool/source conversation, project, bootstrap command, and provider metadata root conversation. 1.4.0 retains its required native `Cwd` argument. 1.4.5 and 1.4.7 removed `Cwd` from the provider's tool schema, so its helper supplies the kernel working directory and the broker verifies the canonical configured workspace. A child source cannot claim its parent. Separately resumed native-child identities remain outside this qualified matrix; the exact root-ID check was not relaxed.

`SemanticSend` uses the broker independently of `StructuredChat`. The provider-owned `agy agentapi send-message` receives the exact target and text without a shell. Durable execution begins before provider I/O. The broker records each operation before native execution, binds payload hashes, and never retries an uncertain native write. Only an exact recipient/content receipt yields `submitted`; that does not mean task/model completion. The receipt retains structured origin, while history and status parity are not fabricated. Native tool decisions remain separate.

Qualified exact runtimes: **1.4.0, 1.4.5 and 1.4.7**. 1.4.7 was first admitted only in an owned Go compiler overlay, and entered the production allowlist after its full native fixture passed. VM 1.4.5 was likewise admitted only in a test overlay before its full native fixture passed. Other versions remain unqualified.

## Evidence

- Failing-first source tests covered missing broker APIs, wrong native owner/project/executable/source, lost replies, changed payloads, preserving the owner prompt, and unencodable NUL text rejected before provider execution.
- Broker race tests passed. Connected real-protocol/fake-native tests prove messaging and retained origin while `StructuredChat` remains false, including idempotent replay.
- The installed native fixtures use a physical private binary copy, synthetic local Gemini inference, isolated HOME/configuration and OS-enforced scratch-write/local-network guards. They approve only the registration command once, park a separate native permission dialog, submit a multiline UTF-8 message through the actual Go broker/native `agentapi`, confirm the command remains unexecuted for three seconds, refuse it locally and observe the marker in the next native model request.
- The public `Client.Launch` regression proves automatic CLI identity probing and broker creation with the integration enabled, followed by an exact origin-bearing semantic send and receipt without `StructuredChat`.
- Guarded VM 1.4.5: pass, 15.53 seconds, in an owned RAM directory inside a read-only mount namespace and fresh loopback-only network namespace. The native source schema omits `Cwd`, matching the measured 1.4.7 contract; the kernel workspace and exact root-ID checks remain enforced. [VM evidence](agy-native-modal-1.4.5.json).
- Guarded full-path 1.4.0: pass, 13.14 seconds. Guarded full-path 1.4.7 candidate: pass, 13.97 seconds. Source contract/schema changes were measured from the runtime rather than assumed by version name.

Reproduce with `SWARM_NATIVE_ADMISSION_FIXTURE=1 SWARM_NATIVE_GO=<native Go binary> go test ./internal/agyinput -run TestNativeAgyModalAdmission -count=1 -v`. `SWARM_NATIVE_AGY_BINARY` selects an existing read-only source binary; the fixture makes its own private copy. `SWARM_NATIVE_INPUT_HELPER` can select an owned crosscompiled Swarm helper when Go is absent on the target. Linux requires the whole fixture in the guarded namespace described in [native_fixture_sandbox.py](../../../scripts/verification/native_fixture_sandbox.py).

An early isolated-HOME probe against the global executable triggered agy's built-in updater, changing the installed Mac binary from 1.4.0 to 1.4.7. No explicit update/install or global rollback was performed; the owner was informed. All subsequent interactive probes used private copies plus OS write/network isolation. Copying the binary alone is insufficient isolation.

Whole-repository gates, independent integration review and deployment remain integration work. The native fixture demonstrates permission separation and context delivery; it does not claim unattended status/attention parity.
