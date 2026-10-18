# Connected native phone packet qualification

This fixture connects the real phone signer/sealer, gateway envelope opener and `ForwardCommand`, remote-tier protocol authentication, assembled daemon/idempotency store, shim dispatch fence and private native SDK bridge fixture. The composer return path uses the real observer SSE/full-message read, native journal projector, `Gateway.RunJournal`, encrypted `RelaySink`, and durable phonecore `AcceptCommit`/publication settlement. Device enrollment and SDK responses are synthetic test fixtures; this is not a physical handset or hosted-relay end-to-end run, and no actual provider request occurs here.

The production NativeComposer capability is required, without a test override. Before observer proof commit `952a71ec`, both composer cases stopped at NativeComposer=false. With that commit and the exact-row producer proof, the connected path passed.

## Assertions

- The actual sealed phone NativeDecision body reaches the gateway and daemon unchanged. Modified ciphertext is refused by the envelope opener. A modified signed body, altered signature and read-only device are refused before any native dispatch. Read-approve devices can answer the scoped native permission and grouped custom/multiple question requests.
- Repeated delivery with the same operation produces one native effect. Lost native replies remain uncertain. A replacement operation cannot bypass that request’s unresolved custody; a later exact receipt recovers admission without another SDK call.
- SourcePhone attribution survives signed composer forwarding and durable native intent. A delivery response alone leaves the phone bubble pending. Only the exact native user row, projected and sealed through the journal return path, removes that durable publication.
- Plain inputs and 4,096-byte `<` inputs pass with both normal and lost native replies. The latter exceed the displayed journal item budget after escaping: the journal is clipped, while the exact input SHA256 remains and settles only the matching operation. Duplicate delivery still produces one native dispatch in all four cases.
- A legitimate native admission delayed 11 seconds reproduced the gateway’s former 10-second composer timeout. The fixed gateway gives composer and native decisions 35 seconds and uses one absolute deadline across payload write and reply read. Only native decisions negotiate the native-request capability; other operations retain their 10-second budget. The delayed admission and its duplicate now return successfully with one native effect.

## Reproduction and evidence

From the combined tree, with all inherited `SWARM_*` variables removed and native Go 1.26.9:

```sh
go test -race ./internal/skeleton ./internal/remotegw \
  -run 'TestOpenCodePhonePacket|TestNativeDecision|TestR8|TestCommandBridge' -count=1 -v
```

Result: skeleton passed in 31.902 s; gateway passed in 2.468 s. The slow native admission case passed in 11.44 s. The exact output is retained at `/private/tmp/swarm-opencode-implementation-20261018/review-phone/p7-native-phone-packet-green.txt`; the timeout failure is `p7-native-phone-slow-red.txt`. No test was skipped in this run.

Selective test/fix sequence: `65f9fd5f` (decision packets), `272c5349` (composer packet/echo RED), state dependencies `0dc20cd0`, `645473d3`, `952a71ec`, then `a9ca3197` (reply-loss matrix and slow-admission RED) and `b62bb6c1` (gateway deadline fix). The fixture adds only test seams to the existing native send harness, not a new production transport.
