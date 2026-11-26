# Roster attention and lineage

The board displays a four-second notice for transitions into Review or Needs Input, including a change among permission, prompt and error. The notice uses the board's shared row geometry so narrow views keep their pinned footer and pointer hit targets. The initial roster paints without announcing its already-present rows.

Wide rows display scoped parent labels, superseded attempts, children, manager and diagnostics under the existing summary width. Compact rows retain the manager prefix and bounded diagnostic tokens. Lineage and supervision only resolve within the row's owner endpoint.

The native Darwin Go and Rust `--roster-motion` witnesses observed a real fixture permission transition after resizing to 40×8. They captured the visible notice on row two and its expiry while the queried owner still held the approval state. Unit cases cover an attention-kind change inside one group and a superseded parent beside a foreign owner with the same local ID.

```sh
scripts/verification/parity/test_board.py --tmux "$(command -v tmux)" \
  --roster-motion --grid-evidence "$new_receipt" --supervisor "$owner" --board "$candidate"
```
