# Rust dashboard daemon version skew

The pinned Go reference is v0.29.22 (`f26648d6`). The Rust dashboard and owner were built from the source accompanying this receipt, with `SWARM_BUILD_VERSION=0.29.22` and `SWARM_BUILD_VERSION=0.29.23`.

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

This receipt covers the persistent newer-version notice on the general roster and the actionable older-version restart. The Go composed form notice is a separate visual case pending qualification. The mixed older Go owner still requires its matching restart command.
