# ADR-030 Unit A3: red run

Tests: `internal/skeleton/account_quota_login_expired_test.go` (written before any implementation).

Command:

```
go test -race -count=1 ./internal/skeleton
```

Key failing lines (behaviour missing in `internal/skeleton/account_quota_fetch.go`; the package
compiles, so these are assertion failures: a `login-expired` fetch error is treated as a generic
error and the account stays "enabled"):

```
--- FAIL: TestQuotaLoginExpiredMarksNeedsLoginKeepsUsageAndStopsFetching (3.05s)
    account_quota_login_expired_test.go:59: account state did not reach needs-login (last "enabled")
--- FAIL: TestQuotaNeedsLoginAccountFetchesAgainAfterReauthenticate (3.05s)
    account_quota_login_expired_test.go:138: account state did not reach needs-login (last "enabled")
FAIL
FAIL	github.com/Nathandela/swarm/internal/skeleton	477.423s
```

`TestQuotaLoginExpiredDoesNotOverwriteReauthenticationDuringFetch` passes today: it is the
regression guard for the generation fence (a reauthentication during the in-flight fetch must
not be overwritten to needs-login). All pre-existing skeleton tests stay green.
