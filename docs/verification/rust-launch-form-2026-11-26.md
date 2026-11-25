# Rust launch-form cell parity, 26 November 2026

Six launch-form frames matched the pinned Go implementation in every cell on
native macOS arm64 and Linux x86_64. Each host captured the cold directory and
settled Claude picker at 80×24, 40×8 and 24×4. The comparison retained glyphs,
cell widths, continuation cells, attributes and resolved foreground and
background colors. Enter at 24×4 left both rosters empty.

The Rust source tip was `634a7384`, based on `bcc656a6`. The Go macOS fixture used
the source-built Go main reference at `f26648d69086a`. Linux used the verified
Go archive reporting `0.29.22`; the Rust release build used the same display
label. The macOS comparison used two development builds reporting `dev`.

| Host | Terminal engine | Go binary SHA-256 | Rust binary SHA-256 |
| --- | --- | --- | --- |
| macOS arm64 | tmux 3.6a | `5f92beebd77f4d49a45ca7989a9d88d42bc3192b623356661217f102366fe179` | `35470ec59554c0396e44b695dd72d916579cd2eb9d81d52f8952edaa7e9cd61a` |
| Linux x86_64 | tmux 3.2a | `5808d838158261470fb68cc97c12d041c4436a77a48746cd00ae75ffbec7f05d` | `7ebc2be3385e1a29ccd27d00cca26b1f2a1cc2a3b4b6c9641869687de7e23875` |

Each arm ran in a separate lifetime at the same freshly created private
workspace path. The version fixture held its reply until the cold frame was
captured. After release, the driver waited for Claude on the focused picker.
The comparator made no substitutions. Both hosts reaped every fixture.

To reproduce, follow [the local parity harness instructions](../../scripts/verification/parity/README.md)
and run `test_board.py --form-grid` for both builds with the same
`--form-root`. Compare the independent receipts with `--compare-forms`.
The raw macOS receipts are `/private/tmp/dashboard-exact-go-1.json` and
`/private/tmp/dashboard-exact-rust-final.json`. The Linux receipts are
`/tmp/swarm-dashboard-exact.yz50vw61/{go,rust}.json` on the isolated test host.

This result covers one offline provider picker and the six listed frames.
Additional provider states, option editing, and other dashboard screens need
their own cell witnesses.
