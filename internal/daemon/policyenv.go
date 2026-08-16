package daemon

// Wave R5 round-4 fix-pack (bead agents-tracker-hggx.6, review BLOCKER 1): the
// DAEMON-POLICY half of ADR-007 D8.
//
// D8 reads "no phone-supplied env (env comes from daemon policy)". The deny half
// shipped -- internal/protocol/remote_launch.go composes every preset launch with
// ClientEnv: nil -- while the policy half did not exist, so a remote launch handed the
// agent persist.FilterEnv(nil): an EMPTY environment. With no PATH the adapter's bare
// argv0 ("claude", "codex") could not resolve at all, and past that the agent process
// would have had neither PATH nor HOME. Every session_launch naming a production preset
// refused. This file is the missing half.

import (
	"os"

	"github.com/Nathandela/swarm/internal/persist"
)

// daemonEnviron reads the daemon's OWN process environment. A package variable only so
// this is the single place the agent environment can originate from; production has one
// implementation and no configuration surface.
var daemonEnviron = os.Environ

// PolicyEnv resolves a launch's agent environment, allowlist-filtered either way
// (persist.FilterEnv -- the S-2 normative list, unchanged by this fix):
//
//   - A launch that SUPPLIED a client env keeps exactly that env. A local launch
//     forwards the invoking shell's environment, which is ADR-006's billing-inheritance
//     rule: the agent bills against the credentials of the shell that started it.
//   - A launch with NO client env (nil -- the remote/preset path, which never accepts a
//     phone-supplied env) gets the DAEMON's own process environment. That is daemon
//     policy in the only form that needs no new configuration and gives a phone no
//     influence whatsoever: the daemon process IS the user's machine environment, so
//     ADR-006's inheritance rule holds unchanged one level up.
//
// A caller that means "an EMPTY environment" passes a non-nil empty slice; nil means
// "no env was supplied", which is the distinction the remote path relies on.
//
// The allowlist is deliberately NOT widened: PATH, HOME, SHELL, TERM, the locale family,
// the venv/conda vars and the two provider credentials are already exactly what a real
// agent CLI needs to run, and everything else -- every unrelated secret, every injection
// vector -- is still dropped from the daemon's environment just as it is from a client's.
func PolicyEnv(clientEnv []string) []string {
	if clientEnv != nil {
		return persist.FilterEnv(clientEnv)
	}
	return persist.FilterEnv(daemonEnviron())
}
