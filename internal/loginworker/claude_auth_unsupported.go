//go:build !linux

package loginworker

import (
	"context"
	"github.com/Nathandela/swarm/internal/logins"
	"os"
	"time"
)

// ValidateClaudeAuthManager never creates units or writes authentication.
func ValidateClaudeAuthManager(context.Context) error { return ErrUnsupported }

func startClaudeAuthScope(context.Context, string, string, logins.ClaudeAuthRef, time.Time) (logins.ClaudeAuthScope, error) {
	return logins.ClaudeAuthScope{}, ErrUnsupported
}
func checkClaudeAuthConfinement(logins.ClaudeAuthScope) error  { return ErrUnsupported }
func containClaudeAuthScope(logins.ClaudeAuthScope, int) error { return ErrUnsupported }
func verifyClaudeAuthScopeRetired(context.Context, logins.ClaudeAuthScope) error {
	return ErrUnsupported
}
func collectClaudeAuthScope(context.Context, logins.ClaudeAuthScope) error { return ErrUnsupported }

func RunClaudeAuthConfinementCheck(string) error { return ErrUnsupported }

func openClaudeAuthImage(ClaudeAuthConfig) (*os.File, error) { return nil, ErrUnsupported }

func stopClaudeAuthRevokedWorker(context.Context, logins.ClaudeAuthScope) error {
	return ErrUnsupported
}

func reapClaudeAuthScope() error { return ErrUnsupported }
