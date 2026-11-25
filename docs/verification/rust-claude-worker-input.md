# Rust Claude Code native worker input

Claude Code 2.1.329 is qualified on macOS arm64 for ordinary worker input bound to a private Discord place. A new worker installs the same sender and independently named queue guard used by the Go adapter. The shim waits for two live, authenticated native registrations for the same conversation. The guard retires on a conversation-changing native command or session end. Prior receipts remain readable after retirement.

The owner semantic endpoint requires an exact durable connector cutover for the operation, target incarnation, rendered text and origin. The shim rechecks the active installation and cutover while holding the connector guards at native handoff. It writes an uncertain receipt before handing out the operation once. It holds authority through the sender's immediate submission acknowledgement, with a bounded expiry enforced by the sender. Native rejection is retained separately. The input goes through Claude's own prompt queue while Claude controls its permission dialog.

```bash
python3 scripts/verification/rust_claude_worker_input.py /path/to/swarm /path/to/claude
```

The guarded witness copies the installed executable, validates the native provider sandbox's allowed and denied paths, uses a synthetic Claude API key and serves synthetic inference on the only allowed TCP endpoint. It binds a private synthetic personal installation without connecting to Discord. The script stages the exact connector cutover on disk to isolate the owner and shim behavior. It refuses a forged body and a caller with no cutover, accepts an exact retry without another queue entry, leaves a genuine native Bash permission dialog pending, waits for local denial, and observes the exact same message in the real provider's next model request. The requested sentinel file stays absent. Native `/clear` retires readiness and keeps the exact old receipt readable.

The Go modal and lifecycle witness `SWARM_NATIVE_ADMISSION_FIXTURE=1 go test ./internal/claudeinput -run '^TestNativeClaudeModalAdmission$' -count=1 -v` passed against a copied installed 2.1.329 in 23.81 seconds with the same loopback isolation class. That reference also observes stale enqueue exclusion after `/clear`, exact native interrupt, and separation of identical human and plugin text. These are distinct from this Rust witness.

Local serial Rust tests passed with 74 passing and one existing opt-in test ignored. Clippy passed with warnings denied. Manager nonce/turn/tool custody, native permission decisions, Linux, and real Discord or Anthropic accounts remain outside this evidence.
