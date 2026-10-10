# ADR-030: Managed Claude profiles renew their own expired access token

- Status: Proposed
- Date: 2026-10-10
- Amends: [ADR-028](ADR-028-personal-account-pools.md), Decision paragraph 4 (line 20, "Quota and authentication are observation inputs ..."). Swarm may now perform the native credential refresh itself, under the conditions below; a server-confirmed dead refresh token is an observation that may mark the account needs-login.
- Affects: `internal/accounts` (`claude_renew.go`, `native_inventory.go`), `internal/accountusage` (`fetch.go`, `claude_exchange.go`), `internal/skeleton` (`account_quota_fetch.go`), `internal/upgrade` (`account_stock_custody.go`), `internal/tui` (`accounts.go`)

## Context

ADR-028 assumed that native credential refresh happens inside the Claude CLI while it runs. A managed Claude account with no running discussion never refreshes. Its access token expires, `GET /api/oauth/usage` returns 401 on every poll, and the account list keeps showing a week-old quota reading while the account stays "enabled" and eligible for rotation. The refresh token on disk is usually still valid; nothing uses it.

## Decision

**D1. Renewal is reactive.** Swarm renews only when a quota fetch for a managed Claude account gets a 401 and the re-read credential is unchanged. There is no proactive renewal, no timer and no heartbeat. Renewal runs inside the existing quota fetch call, so it inherits its 10 s context, its cancellation and its retirement-custody reference; no new reference counting is added.

**D2. Managed profiles only.** Only private profiles owned by `internal/accounts` are renewed. Swarm never reads or writes `~/.claude` or any unmanaged login. Codex is out of scope.

**D3. The native protocol, pinned to Claude Code 2.1.296.** The refresh is a JSON `POST https://platform.claude.com/v1/oauth/token` with `grant_type=refresh_token`, the stored refresh token, the stored `clientId` (or the CLI's default client id) and the stored scopes. It uses the same hardened HTTP client as the usage fetch: no proxy, no redirects, 1 MiB response cap, duplicate-key-rejecting JSON. The endpoint and default client id are package constants marked with the pinned CLI version. No model request is ever made.

**D4. Native lock and compare-and-swap.** Swarm takes the same two `proper-lockfile` directory locks the CLI takes (`<profile>/.oauth_refresh.lock` and the legacy sibling `<profile>.lock`), treating a lock older than 60 s as stale. A fresh lock held by someone else means busy; swarm backs off and retries on the next poll. Inside the lock it re-reads the credential: if the access token already changed, a sibling renewed it and swarm adopts that result. After the exchange it writes only if the on-disk refresh token still equals the one it posted, merging `accessToken`, `refreshToken`, `expiresAt`, and when present `refreshTokenExpiresAt` and `scopes` into `claudeAiOauth`, preserving every other key. The write uses the existing durable replace helper at mode 0600.

**D5. Identity must match.** When the response names both the account and organization, their identity digest must equal the binding's identity, or nothing is written and the result is `ErrIdentityChanged`. The `.claude.json` identity is checked before and after the write. A refresh with the same verified identity remains, as ADR-028 says, not account rotation.

**D6. A dead refresh token is an observation.** If the server rejects the refresh token (`invalid_grant`) and the on-disk refresh token is unchanged, the fetch reports `login-expired` and the account is marked needs-login, only if the account's current generation still equals the fetch's generation. That reuses the existing needs-login path: quota polling stops, rotation excludes the account, the list says "sign in again" with the date of the last reading, and "Sign in again" restores it. Swarm never retries a rejected refresh token within a fetch; if recording needs-login itself fails, the ordinary error backoff applies and the server simply rejects the token again.

**D7. Leftovers are tolerated.** The erasure inventory removes an empty `.oauth_refresh.lock` directory left in a Claude profile, and the upgrade custody guard accepts a leftover `profiles/<id>.lock` directory.

## Consequences

**Positive:** Idle managed Claude accounts keep fresh quota readings. A genuinely expired login becomes visible as needs-login instead of a silent stale reading, and stops being chosen by rotation. Swarm and a running CLI cannot both spend the same refresh token, because both take the same locks and swarm writes only under CAS.

**Negative and risks:**
- Swarm now writes provider credentials, not just reads them. A bug here can lock an account out until the owner signs in again. The CAS, the identity check and the rule "never write on error or cancellation" bound that risk.
- Refresh tokens rotate. If swarm posts a refresh token and the process dies before the write, the old token may already be spent; the account then shows needs-login on the next attempt and needs one manual sign-in.
- **CLI protocol drift.** The endpoint, body shape, default client id, lock names and lock staleness are copied from Claude Code 2.1.296, not from a published contract. A later CLI may change any of them. A changed endpoint or body shows up as a non-`invalid_grant` failure and leaves the account in today's `auth-required`/`unavailable` state, never needs-login. A changed lock layout is the dangerous case, because swarm and the CLI could then refresh concurrently; the pinned version must be rechecked whenever the supported Claude Code version moves.

## Alternatives Considered

- **Launch the CLI briefly to let it refresh.** Rejected: it may start a model request and spawns a process the owner did not ask for.
- **Proactive renewal before expiry.** Rejected for now: it adds a timer and writes for accounts nobody is using. The reactive path fixes the observed failure.
- **Leave it to the owner.** Rejected: the stale-but-enabled state is invisible and keeps the account in rotation.
