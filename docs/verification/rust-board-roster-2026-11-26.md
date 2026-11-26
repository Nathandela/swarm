# Ordinary roster and retained Options

The fixed roster case runs both boards against the same isolated public owner and actual fixture sessions. Permission and review events and the stopped row use the same captured identities and directory. For deterministic cells, the second live background row enters Review so the scene has no spinner. The separate motion probe uses the ordinary mixed fixture, including Working.

```sh
scripts/verification/parity/test_board.py --tmux "$(command -v tmux)" \
  --grid-evidence "$static_receipt" --static-grid --supervisor "$owner" \
  --reference-board "$pinned_go" --board "$candidate"
scripts/verification/parity/test_board.py --tmux "$(command -v tmux)" \
  --grid-evidence "$motion_receipt" --roster-motion --supervisor "$owner" --board "$candidate"
scripts/verification/parity/test_board.py --layout-options --supervisor "$owner" --board "$candidate"
```

Each paired arm captured 28 complete frames. The four geometries were 120×24, 80×24, 40×8 and 24×4. The scenes were initial, folded, expanded, Down and Up selection, attention navigation, and End selection. The wide rows use both real boards concurrently and acquire an actual common elapsed interval from their unmodified screens. An unbound key requests redisplay when the idle Go view is holding an earlier elapsed value.

The Go/Go, Go-owner Go/Rust and Rust-owner Go/Rust pairs on Darwin arm64 and Linux x86_64 matched all cells and all captured modes other than hidden renderer cursor coordinates. Full original coordinates remain in all raw receipts. Each Darwin cross-board arm recorded 20 hidden coordinate differences. Each Linux cross-board arm recorded 18. Cursor visibility, alternate screen, keyboard and mouse modes matched in every paired frame.

The genuine 120-column motion fixture observes the Working row's colored spinner and elapsed column from the rendered grid while no keys are sent. The independent Go and Rust arms each showed at least four phases and two nondecreasing elapsed values on both hosts. The sample sequences, complete raw frames, actual times and identities remain in the receipts.

The public Options layout case chose repository grouping with name order, then tag grouping with arrival order. It canceled a later edit, restarted the board and checked the durable layout and the original termios. The Go/Go, Go-owner Rust board and Rust/Rust cases passed on both hosts.

| Host and terminal | Pinned Go SHA-256 | Rust static roster SHA-256 |
| --- | --- | --- |
| Darwin arm64, tmux 3.6a | `5f92beebd77f4d49a45ca7989a9d88d42bc3192b623356661217f102366fe179` | `253addca7b9ecacad3d260cf9f4d8e916803e052b7f02d9cc21dcf26d2902a2d` |
| Linux x86_64, tmux 3.2a | `5808d838158261470fb68cc97c12d041c4436a77a48746cd00ae75ffbec7f05d` | `b13220a6a1a412e0bdc2ad554312c57deffd90dc7e2c787d2f59b58e1a3cfa2a` |

| Owner and boards | Darwin static receipt SHA-256 | Linux static receipt SHA-256 |
| --- | --- | --- |
| Go owner, Go/Go | `37fd0c75dfbd2993677782e2deb5e5576342b8962c700889e0e4f7de2d27b0cf` | `66fbc63b4596f07bf9d5f04d43853000a575eea2a64f1106b1b8d200ebbf6186` |
| Go owner, Go/Rust | `0bd4947cc2ca2dfc0c55209b15115fa50a53b61f43a2597b097e0cc65a8fb082` | `b65d8bf064a1dd0f5230d4015f3834701ba4a74349845c10fe6f164a0424add0` |
| Rust owner, Go/Rust | `03adc05044789d8a70e1811667db667bc18d80e44a9839038bb11866bdadaaa7` | `826d5dba8167ae993af2ae5ad47b1fcde9d614ecec2b4b671a8d2ed11334d050` |

## Desktop Options comparison

