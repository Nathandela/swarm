# Private manager bridge and service setup foundations

The `swarm voice mcp` subprocess uses the official [Go MCP SDK v1.8.0](https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.8.0). It exposes typed Swarm action, publication and clarification tools, forwarding host metadata separately from model arguments to the private controller socket. The native runtime/controller must resolve that evidence before executing a call; a credential or inherited session ID alone grants no native turn. The real controller, scheduler and native proof integration are separate work.

The stdio transport explicitly limits incoming frames to 128 KiB. Inputs, results and encoded envelopes are bounded. Native metadata and large integer arguments retain their original meaning. Tool schema validation rejects invented authority arguments. The local HTTP client uses only its configured Unix socket, refuses redirects and automatic mutation retry, preserves cancellation, and reports an unavailable response without exposing raw response text or credentials. Both the socket and its directory must have private owner custody before a bearer is sent.

Configuration has an exact versioned schema and a 16 KiB encoded limit. Atomic replacement and directory fsync preserve prior configuration when validation fails. Speech keys are separate owner-only files, loaded through bounded, no-follow, regular-file checks; no key value is a CLI argument. The launchd/systemd renderers preserve supported paths exactly and refuse characters their target format cannot represent. Rendering does not install or start a service.

Failing-first SDK and CLI tests preceded implementation. Independent Astra review then reproduced and verified fixes for seven defects: escaped configuration exceeding its read limit; unsafe socket custody; lost cancellation after response headers; duplicate or case-aliased reply fields concealing refusal; systemd-invalid executable characters; XML path replacement; and canonical JSON expansion exceeding the result limit. Permanent regressions retain these cases. Independent focused race runs passed in 3.033 and 3.164 seconds; MCP CLI dispatch passed in 2.865 seconds. The full voice package passed in 8.237 seconds and focused voice/CLI lint reported zero issues. Loopback fixtures use no production credentials or model calls.

Reviewed source hashes:

```text
config.go dd0afd48befabf947c47823d29bef552bdc69c73c95a87d1577259e10fea490b
mcp.go b4784395d8736f378aa27dd0559b3da2716552fedc636456e9ca5d4cfcb9c9fc
bridge_client.go 097ec8ceaeb1a53da4d72650b0a4c2e66bdd4be8ea3459c05f79cc06e0790eb1
unit.go 3cea42741c8f0e80f69a9a672b769798735f1f0c494f92ccae4b712b4dedb255
cmd/swarm/voice.go 1f32ab416233f395b28f31758c7a081b5d55f5211a2bc719e657a406f20e16e3
```

The dependency adds the official SDK plus its JSON Schema, JSON encoding and URI-template dependencies; `x/time` moves to the SDK's required v0.15.0. This does not add a hosted MCP service or expose the private bridge over the network. Native-account action execution, service installation, desktop/phone authentication and the full voice experience are not certified by this checkpoint.
