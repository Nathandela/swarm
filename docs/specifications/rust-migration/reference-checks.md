# Reference observations from the planning pass

Recorded locally on 25 November 2026. The runtime checks below used macOS 27.2.1 (26B131), Go 1.26.5, GOHOSTOS/GOHOSTARCH darwin/amd64. These are observations of the Go references. The measurements are single suite invocations on a working development machine. Use the paired controlled runs in the [qualification plan](qualification.md) for Rust performance claims.

| Check | Result |
|---|---|
| Main revision | Remote main and release v0.29.22 resolve to `f26648d69086e329132ac7849954d2f29a895387`. Local source build SHA-256 `dd1c72a067ff7dbd51d31413a6d51be10c3fb381173145e52ab9d1a509ec354e`. |
| Call reference | Remote integration head `da7b9f2dfa7e2c6960bd860a72459c0448845c82`. Independent local source build SHA-256 `dc4f267601edde65989656466216db614446782a2d89a60ad0540abc6482dbe3`. |
| CLI inspection | Built both with the ordinary Go compiler. Main reports `swarm dev (go1.26.5)` as expected without release linker stamps. Both binaries printed Discord help successfully. Only the call reference advertised `stop-call`. A frozen qualification run also needs the installed release and matching media helper. |
| Codec checks | Complete `internal/wire` and `internal/shimwire` suites passed. |
| Targeted storage and Discord checks | Two migration-order/missing-step tests and four tests for uncertain creation, conflicting admission replay, concurrent operation binding and wait-notice nonce expiry passed. |
| Keystroke echo | Real client → daemon → shim → PTY, p95 **0.127 ms** across 1,000 post-warmup samples. The Go gate of <10 ms passed. |
| Warm-daemon first paint | 50 real sessions, 25 trials, p95 **8.449084 ms**. The <100 ms requirement passed. One missing shim-socket diagnostic appeared during the passing run. Its cause was not established. Carry the diagnostic into M0 calibration before treating the fixture as quiet. |

To reproduce scoped checks in independent pinned checkouts, use the shared Go caches and the test supervisor's private scratch. Build flags, paths and metadata can change binary digests. Always record the digests of the actual executables used.

```sh
go build -o /tmp/swarm-main-reference ./cmd/swarm
/tmp/swarm-main-reference --help
/tmp/swarm-main-reference version
/tmp/swarm-main-reference discord --help
go test -count=1 ./internal/wire ./internal/shimwire
go test -count=1 ./internal/persist -run '^TestApplyMigrations'
go test -count=1 ./internal/discord -run '^Test(OwnerUncertainCreateIsNeverRepeated|AdmissionDurableReplayConflictAndExactContext|ConflictingConcurrentClaimsAndOperationBinding|UnconfirmedWaitNoticePastNonceWindowGoesPassive)$'
go test -count=1 -v ./internal/e2e -run '^TestE2E_LiveShimKeystrokeEchoLatencyP95$'
go test -count=1 -v ./internal/tui -run '^TestFirstPaintGate_RealDaemon_FiftySessions_P95$'
```

In the call reference checkout build to a different path and exercise `discord --help`. No live Discord event, native provider inference, rollback, long soak, screenshot pair or Rust candidate was exercised in this planning pass.

## Published macOS reference for M0

