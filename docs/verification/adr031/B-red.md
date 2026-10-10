# ADR-031 Unit B red run

Tests: `internal/upgrade/account_stock_custody_lock_test.go`

```
$ go test -count=1 ./internal/upgrade
--- FAIL: TestAccountStockCustodyGuardAcceptsLeftoverLegacyLockDirectory (0.02s)
    --- FAIL: TestAccountStockCustodyGuardAcceptsLeftoverLegacyLockDirectory/private (0.01s)
        account_stock_custody_lock_test.go:26: leftover legacy lock directory failed the custody guard account stock profile inventory contains an unknown entry
    --- FAIL: TestAccountStockCustodyGuardAcceptsLeftoverLegacyLockDirectory/umask-default (0.01s)
        account_stock_custody_lock_test.go:26: leftover legacy lock directory failed the custody guard account stock profile inventory contains an unknown entry
FAIL	github.com/Nathandela/swarm/internal/upgrade
```

Right reason: `accountStockCustodyGuard` still rejects every non-32-hex name in `profiles/`.
`TestAccountStockCustodyGuardRejectsOtherProfileEntries` passes before and must stay green
after the change (regular file or symlink named `<hex>.lock`, uppercase hex, 31-hex, `.locked`
suffix, bare `.lock`).
