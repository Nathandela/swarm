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

Once v0.29.25 became available, the original checksum manifest verified with the same trusted Ed25519 key. The verifier rejected a one-byte change to that manifest. Both archive hashes matched, and both extracted binaries executed natively and reported `swarm 0.29.25 (go1.26.11)`. [Published Go v0.29.25 reference](https://github.com/Nathandela/swarm/releases/tag/v0.29.25)

| Executed v0.29.25 reference | Signed archive SHA-256 | Extracted Swarm SHA-256 |
| --- | --- | --- |
| Darwin arm64 (universal archive) | `e62ef2565cd523cfd5d458190d524d2a84a2fc1f234c8e2fe4b4b225f818ec50` | `a20420c19218e506dc35003a188da27f9cdbb9b15601dcc40792d48422b27d72` |
| `tf_nathan` Linux x86_64 | `bd7be12638ef073e68f1c5755c683b4d9aec6842bb2e9279c6c6af2ecbc75006` | `6697c17b60176b7ad21923d6b65597a2c5e8f353f08665e42e0e5b33d52f292f` |

## v0.29.24 and v0.29.25 release reconciliation

On November 26 the GitHub releases API reported v0.29.25 as the latest stable release at the initial reconciliation, published at 15:49 UTC. The GitHub tag comparisons resolved to the same commits and file sets as the local Go checkout. The independent Discord call oracle remains `da7b9f2d`. [v0.29.24 source comparison](https://github.com/Nathandela/swarm/compare/v0.29.23...v0.29.24) · [v0.29.25 source comparison](https://github.com/Nathandela/swarm/compare/v0.29.24...v0.29.25) · [v0.29.25 release](https://github.com/Nathandela/swarm/releases/tag/v0.29.25)

| Go release change | Rust behavior and evidence | Remaining qualification |
| --- | --- | --- |
| v0.29.24 scheduled Claude opening prompt | Rust claims the attempt before native composer input, ties the draft and closing receipt to that conversation, and suppresses replay when delivery is uncertain. Installed Claude 2.1.329 exercised a real composer and two loopback model responses on each native host. [Native scheduled Claude witness](../../verification/rust-claude-schedule.md) | Final signed candidate and sustained scheduled workload. |
| v0.29.24 ten-minute opening deadline | The full Rust suite exercises the deadline boundary, keeps the attempt open while requesting a stop, and records the matching incomplete detail after verified exit. [Rust deadline regression](../../../crates/swarm/src/schedule/engine.rs) | Final signed candidate. |
| v0.29.24 completion guidance for a scheduled worker without a manager | The CLI emits Go's scoped `schedule complete` guidance and preserves the normal route when a mailbox or worker bridge is present. The CLI regression passed on both native hosts. [CLI regression](../../../crates/swarm/tests/teammate.rs) | Final signed candidate. |
| v0.29.25 thread names | Rust derives a title from the authenticated opener's first eight non-mention words, clips it to 95 UTF-16 units, and attempts the default title after a definitive HTTP 400 and renewed source and authority checks. Authenticated transport and the native default suites passed. [Baseline fleet evidence](../../verification/rust-m11-development.md) | Live Discord user ingress and thread creation. |
| v0.29.25 quieter reconnect repair | In a Rust-owned guild, known ingress keeps a thread quiet. Missing eligible human messages or unreadable history stage one restatement per gap epoch. The loopback tests checked permission changes, source deletion and lost acknowledgements. Retained Go migration accepts an already settled `GapMissed` row with its original receipt. [Ordinary repair evidence](../../verification/rust-discord-service.md) · [Retained Go evidence](../../verification/rust-discord-retained-state.md) | Live reconnect, plus processing pending repair and the full retained manager lifecycle. |
| v0.29.25 reply notices | Rust quietly closes a completed enrolled turn with a confirmed partial and an empty worker ledger. It can also issue one automatic notice after verifying the exit and unanswered opening turn of one linked enrolled Codex worker. A separate retained-personal-VM command reconciles Go's original reply obligation against linked-worker evidence and the 30-minute notice boundary. Both native hosts exercised these scoped paths. [Rust-owned reply checks](../../verification/rust-discord-service.md) · [Retained reply evidence](../../verification/rust-discord-retained-state.md) | Multiple-worker attribution, ordinary VM automation, and live Discord. |

The complete v0.29.25 normal source round passed 97 test-bearing packages with eight packages containing no tests. The runner was the macOS arm64 host using the Darwin x86_64 Go 1.26.5 toolchain under Rosetta, with umask `022` and `-count=1 -p=1 -parallel=1 -timeout 35m`. The normal round log SHA-256 is `aa9740e40bf391148b6f3d8f4c0d2b802c5f72533713687bbdd1a12fe0cb5f0c`. The separately downloaded signed v0.29.25 binaries and the Rust qualification arms use the native architectures listed above.

The subsequent complete normal source round also passed all 97 test-bearing packages and eight packages containing no tests using native Darwin arm64 Go 1.26.10. The isolated checkout remained at the signed v0.29.25 source. Native log SHA-256 is `54d31da39cb850567a93b9d0d2edd5e7ca6a79410f44d9970826b6b9b8ee9e54`.

The full Rosetta race round also passed all 97 test-bearing packages with eight containing no tests. It used Go 1.26.5 Darwin x86_64 and the same serial package and test settings. Its log SHA-256 is `487d02f6bafcd6df5305a98ba651dfd8c28a0e3427fe9bc7b12449ce7334f223`.

The native Darwin arm64 Go 1.26.10 race round passed those same 97 test-bearing packages and eight packages without tests at the signed v0.29.25 source. With `-count=1 -p=1 -parallel=1 -timeout 35m` and umask `022`, the race log SHA-256 is `6dc73da5d00a6b335cc8274a21e30f9e4b541597b0eb18647396d53f8c739f53`.

## v0.29.26 worker attachments

Go `main` advanced six commits past the previous signed tag to `7f67c067f153f4c1cdd13f3cb20d9610dc6ca9cf`. GitHub published that exact source as v0.29.26 at 17:20 UTC. It broadens worker attachment formats, makes filenames stable through Discord, refuses hidden and credential paths, and adds an opt-in live test. The final merge retains the v0.29.25 HTTP 413 handling. Beads `agents-tracker-8fp1.9.2` tracks the parity work. [v0.29.26 source comparison](https://github.com/Nathandela/swarm/compare/v0.29.25...v0.29.26) · [v0.29.26 release](https://github.com/Nathandela/swarm/releases/tag/v0.29.26)

The original checksum manifest verified with the pinned Ed25519 key, and the verifier rejected a one-byte change. Both signed executables ran natively and reported `swarm 0.29.26 (go1.26.11)`.

| Executed v0.29.26 reference | Signed archive SHA-256 | Extracted Swarm SHA-256 |
| --- | --- | --- |
| Darwin arm64 (universal archive) | `5444440e83ff2806fa1a1f60496e4892c7eeb826ab13dd61be7abcdf597273d6` | `b6b621cb6cf91b71e17cba54fff91a3cdda3c862e145b029c42e51f6728bf663` |
| `tf_nathan` Linux x86_64 | `58cc17331cc8357f0bf5a8b639afa010b50d1d0b3d8d8e78c8d4bf455149ca9e` | `629ffb0eca67906dfefcbe3f0e5c63e648f8973c05bcd486fd83a15dd3d8859b` |

The six new offline Go attachment tests passed from exact source on macOS arm64 and isolated Linux x86_64, including the unsafe file-set subtests. The native Mac compiler was Go 1.26.10 and also produced the Linux test executables. Log SHA-256 is `acf747d858759400344a269b2c1dacdd29179b36a9e497ffd45e4193e4337511` on macOS and `91066987d9d3caa05daeddd58c3d84b00ef12ceca5211e1a76390544cd8f06e1` on Linux. Rust's paired capture and attachment-only loopback checks read the same Go source and retained the original v0.29.25 publication behavior. Live user-origin attachment delivery remains open. [Rust attachment witness](../../verification/rust-discord-retained-state.md)

The integrated Rust and signed Go v0.29.26 executables returned identical exit codes, stdout and stderr for both `teammate message` help flags and a file supplied with `--kind needs-input` on both native hosts. The help advertises the expanded formats, ten files and the 8 MiB total. The two comparison logs have SHA-256 `8075deec939ad3af71b1fda94b366e9459c5a82952fd851c7b2e5ae642637a57`.

The clean exact-source Darwin arm64 round passed 97 packages with eight containing no tests. It used native Go 1.26.10, umask `022` and `-p=1 -parallel=1 -count=1 -timeout=45m`. Log SHA-256 is `e4be4ed16870906786addad572d39ec007e7389c90f7124435ad5d76e34647cb`. The matching race round passed 82 packages, reported eight without tests and failed 15. Its log records restricted process enumeration, retained synthetic process state and timing failures. It contains zero race detector reports. The race gate remains open. Log SHA-256 is `0984aed7d2568e0d9755d72ef35df912e9f92ee1b3d8ec920f96551a5e5621b8`.

## v0.29.27 worktrees and terminal restore

GitHub published v0.29.27 at 21:54 UTC on November 26. The 11-commit source delta ends at `51e81f2604cb2a2eff4678c9a0b00bdf1b2a8a25`. The retained workflows affected are physical sibling worktree placement, ownership and deletion, and the first board frame after restoring an unchanged terminal. [Official release](https://github.com/Nathandela/swarm/releases/tag/v0.29.27) · [Exact source comparison](https://github.com/Nathandela/swarm/compare/v0.29.26...v0.29.27)

The pinned Ed25519 key verified the checksum manifest and rejected a changed copy. Both signed executables reported `swarm 0.29.27 (go1.26.11)` natively. The local signature and artifact witness SHA-256 is `2c3f0d152dc4ebc97087ce154c7e8bada03763e42489be80873f3bb009459756`.

| Host | Archive SHA-256 | Executable SHA-256 |
| --- | --- | --- |
| Darwin arm64 from universal archive | `e35974dd2b2fe20596593e0212bd5f4c74558928e91b687f4613d046995bc973` | `5a10479a69b0faaf182166493f4577a388e79d4ea640a2600aa8f79af64ed6a3` |
| `tf_nathan` Linux x86_64 | `bac5052b0cd0fe52608d0f6371c04dd8708b5ebe3747e5d644046e1a700f83d2` | `0c1dcb49c75995d6cdc5e88bf35e0ac596718104dc740a7835c4bcd6c93a0749` |

Native Darwin Go 1.26.10 passed the focused `internal/worktree` race run and all 20 repetitions of `TestTerminalRestoreRedrawsUnchangedBoard` from exact v0.29.27 source. Log SHA-256 values are `3ddc8943b5796b118100abba5d37b62260439f2af64aa2076bdc86008589c31e` and `047b48f358d1aba764546f4ed12bbc561263b1bc981b1121dc15aa7ad4cd2f0f`. This was the focused v0.29.27 source check. The broader v0.29.26 race gate above remains open.

Rust's deletion safeguard preserves registered nested worktrees, including a temporarily missing gitfile, and independent nested repositories. Git 2.34 compatibility decodes quoted native pathname bytes while malformed registration input refuses removal. Ordinary deletion still succeeds next to a quoted sibling and a symlink to an external repository. The two focused tests passed on both hosts. Log SHA-256 values are `10ea314ba419150bfa157f8f9851766d22eb41a80e4cf95eea21ee0bc302b404` on Mac and `186c665bb43066f24d9b2f1b652eabe6a10939aaf3894802dec6aff6e6dabbac` on Linux.

The native integrated placement follows the verified primary Git repository for ordinary isolated launches and retains the linked source's exact HEAD. Both platforms exercised simultaneous claims, marker repair, preserved scheduled workspaces, replacement refusal and source-gone cleanup after a daemon crash. The terminal witness uses actual visible cells, checks the unchanged roster and geometry, and observes a two-second quiet return after Ctrl+Q. Signed Go v0.29.27 and the joined Rust build passed eight cycles on both native hosts. [Worktree verification](../../verification/rust-worktree-placement.md) · [Terminal witness](../../../scripts/verification/parity/README.md)

Both native hosts ran the original signed Go v0.29.26 to v0.29.27 upgrade transaction in a private prefix and state root. The loopback feed served the original signed release bytes. A changed signature refused staging, the original signature permitted activation, rollback restored the exact original Swarm and legacy companion bytes, and the unattended check kept v0.29.27 held. The Mac and Linux receipt SHA-256 values are `02d285797d39fe0d2145093565393fbf9ce70a1d84ab94f35de02529ba6af3e9` and `973983013727307d381a6bc59c3b473720f090b540df9d1f42272b75b61d5e93`. This control used a stopped disposable Go environment. [Rust preview transition](../../verification/rust-managed-upgrade.md)

## M0 source build repeatability on Apple Silicon

Two builds in independent scratch directories on this host with Go 1.26.5 and `GOARCH=arm64` produced identical SHA-256 digests within each reference. Both binaries executed their version and Discord help probes. Running the builder with an existing output directory returned exit 2 and preserved its contents.

| Source CLI binary | Native darwin/arm64 SHA-256 |
|---|---|
| Main at f26648d6 | `5f92beebd77f4d49a45ca7989a9d88d42bc3192b623356661217f102366fe179` |
| Calls at da7b9f2d | `683c26bf5bb4fe1e87aea76d435b47473d8f4443b67c14d2be5cf1bad9bededf` |

Repeatability here covers a fixed compiler and target on one host. The published archive above uses the release toolchain, flags and stamps. [Builder and invocation](README.md#running-the-first-milestone)
