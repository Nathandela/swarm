# Historical stopped classic OpenCode compatibility

The classic Go v0.18.2 shim writes a schema-1 session with wire 1, a private launch record and an exit report after joining the PTY. This gate checks that historical shape. The captured ses_ ID and complete original JSON remain in the Go ledger. Rust uses the same file for label edits. The gate refuses a current OpenCode runtime binding or its side files, a legacy account binding, unclassified launch flags, or a live shim.

```bash
cargo build --locked -p swarm
python3 scripts/verification/stopped_go_opencode.py GO_V0182 PINNED_CURRENT_GO target/debug/swarm
```

The original and rollback binary is Go tag v0.18.2 (2f54b3e2835fbbd50247a7b722d0bf7ad88882a6). The current Go readability check uses f26648d69086e329132ac7849954d2f29a895387. The witness uses a synthetic provider process under the real historical supervisor, captures its session marker from the PTY, and copies the stopped root. After Rust labels are written, both Go binaries read them and Go v0.18.2 launches the synthetic provider with the same --session ID.

The current managed OpenCode v3 state requires separately verified worker, runtime, and receipt custody.

The original option builder emits model, agent and prompt in that order. A resume emits exactly one session flag and the captured conversation ID. The native macOS and Linux copied-state rehearsals include all three fresh options and refuse repeated prompts, duplicate session selectors, and a conflicting session ID before any writes.

## Native resume authority

A stopped classic row can be viewed and labeled in compatibility mode. A Rust
resume request for that row returns a refusal directing the operator to its
original Go owner. Go v0.18.2 extracts the `ses_` identifier from the terminal
exit marker and uses it with `--session`. That record provides neither the
native creation time nor verified historical account authority. The v0.29.24
Go launcher independently requires a durable native binding before it will
resume an older OpenCode row.

The signed Go v0.29.24 release
(`a716dd50a5faf746ce6f0831fc31d0d73b06ffeb`) records managed OpenCode as
schema 3. It binds the session ID, creation time, physical workspace and
configuration paths. OpenCode resolves provider credentials and configuration
contents at runtime. The Go binding has no authenticated provider account
identity. A matching SQLite row establishes native history identity, while the
historical account remains unproved. Rust therefore refuses stopped schema-3 admission with
either compatibility opt-in combination.

Run the guarded installed OpenCode 1.19.3 loopback with
`--stopped-go /path/to/go-v0.29.24`. Stage `stopped_go_opencode_native.py`
and `stopped_go_state.py` beside the usual Linux loopback files and put the Go
binary at the corresponding path inside the probe mount. The witness
completes one native loopback turn, stops the Go shim, and matches the SQLite
row and user turn to the durable record. It checks both Rust refusals against
byte-identical Go artifacts and native database files, unchanged session
directories and an unchanged loopback request count. The signed Go owner
then resumes the same native root, creation time and profile under a new
worker generation without another model request. The classic witness checks
that an explicit Rust resume request creates no session, changes no source
bytes, and invokes no provider.

Both witnesses passed on macOS arm64 and Linux x86-64 on 2026-11-26.
The classic witness used Go v0.18.2 and signed v0.29.24. The managed witness
used signed v0.29.24, the installed native executable, and a private loopback
profile with external egress denied. The provider account provenance gate
remains open.
