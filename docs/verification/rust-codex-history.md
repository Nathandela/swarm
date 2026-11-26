# Rust Codex owner history

`swarm history <session> --limit 1` returns the newest retained interaction and its stable `item_id`. Pass the ID as `--before` to page strictly older. A cursor outside the retained window returns an empty page at the history floor. Pilot view and watch use the same durable owner journal and treat native content as worker data.

The private backend observes completed user and final assistant messages for the exact ordinary Codex thread and turn. The first reply can finish before the observer joins. On join, a bounded opening backfill reads the exact authenticated rollout and requires matching native root-turn, thread, item completion and final text evidence. The journal retains plain text up to 4096 bytes per item. Existing rollout history for a viewer newly resumed from another row, unsupported message types, and messages outside retained bounds are unavailable through this journal.

On 2026-11-26, installed Codex 0.172.0 on Linux x86-64 passed `parity_codex.py --native --loopback-model --history-check` with a disposable native home and synthetic inference. The public CLI paged from the reply back to the original prompt, safely handled a forged anchor, and Pilot viewed and replayed the same reply from the user-message cursor. No fixture processes survived. macOS Clippy and journal unit tests passed.
