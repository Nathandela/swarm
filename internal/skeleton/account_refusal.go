package skeleton

import (
	"cmp"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/accountcheck"
	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
)

// accountRefusal is one account a managed launch could not use: its nonsecret
// reason code and, for the codes the owner can act on, a one-line remedy.
type accountRefusal struct {
	id, label, code, remedy string
}

// noAccountError is the single refusal of a managed launch no account could serve.
// It still matches errAccountLaunch for callers that only classify.
type noAccountError struct {
	provider string
	refusals []accountRefusal
}

func (e *noAccountError) Error() string {
	parts := make([]string, 0, len(e.refusals))
	for _, r := range e.refusals {
		part := fmt.Sprintf("%q %s", r.label, r.code)
		if r.remedy != "" {
			part += " - " + r.remedy
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		parts = append(parts, "none is registered")
	}
	return fmt.Sprintf("accounts: no %s account can take this discussion: %s; or turn %s rotation off", e.provider, strings.Join(parts, "; "), e.provider)
}

func (e *noAccountError) Unwrap() error { return errAccountLaunch }

// accountRefusalCode is the reason code of a failure that belongs to the bound
// account alone, or "" for every other failure, which another account would share
// (cwd, project, argv, environment, CLI) and so is never retried.
func accountRefusalCode(err error) string {
	for _, code := range profileScopedConflicts {
		if errors.Is(err, accountconfig.Conflict(code)) {
			return code
		}
	}
	switch {
	case errors.Is(err, accounts.ErrInvalidCredentials):
		return "invalid-credentials"
	case errors.Is(err, accountcheck.ErrUnavailable):
		return "configuration-busy"
	case errors.Is(err, accountcheck.ErrCustodyUnknown):
		return "writer-custody-unknown"
	}
	return ""
}

// profileScopedConflicts are raised by the bound account's own profile, which no
// other account's profile shares.
var profileScopedConflicts = []string{"candidate-configuration-marker-changed", "unsafe-account-profile", "unsafe-account-configuration", "native-configuration-update-pending"}

func refusedAccount(tried []accountRefusal, id string) bool {
	for _, r := range tried {
		if r.id == id {
			return true
		}
	}
	return false
}

// accountRefusal describes err for the bound account, with a remedy when the owner
// can act on it.
func (a *coreAPI) accountRefusal(b accounts.Binding, err error) accountRefusal {
	r := accountRefusal{id: b.AccountID, label: b.AccountID, code: accountRefusalCode(err)}
	if registry, snapErr := a.accounts.store.Snapshot(); snapErr == nil && registry.Accounts[b.AccountID].Label != "" {
		r.label = registry.Accounts[b.AccountID].Label
	}
	switch r.code {
	case "invalid-credentials":
		r.remedy = "sign in again"
	case "configuration-busy":
		r.remedy = "retry in a moment"
	}
	return r
}

// noAccountLeft refuses a launch after selection found no account: the accounts
// already tried with their codes, then why each remaining one was not eligible.
func noAccountLeft(provider string, remaining accounts.Registry, tried []accountRefusal, now time.Time) error {
	refusals := append([]accountRefusal(nil), tried...)
	ids := make([]string, 0, len(remaining.Accounts))
	for id, account := range remaining.Accounts {
		if account.Provider == provider {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		account := remaining.Accounts[id]
		r := accountRefusal{id: id, label: cmp.Or(account.Label, id), code: "not-eligible"}
		switch {
		case account.Lifecycle != accounts.LifecycleEnabled:
			r.code, r.remedy = account.Lifecycle, "enable it in Accounts"
		case account.Auth != accounts.AuthValid:
			r.code, r.remedy = "signed-out", "sign in again"
		default:
			for _, scope := range account.Quota.Scopes {
				if scope.Denied || scope.Exhausted(now) {
					r.code, r.remedy = "quota-exhausted", "wait for its reset"
				}
			}
		}
		refusals = append(refusals, r)
	}
	return &noAccountError{provider: provider, refusals: refusals}
}
