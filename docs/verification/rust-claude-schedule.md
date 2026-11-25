# Rust scheduled Claude Code witness

The Go v0.29.22 schedule route starts Claude with a reserved native session UUID and submits the opening prompt through its private composer. Claude reports through `swarm schedule complete` using the existing attempt-scoped Unix receipt endpoint. The scheduled route uses the configured Claude permission mode. The matching closing assistant response is a separate completion condition.

The Rust shim qualifies the idle composer and native hook payload for Claude Code 2.1.329. It checks the exact reserved attempt, operation, session UUID and opening content hash before submitting the prompt. The scheduler writes its durable Begin first. The shim sends one bracketed paste while the owner input lease is free. It checks the exact durable attempt again and waits for Claude’s matching native draft indicator before issuing Enter. A changed screen or missing draft leaves the attempt in Attention with no replay. Tabs, carriage returns and other terminal controls produce a permanent Claude route refusal before a worker is reserved, so the scheduler can try the next configured agent. A native UserPromptSubmit hook joins the exact paste hash to the native prompt UUID. The closing Stop event must belong to the same main-loop native conversation and prompt UUID. The scheduler requires both the scoped receipt and the closing message containing its summary.

Run the local witness with absolute paths to a built candidate and an installed Claude executable.

```bash
python3 scripts/verification/rust_claude_schedule.py /path/to/swarm /path/to/claude
```

On macOS arm64 with installed Claude Code 2.1.329, the witness copied the executable into an isolated scratch directory. The Seatbelt checks first proved denial of writes beyond scratch and TCP connections to an unrelated loopback listener. Only the local synthetic inference endpoint was allowed. The script observed a single opening turn, a shell tool call to the scoped receipt endpoint, Working while the model's final response was withheld, and Ready after its actual Claude Stop hook. Retrying the public manual run with the same operation ID retained one occurrence and one attempt. A second disposable workspace reached a real folder-trust prompt. Swarm reported Needs input and submitted no opening request. Completion invoked outside the worker scope was refused. Public CLI jobs containing literal tab or carriage return recorded a Claude refusal with no worker or prompt and attempted the next configured route. A deterministic executable painted a new folder-trust modal immediately after receiving the bracketed paste. The run entered Attention and the executable observed no Enter.

Local verification passed `cargo test --workspace --locked -- --test-threads=1` with 72 passing tests and one existing opt-in test ignored. `cargo clippy --workspace --all-targets --locked -- -D warnings` passed. Linux, live account inference, schedule reopen after exit, exact Claude quota fallback, recovery through a manual trust decision and Claude Discord/manager native input are separate open cases. The current route keeps the opening fenced after owner terminal input. A different Claude version needs its own composer and hook witness.

See the [Go scheduled-agent qualification](scheduled-agents/README.md) and [Claude hook reference](https://code.claude.com/docs/en/hooks).
