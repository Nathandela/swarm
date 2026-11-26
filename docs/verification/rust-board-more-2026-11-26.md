# Populated board More menu

Pinned Go source is `f26648d6`. The paired witness opens both boards against the same live, isolated supervisor. Each board receives the same projected source ID, path, provider, and attention state. The receipt records complete styled cells and raw pane captures, with no substitutions for identity, directory, color, or whitespace.

```sh
scripts/verification/parity/test_board.py --tmux "$(command -v tmux)" \
  --grid-evidence "$fresh_receipt" --supervisor "$owner" \
  --reference-board "$pinned_go" --board "$candidate"
```

The witness covers 120×24, 80×24, 40×8, and 24×4. It compares the named source and complete menu, pointer Down and Up, a click beyond the selected command, and an 80×24 to 24×4 resize with the Rename selection preserved. It also exercises the Back footer to return to the selected row. The comparator checks every cell and the complete mode set exposed by tmux, including actual cursor coordinates and visibility. Each of the six arms returned 17 exact frame pairs and zero surviving fixture processes.

| Host and terminal | Frozen Go binary SHA-256 | Frozen Rust binary SHA-256 |
| --- | --- | --- |
| Darwin arm64, tmux 3.6a | `5f92beebd77f4d49a45ca7989a9d88d42bc3192b623356661217f102366fe179` | `8313acd485bfc9b32a1a21b2f55c44466ca753a6b569b89ea12bbf881b65acce` |
| Linux x86_64, tmux 3.2a | `5808d838158261470fb68cc97c12d041c4436a77a48746cd00ae75ffbec7f05d` | `52dd6c2bb048f9ea720957a6f590f0cccf75a450d9bdedae8c68e73ade61ce07` |

| Owner and board pair | Darwin receipt SHA-256 | Linux receipt SHA-256 |
| --- | --- | --- |
| Go owner, Go/Go boards | `59fadb8c9abc5cd20afc86608a03b29a48320fb841a1540409ec7860ca250d47` | `10d1485fcd85683f3f494d0b21b7c1b9e740154dc01f4de7a7e1de54eee7dd48` |
| Go owner, Go/Rust boards | `886795a88b934b2f3c7fb5da14020a249d6e3439d4c205b488be2b7b0b0ee799` | `17ca7307d14ad3b4357029a105ac315b4d12737d44fe0ccf77ca19c0e4cf5336` |
| Rust owner, Go/Rust boards | `f828a5ad43883e16c49c7fc1e3651fc9dcf232beed90d6db6e4a2630a267dd24` | `7562bebdbe3d08e342ca3c5b46c82ed46fde5923bc5ccbfe406d8013cab3b573` |

The tested menu target is a running Claude fixture in the permission state. The paired oracle follows selection and viewport state. Mutating action outcomes and provider-specific stopped rows are covered by separate cases. This slice does not close the general-board animation, edit and confirmation variants or the owner-backed Options rows.
