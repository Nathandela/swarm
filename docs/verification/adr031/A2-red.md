# A2 red run: accountusage Claude exchange and fetch renewal

Tests written first: `internal/accountusage/claude_exchange_test.go`, `internal/accountusage/fetch_renew_test.go`.

## Full package (exchange symbol missing)

```
$ go test -count=1 ./internal/accountusage
internal/accountusage/claude_exchange_test.go:54:19: undefined: exchangeClaude
internal/accountusage/claude_exchange_test.go:70:17: undefined: exchangeClaude
internal/accountusage/claude_exchange_test.go:85:13: undefined: exchangeClaude
internal/accountusage/claude_exchange_test.go:119:14: undefined: exchangeClaude
internal/accountusage/claude_exchange_test.go:138:12: undefined: exchangeClaude
internal/accountusage/claude_exchange_test.go:149:12: undefined: exchangeClaude
internal/accountusage/claude_exchange_test.go:156:11: undefined: exchangeClaude
FAIL	github.com/Nathandela/swarm/internal/accountusage [build failed]
```

## Fetch renewal behaviour (exchange tests set aside temporarily)

```
$ go test -count=1 -race ./internal/accountusage
--- FAIL: TestClaudeUsage401RenewsOnceAndRetriesWithNewBearer
    renewal did not recover usage: usage=1 token=0 error=Usage authentication was refused; ...
--- FAIL: TestClaudeRenewalPostsStoredClientID
    renewal with stored client failed: token=0 ...
--- FAIL: TestClaudeRenewalAdoptedBySiblingRetriesWithSiblingBearer
    adopted renewal did not retry with sibling bearer: usage=1 token=0 ...
--- FAIL: TestClaudeRenewalDeadRefreshTokenIsLoginExpired
    dead refresh token at 400 was not login-expired: usage=1 token=0
--- FAIL: TestClaudeRenewalBusyLockIsUnavailable
    busy native lock was not unavailable: usage=1 token=0 error=Usage authentication was refused; ...
--- FAIL: TestClaudeRenewalHappensAtMostOncePerFetch
    renewal loop was not bounded: usage=1 token=0 ...
FAIL
```

Guard tests already green (must stay green): TestClaudeRenewalSkippedWhenNativeSnapshotAlreadyChanged,
TestCodexUsage401NeverRenews, and all pre-existing fetch_test.go tests.

Reason: fetch's 401 branch never calls `RenewClaudeCredential` (token=0) and `exchangeClaude` does not exist.
