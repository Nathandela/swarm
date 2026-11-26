# OpenCode admission boundary probes

Run the probes with Node 22 or later and the production source transform from the candidate under test.

```sh
cargo test --locked --bin swarm providers::opencode::tests::native_admission_boundary_fixture -- --ignored --exact --nocapture
```

The Rust test writes the transformed bridge to private scratch and gives its path to the Node suite. The three cases use the real bridge protocol on a Unix socket and deterministic SDK and storage fixtures. No shipping code reads a fault selector. The normal Node test command still runs the untransformed reference asset tests.

| Case | Digest HTTP | Bridge dispatch POSTs | SDK reads | SDK prompt calls | Exact native user rows / text parts | Assistant rows |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Route no longer authorized at digest | 422 | 0 | 0 | 0 | 0 / 0 | 0 |
| Successful digest, SDK status read stalls | 200 | 1 | 4 | 0 | 0 / 0 | 0 |
| Successful digest, late native user and part | 200 | 1 | 11 | 1 | 1 / 1 | 0 |

The stalled status read reaches its existing four-second deadline. The bridge returns a correlated `native_deadline` rejection. Releasing the late SDK read still produces no prompt call, native row or stored operation receipt. The delayed mutation case keeps the one SDK prompt pending while the bridge's deadline elapses. The receipt is uncertain with no native row, remains uncertain with the exact user row alone, and becomes admitted only after the exact text part is visible. Read-side reconciliation makes no second POST. Admission proves the user's input only.

These probes separate the digest and native preflight from SDK submission and exact-row reconciliation. The SDK and native storage are fixtures. They leave actual Bun HTTP delivery, SQLite write timing, model transport, and the cause of the simultaneous Linux soak failures unproven. The probes do not touch signed queue roots.
