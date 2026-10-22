# Subscription switcher evidence

See the [research conclusions](../subscription-switcher-2026-10-22.md). These artifacts characterize current behavior; they do not qualify a new switcher or authenticate real subscriptions.

## Native Codex auth-cache probe

```bash
python3 docs/research/subscription-switcher-evidence/native_codex_fake_auth_probe.py \
  --codex /absolute/path/to/codex
```

Uses generated fake JWTs, temporary HOME/CODEX_HOME/XDG paths and cwd, file auth, and a mock backend. Only initialize/account-read RPCs are requested. Backend and OAuth URLs point to loopback; no OS-level egress interception is asserted. The captured JSON records binary SHA-256 and the observed A → A → B sequence across file replacement and process restart. An earlier attempt without the backend override made external workspace discovery with a fake token and got 401; no real credential or model turn was involved.

## Native concurrent refresh probe

```bash
python3 docs/research/subscription-switcher-evidence/native_codex_concurrent_refresh_probe.py \
  --codex /absolute/path/to/codex
```

Creates one temporary native home, first initializes its SQLite state, then starts two native app-servers with the same generated token and distinct loopback refresh URLs. The fake endpoint parses request bodies and holds the first original-token refresh until the other process arrives. `refresh-probe-result.json` records overlap with both account/read RPCs complete. Labels identify the child configured with each URL; no OS-PID network capture is claimed. Follow-up refresh requests use another synthetic token. No real token rejection or refresh persistence is established.

`refresh-probe-exploratory-result.json` preserves an earlier result with a child initialization failure. Its old reader labeled immediate stdout EOF as a timeout; later diagnostics found concurrent first-time SQLite setup contention. The final probe initializes that scratch state first and distinguishes EOF from a response deadline. This is not a diagnosis of an existing Swarm production startup failure.

## Swarm reproductions

The two `*.go.txt` files can be injected into `internal/skeleton` with Go's `-overlay` facility without changing production source. Map a virtual `internal/skeleton/<name>_test.go` to the absolute path of the corresponding text file in an overlay JSON `Replace` object, then run:

```bash
go test -race -count=1 -overlay=/absolute/path/to/overlay.json ./internal/skeleton \
  -run '^TestOverlay(SourceConfigDriftRefusesSuccessorBeforeKill|AuthRecycleDoesNotReplayPrompt)$'
```

The configuration-drift test **passes by reproducing a refusal**, not by proving that recovery works. The no-replay assertion tests recovery launch construction, not provider side effects. Native Go 1.26.10 on Darwin arm64 was used; inherited `SWARM_*` variables were cleared. Exact recorded commands and paths describe this workstation, and should be adjusted when rerunning elsewhere.

`recovery-summary.json` records selected normal/race lifecycle coverage; `root-focused-summary.json` records additional account/config/identity checks. The full repository suite was not rerun for this research. `repositories.json` is a dated GitHub metadata snapshot; `source-surface.json` is a deduplicated count, not an estimate of removable lines.

`upstream-tests.json` captures exact source pins, commands and fixture boundaries for 856 passing selected claude-swap tests and 465 passing codex-auth tests with 8 conditional skips. Those tests used fake credentials/Keychain/daemon/network fixtures and do not qualify real account switching.
