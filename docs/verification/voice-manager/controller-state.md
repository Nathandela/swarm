# Controller state and replay foundations

The optional voice controller has a private bbolt ledger, immutable manager/native bindings, monotonic input admissions, exact action receipts, request lifetime fences, revocable speech permits and atomic journal/event/wake checkpoints. Its device stream uses separate replay coordinates and bounded encoded envelopes. This is the state foundation of P2; the running scheduler, native manager integration and client experience remain separate integration work.

Opening increments the controller generation. A separate fsynced initialization marker prevents a missing database from silently creating a new authority namespace. Physical schema 2 stores individual records and a checked root containing their count, aggregate keyed-record checksum and logical size. Unsupported or damaged state refuses access. These checks detect corruption; they are not authentication against an attacker with the owner's filesystem authority.

Mutations copy only affected tables, write their changed records and root in one transaction, then install the verified cache after successful commit. Exact retries perform no durable write. Returned values and snapshots cannot mutate cached byte slices. Failed commits, capacity refusal and oversized record keys leave both cache and durable state unchanged. Unknown or unresolved operations retain their guards; retired admission floors prevent compacted requests from acquiring fresh authority.

Independent Astra review reran the permanent store/controller/stream tests under the race detector (28.118 seconds) and added five isolated adversarial tests (4.124 seconds): failed public wake transitions followed by reopen, retries while another writer is held, concurrent mutation of detached results, repeated staged-key edits with exact byte accounting, and missing buckets/digests/roots against warm cache and reopen. No remaining defect was found. The earlier review also corrected canonical RawMessage persistence, closed-turn/task fences, startup capacity rollback and actual encoded stream limits. Full supervised package races passed after adding native-generation identity and MCP/config tests; build and vet passed.

Reviewed production SHA-256:

```text
store.go 6effa7fe49bc9d1b0f5b3424328861c4492387b427e0358aaf9517459243b1a8
state.go 938cf2e7a5740b0facb6d911f24a8cfc42aaf83fafecb4504c780248ce3892df
```

On the same Apple M1 and native Go 1.26.10 toolchain, synchronous bbolt benchmarks at 10,000 records produced these medians across three five-operation samples:

| Operation | Full-state baseline | Per-record implementation |
|---|---:|---:|
| Fresh action | 65.18 ms / 25.10 MB allocated | 16.40 ms / 2.43 MB |
| Fresh event and wake | 68.79 ms / 25.10 MB | 13.73 ms / 92.9 KB |
| Exact action retry | 63.18 ms / 23.42 MB | 17.1 µs / 2.24 KB |
| Exact event retry | 65.42 ms / 23.42 MB | 9.32 µs / 2.86 KB |
| Detached full snapshot | 32.72 ms / 5.71 MB | 1.52 ms / 2.96 MB |

The baseline was reconstructed by reversing the persistence optimization in scratch; it used identical fixtures and the same newly required native-generation field. The runs were sequential on a shared host, with fixture restoration outside the timer. These are microbenchmarks, not human speech or end-to-end latency results. Reproduce the current measurement with `go test ./internal/voice -run '^$' -bench 'BenchmarkControllerStore/records=(1000|10000)/' -benchmem -benchtime=5x -count=3` under the repository test environment.

Known limits: schema 1 was never released and is refused rather than migrated. Fresh action writes still copy their bounded table, and snapshots remain proportional to retained records. The 10,000-record/64 MiB logical bounds are not a physical disk quota; long-run growth and compaction qualification remain open. Native request reconciliation after service restart belongs to the runtime/controller caller, not merely to a fresh binding.
