# Rust Claude Code native worker input

Claude Code 2.1.329 is qualified on macOS arm64 for ordinary worker input bound to a private Discord place. A new worker installs the same sender and independently named queue guard used by the Go adapter. The shim waits for two live, authenticated native registrations for the same conversation. The guard retires on a conversation-changing native command or session end. Prior receipts remain readable after retirement.

The owner semantic endpoint requires an exact durable connector cutover for the operation, target incarnation, rendered text and origin. The shim rechecks the active installation and cutover while holding the connector guards at native handoff. It writes an uncertain receipt before handing out the operation once. It holds authority through the sender's immediate submission acknowledgement, with a bounded expiry enforced by the sender. Native rejection is retained separately. The input goes through Claude's own prompt queue while Claude controls its permission dialog.

```bash
python3 scripts/verification/rust_claude_worker_input.py /path/to/swarm /path/to/claude
```

The guarded witness copies the installed executable, validates the native provider sandbox's allowed and denied paths, uses a synthetic Claude API key and serves synthetic inference on the only allowed TCP endpoint. It binds a private synthetic personal installation without connecting to Discord. The script stages the exact connector cutover on disk to isolate the owner and shim behavior. It refuses a forged body and a caller with no cutover, accepts an exact retry without another queue entry, leaves a genuine native Bash permission dialog pending, waits for local denial, and observes the exact same message in the real provider's next model request. The requested sentinel file stays absent. Native `/clear` retires readiness and keeps the exact old receipt readable.

The Go modal and lifecycle witness `SWARM_NATIVE_ADMISSION_FIXTURE=1 go test ./internal/claudeinput -run '^TestNativeClaudeModalAdmission$' -count=1 -v` passed against a copied installed 2.1.329 in 23.81 seconds with the same loopback isolation class. That reference also observes stale enqueue exclusion after `/clear`, exact native interrupt, and separation of identical human and plugin text. These are distinct from this Rust witness.

The combined Rust witness can also use the public dispatch producer. Build the candidate and the unit test executable with `cargo build --workspace --locked` and `cargo test --workspace --locked --no-run`, then pass the executable printed for `src/main.rs` as the third script argument. The test admits an explicit worker-addressed synthetic personal message through the production inbox, obtains its actual worker incarnation through owner IPC, and exercises the actual connector cutover and receipt settlement. The provider-only witness still tests the missing cutover and exact retry paths directly. Neither fixture opens a real Discord account.

The combined serial Rust run passed 102 tests with three opt-in cases ignored. Clippy passed with warnings denied. Manager nonce/turn/tool custody, native permission decisions, other provider versions and live Discord or Anthropic accounts remain outside this evidence.

## Personal and guild delivery on macOS and Linux

Installed Claude 2.1.329 passed three independent runs on macOS arm64 and Linux x86_64. The personal DM admitted the owner’s explicitly addressed text. An enrolled guild thread admitted the owner’s mention and verified its audience again at the native effect. A separate guild run withdrew the audience at that recheck. Its native receipt was rejected and the subsequent real model request included a fresh local operator message with zero occurrences of the withdrawn text. In the accepted cases, the model observed the addressed message exactly once. All six runs preserved the pending Bash dialog and its protected file, then revoked the installation's native readiness on `/clear`.

The producer, owner IPC, receipt store, guild proof socket, native Claude plugin and PTY ran as real processes. The Discord message lookup and membership result were synthetic, as were the Claude inference responses. Use the optional native test executable argument to [the worker witness](../../scripts/verification/rust_claude_worker_input.py) with `--guild accepted` or `--guild revoked`. Run Linux through the scratch-only launcher with the staged test executable named `producer`. Live Discord delivery and a real encrypted call have separate release gates.

## Enrolled worker transcript startup

Claude 2.1.329 can create its native transcript with only a header before the first conversation row. The worker read stays pending while that bounded prefix is valid. After conversation records arrive, completion still requires the original transcript inode, expected prompt hash and linked assistant response. The parser refuses a conflicting conversation or checkout, malformed complete JSON and a scan that exhausts the size bound.

Six isolated enrolled worker runs per host completed the original prompt envelope and manager tool call, then recorded the expected reply. Darwin arm64 ran `46dd3b10`. Linux x86_64 ran `17de672c`, which also makes the test create its transcript with explicit `0600` permissions. The focused test passed on Linux under both the VM default umask and `077`. An earlier separate manager custody error was absent in the repeated runs. These runs qualify the transcript startup change and leave that custody error open.

| Native evidence | SHA-256 |
| --- | --- |
| Darwin enrolled runs, ordered list of six log digests | `13471f458b9993e8047e23676440b8e0f6a35c729b766160cd3dc5a518d96c4e` |
| Linux enrolled runs and focused file-mode tests | `b226aff0f3b69bdc5bad61586b47008d8b0ef50b785289c8a397be12692216a6` |
