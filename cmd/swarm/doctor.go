package main

// `swarm doctor` -- the machine's lifecycle incidents, as a command (lifecycle plan
// R1). Every check answers with a finding that names its own fix, because the
// checks ARE the incident log: the 2026-08-28 attach failure (a backend planned
// against the wrong PATH), the bare-environment daemon that caused it, and the
// version skews the runbooks warn about.
//
// THE NON-SPAWNING INVARIANT: doctor never starts a daemon. Every swarm client
// verb auto-starts one when none is running (D-1), which is exactly how a cron'd
// `swarm ls` once rewrote a good daemon.env from a bare environment -- a
// diagnostic tool that could CAUSE the incident it diagnoses would be worse than
// none. Doctor therefore composes only from the non-spawning seams converge
// already trusts from a timer: daemon.LockFree, converge.HelloVia (a bounded
// protocol.Dial), daemon.LoadSavedEnv, and a persist read of the session store.
// doctor_test.go holds the negative control.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/detect"
	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/converge"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/version"
)

// doctorFinding is one check's answer. Status is "ok", "warn" or "fail"; Fix is
// the command that clears a non-ok finding, always present when Status is not ok.
type doctorFinding struct {
	Check  string `json:"check"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// credentialKeys are the provider credentials the S-2 allowlist carries
// (persist.FilterEnv): a saved environment that lost them launches sessions that
// resolve their binaries fine and then cannot bill -- a degradation binary
// resolution alone cannot see, so doctor checks for it by name.
var credentialKeys = []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"}

// runDoctor is `swarm doctor [--json]`. Exit 0 when nothing failed, 1 when any
// finding is "fail"; "warn" findings do not fail the run (a machine with no
// daemon running is healthy, merely idle).
func runDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the findings as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cc, err := clientConfig()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "doctor: %v\n", err)
		return 1
	}

	var findings []doctorFinding
	findings = append(findings, doctorDaemonCheck(cc)...)
	saved, savedFindings := doctorSavedEnvLoad(cc)
	findings = append(findings, savedFindings...)
	if saved != nil {
		findings = append(findings, doctorAgentChecks(saved)...)
		findings = append(findings, doctorCredentialChecks(saved)...)
	}
	findings = append(findings, doctorSessionChecks(cc.StateDir)...)
	findings = append(findings, doctorUpgradeCheck(cc.StateDir))

	if *asJSON {
		if err := writeJSON(stdout, findings); err != nil {
			_, _ = fmt.Fprintf(stderr, "doctor: %v\n", err)
			return 1
		}
	} else {
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "STATUS\tCHECK\tDETAIL\tFIX")
		for _, f := range findings {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", strings.ToUpper(f.Status), f.Check, f.Detail, f.Fix)
		}
		_ = tw.Flush()
	}

	for _, f := range findings {
		if f.Status == "fail" {
			return 1
		}
	}
	return 0
}

// doctorDaemonCheck: is a daemon running, and does its build match this binary?
// The first probe is the SOCKET's existence, not daemon.LockFree: LockFree
// CREATES the lock file it tests (flock needs one), and doctor's contract is to
// leave an untouched state dir exactly as found -- the negative control in
// doctor_test.go caught the LockFree-first version doing otherwise. The lock is
// consulted only once a socket exists, where the state dir is in use anyway and
// the question is "crashed or wedged".
func doctorDaemonCheck(cc daemon.ClientConfig) []doctorFinding {
	if _, err := os.Stat(cc.SocketPath); errors.Is(err, os.ErrNotExist) {
		return []doctorFinding{{
			Check: "daemon", Status: "ok",
			Detail: "no daemon running; the next swarm command starts one from its own shell",
		}}
	}
	build, err := converge.HelloVia(cc.SocketPath)()
	if err != nil {
		if daemon.LockFree(cc) {
			return []doctorFinding{{
				Check: "daemon", Status: "ok",
				Detail: "a stale socket from an exited daemon; the next swarm command replaces it",
			}}
		}
		return []doctorFinding{{
			Check: "daemon", Status: "warn",
			Detail: fmt.Sprintf("a daemon holds the lock but did not answer the hello: %v", err),
			Fix:    "swarm daemon restart",
		}}
	}
	if build != version.Version {
		return []doctorFinding{{
			Check: "daemon", Status: "warn",
			Detail: fmt.Sprintf("the daemon runs %s but this binary is %s", build, version.Version),
			Fix:    "swarm daemon restart (adopts live sessions)",
		}}
	}
	return []doctorFinding{{
		Check: "daemon", Status: "ok",
		Detail: fmt.Sprintf("running %s, matching this binary", build),
	}}
}

// doctorSavedEnvLoad reads daemon.env; a nil first return means the agent and
// credential checks cannot run.
func doctorSavedEnvLoad(cc daemon.ClientConfig) ([]string, []doctorFinding) {
	saved, err := daemon.LoadSavedEnv(cc.StateDir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, []doctorFinding{{
			Check: "daemon.env", Status: "warn",
			Detail: "no saved daemon environment; unattended restarts refuse and remote launches fall back to the daemon's own",
			Fix:    "swarm daemon restart (from a terminal)",
		}}
	case err != nil:
		return nil, []doctorFinding{{
			Check: "daemon.env", Status: "fail",
			Detail: fmt.Sprintf("cannot read the saved daemon environment: %v", err),
			Fix:    "swarm daemon restart (from a terminal)",
		}}
	case len(saved) == 0:
		return nil, []doctorFinding{{
			Check: "daemon.env", Status: "fail",
			Detail: "the saved daemon environment is empty: nothing safe to spawn from, no PATH for remote launches",
			Fix:    "swarm daemon restart (from a terminal)",
		}}
	}
	return saved, []doctorFinding{{
		Check: "daemon.env", Status: "ok",
		Detail: fmt.Sprintf("saved, %d variables", len(saved)),
	}}
}

// doctorAgentChecks is the split-brain check, the 2026-08-28 incident as a
// permanent test: for every production agent CLI, what YOUR shell resolves must
// also resolve for the daemon's saved environment, or sessions launched outside
// a terminal (remote, preset, converge-spawned) run a different binary or none.
// The saved-env probe resolves names only (detect.EnvHost never executes).
func doctorAgentChecks(saved []string) []doctorFinding {
	host := detect.Host{}
	envHost := detect.EnvHost{Env: saved}
	var out []doctorFinding
	for _, name := range registry.Names() {
		if !registry.IsProduction(name) {
			continue
		}
		ad, ok := registry.New(name)
		if !ok {
			continue
		}
		client := adapter.Detect(ad, host)
		fromSaved := adapter.Detect(ad, envHost)
		check := "agent:" + name
		switch {
		case client.Found && !fromSaved.Found:
			out = append(out, doctorFinding{
				Check: check, Status: "fail",
				Detail: fmt.Sprintf("resolves for your shell (%s) but NOT for the daemon's saved environment -- non-terminal launches of %s break", client.Path, name),
				Fix:    "swarm daemon restart (from this terminal)",
			})
		case !client.Found && fromSaved.Found:
			out = append(out, doctorFinding{
				Check: check, Status: "warn",
				Detail: fmt.Sprintf("resolves for the daemon's saved environment (%s) but not for your shell", fromSaved.Path),
			})
		case !client.Found:
			out = append(out, doctorFinding{
				Check: check, Status: "ok",
				Detail: "not installed",
			})
		case client.Path != fromSaved.Path:
			out = append(out, doctorFinding{
				Check: check, Status: "warn",
				Detail: fmt.Sprintf("your shell resolves %s, the saved environment resolves %s -- two different binaries", client.Path, fromSaved.Path),
				Fix:    "swarm daemon restart (from this terminal)",
			})
		default:
			out = append(out, doctorFinding{
				Check: check, Status: "ok",
				Detail: client.Path,
			})
		}
	}
	return out
}

// doctorCredentialChecks: a provider key present in this shell but absent from
// the saved environment is the half of the bare-daemon incident binary
// resolution cannot see (ADR-006 billing inheritance).
func doctorCredentialChecks(saved []string) []doctorFinding {
	var out []doctorFinding
	for _, key := range credentialKeys {
		inShell := os.Getenv(key) != ""
		inSaved := false
		for _, kv := range saved {
			if strings.HasPrefix(kv, key+"=") && len(kv) > len(key)+1 {
				inSaved = true
				break
			}
		}
		if inShell && !inSaved {
			out = append(out, doctorFinding{
				Check: "credential:" + key, Status: "warn",
				Detail: "set in your shell but missing from the daemon's saved environment; non-terminal launches lose it",
				Fix:    "swarm daemon restart (from this terminal)",
			})
		}
	}
	return out
}

// doctorSessionChecks reads the session store from disk (never the daemon) and
// surfaces every session that launched with no backend although its adapter
// declared one -- the "working PTY, nothing to attach to" degradation.
func doctorSessionChecks(stateDir string) []doctorFinding {
	store, err := persist.NewStore(stateDir)
	if err != nil {
		return []doctorFinding{{
			Check: "sessions", Status: "warn",
			Detail: fmt.Sprintf("cannot open the session store: %v", err),
		}}
	}
	metas, err := store.Scan()
	if err != nil {
		return []doctorFinding{{
			Check: "sessions", Status: "warn",
			Detail: fmt.Sprintf("cannot scan the session store: %v", err),
		}}
	}
	var out []doctorFinding
	degraded := 0
	for _, m := range metas {
		if m.BackendPlanError == "" {
			continue
		}
		degraded++
		out = append(out, doctorFinding{
			Check: "session:" + m.ID, Status: "fail",
			Detail: fmt.Sprintf("launched with no backend: %s", m.BackendPlanError),
			Fix:    "fix the cause above, then relaunch the session",
		})
	}
	out = append(out, doctorFinding{
		Check: "sessions", Status: "ok",
		Detail: fmt.Sprintf("%d persisted, %d degraded", len(metas), degraded),
	})
	return out
}

// doctorUpgradeCheck reads the upgrade state the update transaction writes
// (lifecycle plan R2). Its absence is normal until R2 ships or runs.
func doctorUpgradeCheck(stateDir string) doctorFinding {
	if _, err := os.Stat(upgradeStatePath(stateDir)); errors.Is(err, os.ErrNotExist) {
		return doctorFinding{
			Check: "auto-update", Status: "ok",
			Detail: "has not run on this machine",
		}
	}
	return doctorFinding{
		Check: "auto-update", Status: "ok",
		Detail: "state present (see " + upgradeStatePath(stateDir) + ")",
	}
}

// upgradeStatePath is where the update transaction records each run's outcome.
// Defined here until R2 introduces the writer, so the reader and writer cannot
// drift apart on the location.
func upgradeStatePath(stateDir string) string {
	return stateDir + string(os.PathSeparator) + "upgrade.json"
}
