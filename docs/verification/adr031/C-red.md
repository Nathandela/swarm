# ADR-031 Unit C: red run

Tests: `internal/tui/accounts_needs_login_test.go` (written before any implementation).

Command:

```
go test -count=1 ./internal/tui
```

Key failing lines (behaviour missing in `internal/tui/accounts.go`; it compiles, so the
failures are assertion failures, not typos):

```
--- FAIL: TestAccountsNeedsLoginSummaryNamesSignInAndNewestReading (0.00s)
    accounts_needs_login_test.go:30: summary = "5h 42% used · weekly 73% used · refresh failed · stale", want "sign in again · last reading 03 Oct"
--- FAIL: TestAccountsNeedsLoginListRowAndGuidanceShowSignInAgain (0.00s)
    accounts_needs_login_test.go:46: list row missing "sign in again · last reading 03 Oct": "▌ Personal 1 · needs login · 5h 42% used · weekly 73% used · refresh failed · stale · 0 assigned"
--- FAIL: TestAccountsNeedsLoginDetailExplainsExpiryBeforeBars (0.00s)
    accounts_needs_login_test.go:62: detail missing "Sign-in expired. Last reading 03 Oct 10:42; use \"Sign in again\".":
FAIL	github.com/Nathandela/swarm/internal/tui
```

`TestAccountsNeedsLoginWordingLeavesOtherStatesUnchanged` passes (regression guard for
enabled, paused and needs-login-without-quota). All pre-existing tests stay green.
