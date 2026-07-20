# RED evidence — gateway namespaces journal ids at remote egress (agents-tracker-p1b)

Failing-first (GG-5). The daemon journals RAW local session ids, but SessionViews and
remote commands use the namespaced <endpoint>/<local> id, so a phone consuming the
journal could not correlate a roster/event entry to a command target.

`go test ./internal/skeleton/ -run TestGatewayServiceE2E` (after asserting the roster
id equals the namespaced command target):
```
--- FAIL: TestGatewayServiceE2E_JournalOutAndCommandIn
    gatewayservice_e2e_test.go:125: phone never received the live session over the relay (journal-OUT runtime broken)
```
The phone received the session under its RAW id, not the namespaced id it commands
against -> the assertion for the namespaced id failed. Fixed by namespacing journal
records at the gateway's remote egress (remotegw.namespaceRecord in RunJournal).
