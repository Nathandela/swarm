# Rust dashboard daemon version skew

The pinned Go reference is v0.29.22 (`f26648d6`). The Rust dashboard and owner were built from the source changes referenced in each section, with `SWARM_BUILD_VERSION=0.29.22` and `SWARM_BUILD_VERSION=0.29.23`.

## Restart and general roster

| Native host | tmux | Go 0.29.22 SHA-256 | Rust 0.29.22 SHA-256 | Rust 0.29.23 SHA-256 |
|---|---|---|---|---|
| macOS arm64 | 3.6a | `d4b6db161ee5595c5e887a778cef0b4e859c7e3395405b9baec07f7c129f1be0` | `ffb412ed2b7d1926a68f81efe171e5582bbae6074dc21c0f1b4d0b61dccad70f` | `83113d42f60b61b73cab53b3b96c8ae1f3ccc164040e33c468bb6de9cac7982f` |
| Linux x86_64 | 3.2a | `5808d838158261470fb68cc97c12d041c4436a77a48746cd00ae75ffbec7f05d` | `9081887be007ee9236efa72beaf476bf8d21c14cb4fb37b4c9152fc163062f6e` | `901260e7165d79f14425a1a0bb5a9d5fd58554f08a28c2c638363920828fdf9e` |

Each host passed all five disposable PTY scenarios.

- `notice` compared an original Go and Rust dashboard against the same actual newer Rust owner. Every visible cell, style and captured terminal mode matched at 80×24, 40×8 and 24×4. The owner retained its original PID and build.
- `upgrade` started a real worker with the older Rust owner and opened the newer Rust dashboard. The board replaced the owner, kept the same endpoint and worker PID/birth, and attached the worker again. Input reached the fixture and terminal settings were restored on exit.
- `quit` quit while the upgrade banner was painted, verified terminal restoration and the replacement owner, then reopened the board and drove the same worker.
- `foreign` kept the selected socket fixed but pointed the board at a different private state directory. The original owner and worker remained alive and usable.
- `refuse` selected the pinned Go owner, which lacks the negotiated Rust restart operation. Its PID and hello remained the same and the worker stayed usable from the board after refusal.

Run `scripts/verification/parity/test_version_board.py --mode <case> --supervisor <owner> --board <board> --evidence <new.json>`. For `notice` select the newer Rust owner and older Rust board and also pass `--reference <pinned-go>` and `--tmux <tmux>`. For `upgrade`, `quit` and `foreign` select the older Rust owner and newer Rust board. For `refuse` select the pinned Go owner and newer Rust board. Each arm owns its scratch socket, private state, fixture binary and terminal. Cleanup reported zero survivors in all cases.

The mixed older Go owner still requires its matching restart command.

## Composed forms

The live `notice` fixture also compares Launch and Options against the pinned Go dashboard on the same newer owner. The warning occupies the penultimate row, with the status footer on the final row. Launch and Options retain their 24×4 resize gate. Shared viewport budgeting covers Handoff, Relocation and compact rename/tag/confirmation sheets, including the rows available to read before confirming.

| Native host | tmux | Rust 0.29.22 SHA-256 | Rust 0.29.23 SHA-256 |
|---|---|---|---|
| macOS arm64 | 3.6a | `8fd29ca807643039621bb5f60c63bf7e22f3ac06512b3166c961223d06764f0b` | `637be7c9262ed0584687ff2d14638011611ddcfb834baa28843e6487eb2936cf` |
| Linux x86_64 | 3.2a | `00fd31fb3695fade7d237642c03b1b6ce6d025e0cf356767c555d13d52768192` | `9c9bf0ff0c42449dcf59ec78782eef822b1274fb2448e580ff2176f078f486ec` |

All Launch and roster cells and styles matched at 80×24, 40×8 and 24×4. Options matched apart from the deliberately retired Go Provider logins row at 80×24. The fixture checks the exact Go row and empty Rust row and preserves both raw frames. Launch keeps the cursor hidden. The receipt records its coordinate differences after Go's incremental repaint and compares the remaining terminal modes. Enter at 24×4 left the live owner's roster empty in both arms, and Esc returned to the roster.

## Proposed prerelease correction

The pinned Go dashboard orders whole prerelease suffixes lexically. That makes an owner on `0.29.23-rc.10` look older to a dashboard on `0.29.23-rc.2` and can trigger a downgrade. Rust compares dot-separated prerelease identifiers with numeric precedence, matching the native updater on recognized release labels. A semantically newer owner gets the existing passive notice. Stable release ordering, layout and restart flow keep their existing behavior. Malformed prerelease suffixes are ineligible for a board-initiated restart. This intentional correction is pending Nathan’s release-contract signoff.
