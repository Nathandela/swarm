# ADR-030 Unit A1 red run

Tests written first: `internal/accounts/claude_renew_test.go` (new) and two additions to
`internal/accounts/native_inventory_test.go`. No implementation exists yet.

```
$ go test -count=1 ./internal/accounts
# github.com/Nathandela/swarm/internal/accounts [github.com/Nathandela/swarm/internal/accounts.test]
internal/accounts/claude_renew_test.go:99:22: undefined: ClaudeTokens
internal/accounts/claude_renew_test.go:100:9: undefined: ClaudeTokens
internal/accounts/claude_renew_test.go:125:24: f.s.RenewClaudeCredential undefined (type *Store has no field or method RenewClaudeCredential)
internal/accounts/claude_renew_test.go:125:134: undefined: ClaudeTokens
internal/accounts/claude_renew_test.go:129:32: undefined: RenewBusy
internal/accounts/claude_renew_test.go:172:24: f.s.RenewClaudeCredential undefined (type *Store has no field or method RenewClaudeCredential)
internal/accounts/claude_renew_test.go:172:134: undefined: ClaudeTokens
internal/accounts/claude_renew_test.go:178:32: undefined: RenewRefreshed
FAIL	github.com/Nathandela/swarm/internal/accounts [build failed]
```

The build fails only on the missing A1 symbols (`ClaudeTokens`, `RenewClaudeCredential`,
`Renew*` outcomes, `ErrRefreshRejected`).

Check for typos: a temporary stub of those symbols (deleted straight after, never committed)
made the package compile and vet cleanly. With it, every existing test passed. The new renew
tests failed on their assertions. The inventory behaviour change failed for the expected
reason:

```
native_inventory_test.go:306: leftover refresh lock refused erasure: account is not eligible
--- FAIL: TestClaudeErasureRemovesEmptyLeftoverRefreshLock
--- PASS: TestClaudeErasureRefusesNonEmptyRefreshLock
```
