# Native Codex relocation after checkout deletion

The same fixture ran against pinned Go `f26648d69086e329132ac7849954d2f29a895387` and the Rust candidate on native macOS arm64 and Linux x86_64 with genuine Codex 0.172.0. Each arm used an isolated home and loopback model. The provider completed exactly four marked requests.

The first launch requested read-only sandbox mode against a workspace-write fixture default. After stopping and reaping the first app-server, TUI worker and shim, the witness moved the original checkout out of its recorded path. It selected a different trusted checkout and entered a real terminal turn. It subsequently stopped and performed two ordinary resumes into the selected checkout. The relocation request sent only `resume_from` and `resume_relocate`. Ordinary resume sent only `resume_from`. For both Go and Rust, all four native `turn_context` events recorded read-only sandbox mode. Their checkout sequence was original, selected, selected, selected.

Both implementations retained one outgoing native thread and the original rollout path, inode and byte prefix. The Rust arm also changed its synthetic auth before relocation. Rust refused the launch with an authority error. The roster, rollout bytes and loopback request count stayed unchanged. Restoring the original synthetic auth allowed the positive relocation.

| Host | Implementation | Swarm binary SHA-256 | Original rollout prefix SHA-256 |
| --- | --- | --- | --- |
| Darwin arm64 | Go | `9b6837319c7a52d4105fa887b46d640b0111c2a580611e79b616003a02f59692` | `2b7c5c9d8d70a394f47c541ba830b9e3defa4a4fe4f1862979c13a2a41158634` |
| Darwin arm64 | Rust | `9378dd77792cd9515fb3b4b018cd56e00c65855100e9fa413e3aff9f2a50b4ee` | `9ca07a65877ca1978c8bb50e23d2dbecbaea4a9e53f63ea6007b8d32f7ec0cc0` |
| Linux x86_64 | Go | `5808d838158261470fb68cc97c12d041c4436a77a48746cd00ae75ffbec7f05d` | `05707129b6caceca7247c6c7ac778352bb55fcc938dd089fa8a32135cc3279f3` |
| Linux x86_64 | Rust | `5f1d18798abd9ef608b0dce76710e8a08308278582350a594108889f82065339` | `69de6f727abe9c6bf3a9ef9dcd012b8279f8185e4221c61e4ec6f9320d4bc4a7` |

Rust implementation evidence was captured from base `da91c24e` with the candidate product patch at SHA-256 `17de2ec41cfddbf6701a2ff598a300b05b04bfbfdeb1843b29872713777f80c7`. Both Darwin proof builds additionally used the existing fixture-only identifier injection to constrain native Codex aliases to exact socket paths. Linux used the unmodified candidate and the Go release executable.

The independent review strengthened the original-rollout check in `21147857`. Both native hosts reran the four-turn scenario. Rust refused a byte-identical copy of the rollout at its original path. The fixture also removed the stored observation proof to simulate a historical record, and the relocation refused before a new worker, transcript write or model request. Same-checkout resume still accepts historical observations. Relocating a conversation to a different checkout requires a recorded original rollout. The final receipts have SHA-256 `2a7b12ecd9bf2e853dd8ed8d40140af0f751c467d15c551ce2753f781d22cdaa` for the paired Darwin run and `1e47b233d4a23b7cb5293086da73f19725fbe6c0dccd1ab96cc8a2f1a4b388ed` for Rust on Linux.

On Darwin, run `scripts/verification/run-macos-live-go-codex.sh /path/to/codex --relocation` from the source commit under qualification. On Linux, stage the selected Swarm executable as `swarm` beside the verification scripts and `parity/`, then invoke `sh provider_native_linux.sh /path/to/staged-fixture /path/to/codex codex_relocation.py go` or the same command with `rust` for the Rust arm. The wrapper starts a fresh network and PID namespace with a loopback-only model and a disposable writable root.
