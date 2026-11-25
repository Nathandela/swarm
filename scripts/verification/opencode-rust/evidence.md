# OpenCode 1.19.3 qualification, 2026-11-25

The native runs used the Rust source at `46409417acaadc5ac5f6cf979bb390a0b8d3bdc8`. The runner adjustments in this evidence revision leave the Rust source and pinned Go bridge unchanged. Paths, session IDs, authentication tokens and raw native records are omitted.

| Gate | Result |
| --- | --- |
| Rust | 35 unit tests and 7 integration tests passed. One pre-existing ignored test remained ignored. Clippy passed with warnings denied. |
| Pinned Go-reference bridge fixture | 162 Node tests passed. The pinned bridge asset and its fixtures are unchanged. |
| Installed Linux x86_64 | Bubblewrap isolated both TCP/IP stacks and all test processes. Only loopback was reachable. Four model requests reached the generated fixture. |
| Installed macOS arm64 | The guard rejected IPv4 and IPv6 egress. The initial TUI bound the exact root, projected idle from the native observation stream, and returned history and requests. Daemon rejoin returned the same root and incarnation. Resume returned the same root and creation time and a fresh incarnation after confirmed termination. |

The Linux binary SHA-256 was `9ff0d787e6d878148215e56550f69b928f30025175a8e853952812e7fbbec61d`. The macOS binary SHA-256 was `c8d79523f73e3de6e143088e8224a540969288a39098d4b4fc7be60d79b03bed`.

In the Linux run, the real TUI admitted the initial and semantic inputs. The daemon restarted while native requests were pending. A wrong card hash was refused. An invalid question group produced a bound no-effect rejection before the same pending request was answered. The permission and ordered question receipts reported `admitted`. The semantic operation reached `completed`, an exact repeat returned the same receipt, and an existing composer draft refused a later operation without adding native history. History pages returned distinct native rows. Resume retained the root, creation time, and history under a new incarnation.

The digest checks used arbitrary numeric metadata and reordered object keys. Changing answers or adding a wire field changed the digest. Reordering object keys preserved it. Null and foreign incarnations were refused. Reading the operation afterward confirmed that digest computation had created no receipt entry.

An early exploratory Mac run inherited an existing daemon socket and queried its roster. It performed no production mutation. The committed runners set explicit private state and socket paths and verify the derived owner endpoint before launch.

Worktree-backed resume, historical child transcripts and 24/72-hour runs remain outside this evidence. The model provider in Linux returned fixture data. The macOS run exercised the no-model lifecycle.
