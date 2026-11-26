# Rust Claude operator resume and hands-off handoff

[Provider workflows](../specifications/rust-migration/workflows.md) define the broader acceptance boundary.

| Route | Verified observation |
|---|---|
| Ordinary owner resume | Installed Claude 2.1.329 on macOS arm64 and tf_nathan Linux x86_64 completed a synthetic loopback turn, stopped, then reopened the exact original conversation through `launch.options.resume_from`. A matching authenticated hook was required before the owner launch acknowledged the resumed session. A live duplicate and a changed API authority were refused. |
| Existing Git worktree | Installed Claude on macOS arm64 and Linux x86_64 resumed in the original retained physical checkout. The owner checked Git registration and device/inode evidence before launch. A hands-off successor under the temporary fixture's Git tree remained refused by the worktree guard. |
| Dashboard resume | On both native hosts the operator pressed `r` on the ended row, attached to the resumed Claude PTY, then detached back to the dashboard. The native conversation ID matched in both the plain directory and Git worktree runs. |
| Hands-off | On both platforms, a negotiated owner launch took its directory, transcript pointer, and lineage from the running source. The successor's real native transcript contained the daemon-rendered pointers and omitted the forged client prompt. An unnegotiated request, a combined resume/handoff request, and a symlink substitution were refused. The source remained running. |
| Custody | Native profile and authority fingerprints, executable version, physical workspace and transcript identity were checked. Synthetic credential bytes were absent from the persisted profile proof. A changed authenticated conversation leaves a sticky ambiguity marker. The shim records a source-bound fingerprint of the effective project and ancestor settings and managed files. Authority-changing project or managed settings, dynamic credential helpers and unproved aliases are refused. The real owner fixture verifies changed local and ancestor settings refuse reservation, and replaying a reserved launch through the real shim after an ancestor changes produces no worker. Imported or older rows without proved origin remain ineligible. |

```sh
cargo build --locked -p swarm
python3 scripts/verification/rust_claude_resume.py --candidate "$PWD/target/debug/swarm" --provider /path/to/claude
python3 scripts/verification/rust_claude_resume.py --candidate "$PWD/target/debug/swarm" --provider /path/to/claude --board --worktree
```

The [Linux fixture launcher](../../scripts/verification/claude_native_linux.sh) accepts `rust_claude_resume.py`. The installed executable is exposed read-only, and the fixture uses a scratch-only profile and loopback inference. These runs did not reuse an installed login or test an externally managed profile. External Claude acquisition passed on Linux. Cross-provider handoff and macOS external acquisition have separate gates. [External Claude evidence](rust-claude-reattach.md) · [Codex resume evidence](rust-codex-resume.md)