```sh
scripts/verification/parity/test_board.py --tmux "$(command -v tmux)" \
  --grid-evidence "$options_receipt" --context-grid --supervisor "$owner" \
  --reference-board "$pinned_go" --board "$candidate"
```

Each pair checks ten complete frames. It focuses Manager Prompt, Auto Compact and Threshold on both desktop sizes, then Auto Compact and Threshold at 40×8 and 24×4. The original provider-login row is visible in the Go desktop captures. The comparator records the whole named row where Rust intentionally leaves it blank, and fails on any difference in another retained cell. Each cross-board arm had six recorded retired rows and one hidden cursor coordinate difference. All compact coordinates matched in the settled sequence. The raw receipts preserve every cell and line on both sides.

| Host | Frozen Rust SHA-256 | Go/Go Options receipt SHA-256 | Go-owner Go/Rust receipt SHA-256 | Rust-owner Go/Rust receipt SHA-256 |
| --- | --- | --- | --- | --- |
| Darwin arm64 | `059c60e0c694e2c7c834a4404b364a150937bf4105351fd9d56913f567dae622` | `7d827e649ce45c9adec0008fa94153c5fd649b4b009230f0d93a3259c225f6f9` | `6ab72c26bec1aae387ca5e0168e2ad426d64885cf289dcd392b5ac9a8cd1b193` | `e715c249f661f2d360275470339237b7852095024480abb10c4ca7c4d2dc912e` |
| Linux x86_64 | `6666ff3912fb9a8660c815795dd1ce7c41f58028df3b169f87de8ef7b4945b5f` | `b2461199488a7550d9d171758ec865cf26523f929c46a2dfb850fb3cec82e9bd` | `d8f27f26a1816098a946ee211cdcfad4543d04441643f1908e56113cc4c5b46c` | `ba7aba5ea62800ca724e3e345cd461934e77e30b448c99f2566ac34951c3dd9a` |

The independent motion receipts have SHA-256 values `fff5c965f3ae14d7d4145500b73339a66b9e887d0f1649fc10a967059f4f02f9` (Go Darwin), `9a9de6e75e7351df78d011720d9048b2159ed6c7ee3c3572dbabcc15d36c9606` (Rust Darwin), `4abcd96d750ff727cbcf4eae4711657b7e437d28e441c75bf5409a8e802beb9b` (Go Linux), and `2eb1cc9800dc2e0485e8769126c1f1ecbbac8fa2b16c9d80c1228631dd751b2b` (Rust Linux).

## Retained operator surface and open contracts

| Surface | Status and blocking contract |
| --- | --- |
| Main dashboard, More and Options | Group/fold/order/attention and viewport, pointer actions, editing and lifecycle confirmation, five retained Options controls, live attach/cycling, scheduled Enter, and native Antigravity relocate have public evidence. The retained owner-free presentation gaps are transient attention banners, complete lineage and diagnostic badge rows, and the hidden renderer cursor coordinates above. |
| Linked Discord Discussions | The pinned board obtains a second local owner from the selected Discord slot, verifies its expected endpoint and polls its roster. The board still needs this linked-owner descriptor and bound list/attach/Kill/Delete routing in Rust, including previous-supervisor custody. Rust already projects manager and worker metadata. Manager conversation identity and source/supersedes lineage need to drive the linked section and target actions. |
| Missing-workspace native resume | Antigravity checks exact source and destination through the negotiated owner. Claude, Codex, OpenCode and Hermes need provider-specific owner relocation contracts before the board can issue that action. Ordinary matching native resume remains available. |
| Automations pointer input | Keyboard create, edit, pause/resume, trigger, history and archive use the existing typed catalog. Pointer actions and wheel navigation still need board routing to those same operations. Broader scheduled provider readiness remains with the respective provider owner. |
| Provider-native sign-in recovery | The fenced native refresh/relogin path is a CLI surface. The original Go screen's historical provider-login account switcher and mobile controls are deliberate product exclusions. |
