The test archive is the exact native-generated skills tree from installed Codex
0.160.0, pinned to OpenAI Codex commit
`a956835d020762cb2b570053af06f643a11c0ecc`. It was generated with synthetic
configuration, no credentials, no model turns, and isolated networking.

All 48 public source files match the pinned Git blobs under
`codex-rs/skills/src/assets/samples/`. The remaining regular file is the native
system-skills marker. Bundled licenses remain in the archive; adjacent LICENSE
and NOTICE are unchanged from the pinned repository root.

The tree has 72 nodes, 49 regular files and 307566 regular-file bytes. Its pinned
path/type/content SHA256 is
`8feb5b0889075ff695465a068fbeec422e3d27c8074a48af16185f92e8ac406c`.
The compressed archive SHA256 is
`b5d279af109bb6ef859b3d784549dd3fbb3ff1eb5679f592a47a9a9c31eae449`.

Run `go test ./internal/codexstock ./internal/accountconfig ./internal/accounts`
to exercise the validator, adoption and retirement contracts.
