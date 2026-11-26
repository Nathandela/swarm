# Owner manager prompt in Options

The terminal Options form reads the selected owner-local private manager settings store and uses the same save function as manager admission. Enter saves the Manager Prompt independently from grouping, ordering and CLI-button choices. The prompt is loaded again on reopening. The field uses the shared terminal editor, and bracketed paste converts control characters to spaces after folding CRLF. The backend validates the 1,024-byte limit before replacing the file.

```sh
scripts/verification/parity/test_board.py --manager-options --supervisor "$owner" --board "$candidate"
scripts/verification/parity/test_board.py --tmux "$(command -v tmux)" \
  --grid-evidence "$fresh_receipt" --manager-grid --supervisor "$owner" \
  --reference-board "$pinned_go" --board "$candidate"
```

The public action fixture saved pasted multiline input as a trimmed single line, verified the exact owner file and its 0600 mode, and refused oversized input without replacing that value. It reopened Options and recovered the value, then showed the resize gate and refused to save from 24×4. It verified the restored original `termios` settings and an empty roster after quit. Go/Go, Go-owner Rust board, and Rust/Rust passed on both native hosts.

The paired witness compares the complete focused view at 40×8 and 24×4, including the owner notice, persisted value, caret, hide/restore mouse modes, and the actual terminal cursor position. The six native comparison arms passed with no substitutions and no surviving fixture processes. The 80×24 action fixture verifies real desktop controls and persistence. A whole desktop Options frame with the Go reference's other settings and historical login row is outside this compact comparison.

| Host | Pinned Go SHA-256 | Rust SHA-256 |
| --- | --- | --- |
| Darwin arm64, tmux 3.6a | `5f92beebd77f4d49a45ca7989a9d88d42bc3192b623356661217f102366fe179` | `f06ded73c0f2489b35dfd28e254f3c2ad12fc28befefa00b81dde3ea37c454cf` |
| Linux x86_64, tmux 3.2a | `5808d838158261470fb68cc97c12d041c4436a77a48746cd00ae75ffbec7f05d` | `305eadf8411ea583ab11a486e0aedc6ea3fd9869035687aac63d71b5e90616ea` |

| Owner and boards | Darwin receipt SHA-256 | Linux receipt SHA-256 |
| --- | --- | --- |
| Go owner, Go/Go | `757503a171a7ebaa3d5188739bc654715d8969078fc5b14e3d0dd1ea5e8baace` | `ced8ce13495c1b436846db5ed6476a028bdcca6d98f74ffc4cefc679691cdad4` |
| Go owner, Go/Rust | `df8e8f5e9d759e50045061a41c1e7311f6791db31fb75056b2a1ec7f99f2c75c` | `623747e0f277811d0614ce59061133e6bbeec5057ef4503f008e73b369e1f1c9` |
| Rust owner, Go/Rust | `6d33dcc0ef87479f9a728fdf441c0bf45be83ba29205fc39261f52f86a318c6d` | `4dd7fb3df3c5c66586f7288e5cf8ab916707f74e8aaa2165c6a988b32b2ed461` |
