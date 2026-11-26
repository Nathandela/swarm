# Scheduled review and Antigravity resume relocation

The retained board's Enter action reopens a stopped scheduled review through the same native resume request used by the keyboard's `r` route. The request carries the captured original identity and label. A scheduled review keeps its original directory and never becomes an interactive relocate request. The error path names the failed scheduled reopen.

An ended Antigravity row with an absent workspace can open the relocation form when the selected owner negotiated `resume-relocate`. The form begins at the closest existing parent, shares the launch directory editor and completion, and only accepts an existing destination from a readable viewport. Revalidation checks the captured row's identity, provider, original directory, conversation and stopped state before admission. A missing or changed row produces a refusal. An owner rejection leaves the captured source and the attempted directory in the form. Escape discards the entire draft.

```sh
scripts/verification/parity/test_board.py --relocation-board --supervisor "$owner" --board "$candidate"
scripts/verification/parity/test_board.py --scheduled-enter --supervisor "$owner" --board "$candidate"
scripts/verification/parity/test_board.py --tmux "$(command -v tmux)" \
  --grid-evidence "$fresh_receipt" --relocation-grid --supervisor "$owner" \
  --reference-board "$pinned_go" --board "$candidate"
```

The relocation case drives two actual classic fixture workers which emit distinct native conversation identities. It fails and cancels a destination, deliberately resumes the neighbor, and checks its original cwd and exact `--conversation` argument. It then reopens the original row, checks the 24×4 refusal, completes a qualified directory prefix after resizing, and confirms that the new provider receives only the original conversation at the new cwd. The unit fixture removes an active relocation source and verifies that Enter cannot act on the new selection.

The scheduled case seeds the completed-automation marker in an isolated stopped fixture's actual persisted owner state. Native spawn, shutdown, reopening by Enter and exact provider argv go through public behavior. This case covers the board and owner resume boundary. Scheduler production of the marker is a separate test. The six action arms for each scenario passed on both native hosts, including restored termios. The Go/Go and Go/Rust paired grid arms under both owner implementations returned 18 exact full initial relocation frames at 80×24, 40×8 and 24×4.

| Host | Pinned Go SHA-256 | Rust SHA-256 |
| --- | --- | --- |
| Darwin arm64, tmux 3.6a | `5f92beebd77f4d49a45ca7989a9d88d42bc3192b623356661217f102366fe179` | `be1ef25427689f4ab0299c361f01805db606ba50cee33c08997a12fba6f383f3` |
| Linux x86_64, tmux 3.2a | `5808d838158261470fb68cc97c12d041c4436a77a48746cd00ae75ffbec7f05d` | `f6220ef932505417a8b04e09147ada683483272d3343a7b03051386e03b43be2` |

| Owner and boards | Darwin receipt SHA-256 | Linux receipt SHA-256 |
| --- | --- | --- |
| Go owner, Go/Go | `d0445454fdddcdfa922eb34ab3ed02b1c4c3db599840afcb6ceba977c06ecb7e` | `fd9c1d58ad44c54ac1046dce85034d713cfbdbdcf247fdfc8ec73f1c35a7c8ae` |
| Go owner, Go/Rust | `21326986bd3ea8c6f09b72e2f9f0602c17a46b21b932f0218bb1f1be40ef8956` | `4430ee0dc8df9d1bf45fdf9cfe57e357fed567551cf97ea4c1b8a9cbc74cbb84` |
| Rust owner, Go/Rust | `335c43bb973efa5a7c87bc16d0c12d449cc144e242c912ff2ef4bac5fc4e5bf5` | `8131dda023d51d009079e88a6dcfef158f6299ae1f800f96e388223b3100c07b` |

The Rust owner currently implements verified missing-workspace relocation for Antigravity. Claude, Codex, OpenCode and Hermes retain their ordinary native resume where available and visibly refuse relocation when the original workspace is gone. Each needs an owner-side provider custody route before it can submit relocation.
