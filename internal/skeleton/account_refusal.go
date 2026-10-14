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
	"github.com/Nathandela/swarm/internal/status"
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
	switch {
	case errors.Is(err, accountconfig.Conflict("configuration-writer-active")):
		return "configuration-writer-active"
	case errors.Is(err, accountcheck.ErrUnavailable):
		return "configuration-busy"
	case errors.Is(err, accountcheck.ErrCustodyUnknown):
		return "writer-custody-unknown"
	}
	return ""
}

func refusedAccount(tried []accountRefusal, id string) bool {
	for _, r := range tried {
		if r.id == id {
			return true
		}
	}
	return false
}

// accountRefusal describes err for the bound account, naming the discussions that
// hold it when ending one of them is the remedy.
func (a *coreAPI) accountRefusal(b accounts.Binding, err error) accountRefusal {
	r := accountRefusal{id: b.AccountID, label: b.AccountID, code: accountRefusalCode(err)}
	if registry, snapErr := a.accounts.store.Snapshot(); snapErr == nil && registry.Accounts[b.AccountID].Label != "" {
		r.label = registry.Accounts[b.AccountID].Label
	}
	switch r.code {
	case "configuration-writer-active":
		var names []string
		for _, m := range a.core.List() {
			if m.AccountBinding != nil && m.AccountBinding.AccountID == b.AccountID && m.Status.Process == status.ProcessRunning {
				names = append(names, fmt.Sprintf("%q", cmp.Or(m.Name, m.ID)))
			}
		}
		r.remedy = "end discussion " + cmp.Or(strings.Join(names, ", "), "using this account")
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
