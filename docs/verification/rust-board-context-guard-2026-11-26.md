# Codex Context Guard Options

The Options controls negotiate `context-guard-settings` with the selected local owner. They load a typed policy and submit an explicit draft using the owner's exact revision. The board retains its normal navigation and Manager Prompt while a settings request is pending. It refuses to apply if the owner refused or the selected owner changed. A same-owner reconnect reloads durable policy before allowing a new apply.

```sh
scripts/verification/parity/test_board.py --context-options --supervisor "$owner" --board "$candidate"
scripts/verification/parity/test_board.py --tmux "$(command -v tmux)" \
  --grid-evidence "$fresh_receipt" --context-grid --supervisor "$owner" \
  --reference-board "$pinned_go" --board "$candidate"
```

The action witness verifies default disabled policy, Space enable, invalid threshold refusal, and successful apply at 92 percent. It stages another threshold edit, changes the policy from a second owner connection, and verifies the stale draft cannot replace the new revision. It checks an empty roster and complete restored termios after quitting. An earlier Rust owner without the capability also passed the hidden-control case on Darwin. Go/Go, Go-owner Rust board, and Rust/Rust mutation runs passed on both native hosts.

The paired witness compares focused Auto Compact and Threshold at 40×8 and 24×4. Each host passed all twelve comparisons across Go/Go and Go/Rust boards under both owner implementations. Every resolved cell and terminal mode is compared. The real desktop control flow is exercised at 80×24. The compact capture preserves the full retained screen without the retired Go desktop account switcher. A full desktop reference including that retired row and provider live-compaction behavior are outside these Options receipts.

| Host | Pinned Go SHA-256 | Rust SHA-256 |
| --- | --- | --- |
| Darwin arm64, tmux 3.6a | `5f92beebd77f4d49a45ca7989a9d88d42bc3192b623356661217f102366fe179` | `0f3bbceade542b9209ef57be7327b039bb362268ba1b903bd037c08f8a929f03` |
| Linux x86_64, tmux 3.2a | `5808d838158261470fb68cc97c12d041c4436a77a48746cd00ae75ffbec7f05d` | `09b0ca441ce4362a2c44431ec3be4e02d95d043fb8eef74f337dc75df7d7192e` |

| Owner and boards | Darwin receipt SHA-256 | Linux receipt SHA-256 |
| --- | --- | --- |
| Go owner, Go/Go | `acf140b28c7c13619440976164d9cdd5a6b72d43a752ebebaddfe88552d8da61` | `20fb52f045fe3b259b16b1fc2f86a3571d0626e86190a9db43bccb7c78cd1556` |
| Go owner, Go/Rust | `27d70cd0b0d5f069d11272c410c9cc329a054a5b98b9a1076d0ba6e2b9179f8f` | `7115525977d9b33fd0a05c4e1bc162c63c4f682c2fc022ecf468d8a37544a2d5` |
| Rust owner, Go/Rust | `397376d8c4abcb17c2d207b9eeeca51e8ddca4f13ec65af3f566ded60d434dcb` | `b6685757f50b97b0b3f095a11488881dfae65b6e1918b2c9d5079da84ffea3bd` |
