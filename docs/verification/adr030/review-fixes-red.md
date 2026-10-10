# ADR-030 review fixes: red run

Command: `go test -count=1 ./internal/{accounts,accountusage,tui} -run 'ReadOnlyStoreNever|HeaderUnsafe|LegacySiblingLock|EndpointFailureKeeps|WithoutReadingTime'`

```
--- FAIL: TestRenewClaudeReadOnlyStoreNeverExchanges
    claude_renew_test.go:506: read-only store exchanged a refresh token
--- FAIL: TestRenewClaudeRejectsHeaderUnsafeTokens
        claude_renew_test.go:530: header-unsafe token written: <nil>
--- FAIL: TestClaudeErasureRemovesEmptyLegacySiblingLock
    native_inventory_test.go:350: legacy sibling refresh lock outlived erasure
--- FAIL: TestClaudeRenewalTokenEndpointFailureKeepsItsClass
    fetch_renew_test.go:240: token endpoint 429 was reported as Usage authentication was refused; ...: usage=1 token=1
--- FAIL: TestAccountsNeedsLoginWithoutReadingTimeKeepsUsageSummary
    accounts_needs_login_test.go:96: sign in again · last reading 01 Jan
```
