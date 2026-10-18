# Independent final core review

Reviewed source: `c5d2923214df0db247c0f6714f7d158f5a6caf1a`.

No remaining code-review blockers in the reviewed native recovery, receipt, state, supervision, observation, history, gateway, detail, and transcript paths. The adjacent JSON records exact production-file equality and SHA256 values. The transcript composition preserves the qualified health check and late-child exact ancestry reads; the root message reader signature and observer recovery hook are unchanged.

Three concrete findings were reproduced and fixed during this review:

- Initial native default variant was absent in the user row but spelled `default` in launch custody. Shared comparison equivalence and recovery now accept those native representations while refusing different explicit variants.
- A full original user already present in startup history bypassed retained outcome seeding. Present and absent owned inputs now preserve durable rank/time/source; complete current proof needs no extra SDK read. Exact matching native error names survive a retained-source tie.
- Stable TodoWrite plan IDs repeated within one native page. The anchor now chooses the newest matching row, preserving intervening history and forwarding only real SDK cursors.

Independent native ARM race runs passed: default-variant admission/supervision/reducer cases in 6.628s; initial default, present retained failure/tie/named failure, and explicit mismatch recovery in 6.334s. The plan-anchor fix passed the full focused native history race in 10.003s. Original RED reproductions and fixes remain in their committed test/evidence chains.

Gateway review confirmed that history/detail negotiate the existing read capability, with unchanged unsigned sealed-epoch and remote-off gates. The connected test covers 3 pages, opaque continuation, 40KiB detail, cursor 0, unchanged journal highwater, and no ownership/request authority. Oversized detail replies receive a named refusal before their encoded frame can be dropped.

Android fixture-only changes `417dd905`/`1d471d52` preserve real JDK certificate validation and production SAN/hostname checks. Composition-gate change `a1fa1a29` lists actual NativeRequestView component calls. None skips tests or changes production trust behavior.

Full frozen-head Linux/macOS/native/Android/lint release results are maintained separately by their gate owners. This record is a code-review verdict and focused regression evidence.