On 25 November 2026, `gh release download v0.29.22 --repo Nathandela/swarm -p checksums.txt -p checksums.txt.sig -p swarm_0.29.22_darwin_all.tar.gz` retrieved the three assets into a private scratch directory. Ed25519 verification of the original checksum bytes and base64-decoded signature succeeded with the [public key in the pinned source](https://github.com/Nathandela/swarm/blob/f26648d69086e329132ac7849954d2f29a895387/internal/upgrade/keys.go). Appending one byte to the checksum document caused verification to fail. The downloaded archive matched its unique signed SHA-256 entry, `381b638c39a03476094ad02a5987e9e6f65238b48717bbba6647271c5da1fc98`. Extracting the regular member `swarm` produced universal x86_64/arm64 Mach-O bytes with SHA-256 `addfa213521388e13278ea9cca4e6eeeb5aa15d3bea61b9e59f737ea78e80129`. Running `version` from scratch printed `swarm 0.29.22 (go1.26.11)`. Its `discord --help` omitted `stop-call`. Use this artifact for published macOS comparisons and record the executing architecture. Linux release-archive execution remains open.

On 26 November 2026, the v0.29.23 tag resolved to `edee76a0745152594d162b64de273f8d53a6d5c2`. The signed checksum document verified using the unchanged trusted Ed25519 release key. The same verifier rejected a one-byte modification. Both extracted executables reported `swarm 0.29.23 (go1.26.11)` on the designated native hosts. Their Discord help included `resume-manual`. The separate `da7b9f2d` reference supplies the call-specific `stop-call` surface. [Go v0.29.23 release](https://github.com/Nathandela/swarm/releases/tag/v0.29.23)

| Executed reference | Signed archive SHA-256 | Extracted Swarm SHA-256 |
| --- | --- | --- |
| Darwin arm64 (universal archive) | `62209ecf8363dd6ceef6bec6eb192791dc596ee745f8638bae12d9628129e95a` | `180d1083123a44325ab878543124539ac0ee8863983abfb14f4a933b32ab7549` |
| `tf_nathan` Linux x86_64 | `94c41cb6d79dc1dc97e2efc24f500378d625f27679e7beb2cc74078277a77c95` | `dbf174e757922d2e0da593a2c3f9ae770d4f1e966f1572282388eb87a5ce742a` |

The v0.29.24 tag and main resolved to `a716dd50a5faf746ce6f0831fc31d0d73b06ffeb`. The change from v0.29.23 is the scheduled opening-prompt repair. The new reference claims native Claude composer custody before input, stops an attempt after ten minutes if the opening prompt never began, waits for verified exit before closing the attempt, and directs a scheduled worker without a manager to `schedule complete`. The call branch available from GitHub was already an ancestor of the separately qualified `da7b9f2d` source. Both new signed binaries reported `swarm 0.29.24 (go1.26.11)` and emitted identical scheduled-worker guidance on the designated native hosts. The signature verifier rejected a one-byte checksum modification. [Go v0.29.24 release](https://github.com/Nathandela/swarm/releases/tag/v0.29.24) · [Scheduled prompt change](https://github.com/Nathandela/swarm/pull/110)

| Executed v0.29.24 reference | Signed archive SHA-256 | Extracted Swarm SHA-256 |
| --- | --- | --- |
| Darwin arm64 (universal archive) | `5e116edab170d3db7cebeaf3b5f21d832e43b39b19cbf92315cda022dce245e3` | `1527e5688243cdf98fb20f45cd00c9dc9427cb8e50faf5640c0778640359fe7a` |
| `tf_nathan` Linux x86_64 | `4a9451ca5caa670c08406bfe091f9bf5faaff59e7a67fbcba189b8cd3ab368af` | `3eac509227792e1ef4812860ea74a7d3c56f9a2ade5a8eff26b30b6f4861f6d8` |

The standalone Darwin arm64 v0.29.24 source checkout passed the full normal and race rounds with native Go 1.26.9, `-count=1 -p 2 -timeout 35m` and umask `022`. Each reported 97 test-bearing packages and eight with no tests. Log SHA-256 is `2a3925ecf513388c1dd834fe456440a8db6ca708fe481644c8225a790cd42596` for the normal round and `48cb5a596cad2de68c2732e5110fa7c80804b064be3024b234c06add873d7aaa` for the race round. The previous umask `077` run changed the seeded modes used by several custody fixtures. The diagnostic-log, Discord custody and daemon checks passed when repeated under the ordinary mask. Linux x86_64 passed all four scheduler and scheduled-worker regressions in a private home with the native Go 1.26.9 compiler. Its complete source round stopped at the VM disk guard.

Later on 26 November, remote main and the `v0.29.25` tag advanced to `44d796734fc60f6f9b9d6705dca44736f0c5741b`. The source delta covers Discord thread naming and connector and voice notice handling. New thread names use the first eight non-mention words of the verified opener and at most 95 UTF-16 code units. A definitive HTTP 400 can fall back to the original default title. Reconnect repair asks a thread to restate only when an admissible message was missed or history is unreadable. An existing manager reply normally ends routine reply notices. A missing linked-worker result can still require a notice, with the unverified-final notice limited to one per discussion in 30 minutes. Beads `agents-tracker-8fp1.9.1` tracks the implementation and fixture changes. The full Go Discord and Voice packages passed on macOS arm64. All eight targeted regression cases passed without cache on both native hosts. Log SHA-256 is `70219e23d30aab269ae2314b051851a129b5683d8789a6437692628cba7e3146` on macOS and `5d3d00f1b8a6015ea1525127fcc8c6a3853cd22662108fd1c87a26b38a50cf7c` on Linux. GitHub's latest published release at this checkpoint was still v0.29.24, and unchanged dashboard fixtures used that signed release. [Go v0.29.25 change](https://github.com/Nathandela/swarm/pull/113)

## M0 source build repeatability on Apple Silicon

Two builds in independent scratch directories on this host with Go 1.26.5 and `GOARCH=arm64` produced identical SHA-256 digests within each reference. Both binaries executed their version and Discord help probes. Running the builder with an existing output directory returned exit 2 and preserved its contents.

| Source CLI binary | Native darwin/arm64 SHA-256 |
|---|---|
| Main at f26648d6 | `5f92beebd77f4d49a45ca7989a9d88d42bc3192b623356661217f102366fe179` |
| Calls at da7b9f2d | `683c26bf5bb4fe1e87aea76d435b47473d8f4443b67c14d2be5cf1bad9bededf` |

Repeatability here covers a fixed compiler and target on one host. The published archive above uses the release toolchain, flags and stamps. [Builder and invocation](README.md#running-the-first-milestone)
