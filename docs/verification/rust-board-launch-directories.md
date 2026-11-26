# Rust launch directory selection

The pinned Go reference is v0.29.22 (`f26648d6`). Both boards and both owners were launched from a disposable fixture directory. The Go and Rust arms reused the same physical path on each host, and the captures compared every resolved glyph, cell width, attribute and color.

| Native host | tmux | Go SHA-256 | Rust SHA-256 |
|---|---|---|---|
| macOS arm64 | 3.6a | `d4b6db161ee5595c5e887a778cef0b4e859c7e3395405b9baec07f7c129f1be0` | `c79edce0920f2cd862504774312f091bf4f71c3c080c5794b8a562869aabd687` |
| Linux x86_64 | 3.2a | `5808d838158261470fb68cc97c12d041c4436a77a48746cd00ae75ffbec7f05d` | `00f7e602609fed85489750edb549d0c8b4e73bba0c5a0687419f37fb415b7ba6` |

All twelve frames matched at 80×24, 40×8 and 24×4. They include cold directory focus, settled agent focus, the `ponit` typo prompt, the ranked `point` selection, and the narrow long-confirmation viewport before and after reading. The long form refused Enter before the decision was visible. Each actual owner launched the corrected worker in `point`, preserved the absence of `ponit`, and started a second worker in a new path after a fresh consecutive Enter confirmation. Moving focus between confirmation presses reset that intent. The small terminal continued to refuse launch.

Use the two arms and compare their evidence.

```sh
scripts/verification/parity/test_board.py --form-grid --supervisor "$go" --board "$go" --tmux "$tmux" --scratch "$scratch" --form-root "$scratch/form" --grid-evidence "$evidence/go.json"
scripts/verification/parity/test_board.py --form-grid --supervisor "$rust" --board "$rust" --tmux "$tmux" --scratch "$scratch" --form-root "$scratch/form" --grid-evidence "$evidence/rust.json"
scripts/verification/parity/test_board.py --compare-forms "$evidence/go.json" "$evidence/rust.json"
```

Rust standard launch, attach and post-daemon-death reattach passed on both hosts. Native Antigravity relocation also passed on macOS after changing the shared prefix selector.
