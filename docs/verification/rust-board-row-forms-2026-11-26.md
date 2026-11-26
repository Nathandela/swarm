# Session row editing and lifecycle confirmation

Pinned Go source is `f26648d6`. The comparative witness runs both public boards against one live, isolated supervisor. The permission-state Claude fixture and both review-state background rows are created through public spawn and hook entry points. The edit witness uses review for the second background row to keep the compared scenes stable across arms. The ordinary roster witness separately retains Working and its animation.

```sh
scripts/verification/parity/test_board.py --tmux "$(command -v tmux)" \
  --grid-evidence "$fresh_receipt" --edit-grid --supervisor "$owner" \
  --reference-board "$pinned_go" --board "$candidate"
scripts/verification/parity/test_board.py --edit-rows --supervisor "$owner" --board "$candidate"
```

The comparative witness checks Rename, an edited name with two wide characters and a moved insertion cursor, Tag, and kill confirmation. It uses 80×24 inline rows, 40×8 compact sheets, and the 24×4 resize-before-mutation view. It compares all cells and captured terminal modes, including cursor position and the handoff from board mouse reporting to native terminal selection during the form. No cells, IDs, or paths are substituted.

A second witness saves a name using clear-left and Meta+Left, saves the tag against the same session ID, verifies that the tiny viewport refuses to kill, cancels a readable confirmation, kills the original session, and deletes the stopped row. It checks the public roster after the mutations and verifies the original complete `termios` settings after exit. All its Go/Go, Go-owner Rust board, and Rust/Rust runs passed on both native hosts.

| Host and terminal | Frozen Go binary SHA-256 | Frozen Rust binary SHA-256 |
| --- | --- | --- |
| Darwin arm64, tmux 3.6a | `5f92beebd77f4d49a45ca7989a9d88d42bc3192b623356661217f102366fe179` | `506cb79bc89ef42e28a81ba8b98d896b0ec19d22d0777b39a1f2d4990208e4f3` |
| Linux x86_64, tmux 3.2a | `5808d838158261470fb68cc97c12d041c4436a77a48746cd00ae75ffbec7f05d` | `f6abb9e3ab307ca85f1ef154c510715ed0554e75da40e1d3eee4bd785208ef14` |

Each paired arm returned 12 exact frame comparisons. Go and Rust display their actual matching build versions, `dev` on Darwin and `0.29.22` on Linux.

| Owner and board pair | Darwin receipt SHA-256 | Linux receipt SHA-256 |
| --- | --- | --- |
| Go owner, Go/Go boards | `be31eeca5580de38b672507249b276782402d609b199df15b1966ccfdcae17db` | `3accd18472e8b49505f2593a6d185b38d9bd991d4f68746597dec6325f096ab9` |
| Go owner, Go/Rust boards | `b7d21263a622592c639b5e75d79f2a5ded9329787717da5b624b2d29c00f80a6` | `028b19c49dca0c57af3edad4f2d0b092e9b82aa2472fd79ff8a64a73b17f1b75` |
| Rust owner, Go/Rust boards | `ee07355a098ce769c1ff92ec60d2e1ff24a5aaf5c7370d9684fc6ad013df9b7e` | `83673734473bb102f6db0fd79b7864af55b24b33022651eb2f1b158b0a1d3069` |
