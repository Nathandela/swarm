# Linked Discord Discussions

The terminal board discovers the configured linked owner without blocking UI input. It qualifies namespaced manager and delegated worker rows, hides unrelated remote sessions and superseded rows, and preserves the current local selection. The dispatcher managers stay in their own section through layout and folding. Linked More and footer controls route attachment and lifecycle actions to the captured owner. Rename, local handoff, tagging and schedule-in-directory are local actions.

Before an action, the board resolves the active slot again and checks its exact socket, endpoint and installation binding and the current qualified row. It keeps a present session instance as a fence when both observations provide it. Older Go owners can omit optional capabilities on a later list. The board checks the unchanged namespaced ID, creation time, provider and lineage when that occurs. Attachment uses that same verified connection, requires the attach capability, and retains ACP authorization only when negotiated. Linked attachment has no local session cycle.

```sh
scripts/verification/parity/test_linked_board.py \
  --local "$go" --remote "$rust" --candidate "$rust" --fake-codex "$fixture"
scripts/verification/parity/test_linked_board.py \
  --local "$rust" --remote "$go" --candidate "$rust" --fake-codex "$fixture"
```

Both mixed-owner directions passed on Darwin arm64 and Linux x86_64. The witness provisions two actual owners, an actual admitted manager turn and a genuine public delegated worker. It attaches the previously selected local row, removes and restores the linked socket, checks the visible scoped lineage at 120 columns, attaches the delegated worker and manager, verifies the restricted More menu, rejects confirmation against a mismatching endpoint, then kills and deletes the linked manager through its own owner. The unrelated remote row stays hidden and running. The original local row remains running and the outer tty is restored.

The merged candidate also passed the independent ordinary ACP board permission responder on Darwin. The Linux comparison used a temporary executable tmpfs for binaries, both owners' state and the fixture UI.

| Host | Pinned Go SHA-256 | Rust SHA-256 | Offline Codex fixture SHA-256 |
| --- | --- | --- | --- |
| Darwin arm64 | `5f92beebd77f4d49a45ca7989a9d88d42bc3192b623356661217f102366fe179` | `cefdb615111be98939083813d294824ace2447718603630abe03a3724c4b4cd7` | `443328cdc053f63f3cfac5fb5cb7a7487f1952cea8c97465642956c0fd98bf6b` |
| Linux x86_64 | `5808d838158261470fb68cc97c12d041c4436a77a48746cd00ae75ffbec7f05d` | `791c648f5e3e0fb1ce03fc68ba36dd908e8fb76c798fc1b11fd9a5ef0717f2ab` | `f6b4888378c5581ba41e81d63c52ee6a1883d2edfc0a6a4fc4e2ccb03fba820d` |
