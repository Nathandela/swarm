# Historical stopped classic OpenCode compatibility

The classic Go v0.18.2 shim writes a schema-1 session with wire 1, a private launch record and an exit report after joining the PTY. This gate checks that historical shape. The captured ses_ ID and complete original JSON remain in the Go ledger. Rust uses the same file for label edits. The gate refuses a current OpenCode runtime binding or its side files, a legacy account binding, unclassified launch flags, or a live shim.

```bash
cargo build --locked -p swarm
python3 scripts/verification/stopped_go_opencode.py GO_V0182 PINNED_CURRENT_GO target/debug/swarm
```

The original and rollback binary is Go tag v0.18.2 (2f54b3e2835fbbd50247a7b722d0bf7ad88882a6). The current Go readability check uses f26648d69086e329132ac7849954d2f29a895387. The witness uses a synthetic provider process under the real historical supervisor, captures its session marker from the PTY, and copies the stopped root. After Rust labels are written, both Go binaries read them and Go v0.18.2 launches the synthetic provider with the same --session ID.

The current managed OpenCode v3 state requires separately verified worker, runtime, and receipt custody.

The original option builder emits model, agent and prompt in that order. A resume emits exactly one session flag and the captured conversation ID. The native macOS and Linux copied-state rehearsals include all three fresh options and refuse repeated prompts, duplicate session selectors, and a conflicting session ID before any writes.
