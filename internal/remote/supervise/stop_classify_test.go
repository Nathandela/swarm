package supervise

// FAILING-FIRST tests for slice S4b, second item: Stop still classifies by PROSE.
//
// notLoaded() (supervisor.go) sniffs launchctl's and systemctl's combined output for
// "no such" / "not find" / "not loaded" / "does not exist" and decides from that whether a
// failed teardown was really a success. That is the exact defect class S4's remediation
// purged from Ensure, and the rationale it was purged for indicts Stop identically: launchd
// has no stable, documented wording for these conditions, so a substring match decides on a
// coin flip. Ensure's own comment records one instance -- an already-bootstrapped label
// commonly reports "Bootstrap failed: 5: Input/output error", which names nothing.
//
// Stop's decision logic has ZERO coverage today; only its ErrNotInstalled path is tested.
// Both ways of getting it wrong are real:
//
//   - a message that does not match, on a job that was never loaded, becomes a spurious
//     "the device is revoked, but its gateway was not stopped" on every revoke;
//   - a message that DOES match, on a real bootout refusal, is swallowed -- and a gateway
//     that survives its revoke is served to the NEXT phone under the OLD epoch, because
//     Ensure is a documented no-op against a running job (stopGatewayIfQuiescent's own
//     comment says exactly this).
//
// WHAT THESE TESTS DO NOT DECIDE. They never name the command Stop consults after the
// teardown fails, and they never require a particular outcome for a particular message.
// They require only that the decision come from an init-system call's EXIT STATUS. The
// scripted runner treats the FIRST call as the teardown and every call after it as the
// decider, so any shape of second opinion satisfies them -- `launchctl print`,
// `systemctl --user is-active`, a repeated bootout, anything.
//
// Mirrors TestHostEnsure_ClassifiesNothingByMessage (supervisor_test.go), which scripts
// five outputs and requires one outcome from all of them.

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// teardownOutputs are the messages a failed teardown really produces. Three carry a
// notLoaded() substring and two do not, which is the whole point: the exit status is
// identical in all five, so nothing but the prose distinguishes them.
var teardownOutputs = []string{
	"Boot-out failed: 3: No such process",
	"Boot-out failed: 5: Input/output error",
	`Could not find service "com.swarm.remote" in domain for uid: 501`,
	"Failed to disable unit: Unit file swarm-remote.service does not exist.",
	"",
}

// stopScript stands in for the real exec of launchctl/systemctl during Stop. It scripts by
// POSITION, not by command name: the first call is the teardown (bootout / disable --now),
// and everything after it is whatever second opinion the implementation chooses to consult.
// Scripting the decider by exit status alone is what keeps these tests from prescribing it.
type stopScript struct {
	ran []string // subcommands, in order

	teardownErr error  // exit error of the FIRST call
	teardownOut string // its combined output

	deciderErr error  // exit error of EVERY call after the first
	deciderOut string // their combined output
}

func (s *stopScript) run(name string, args ...string) ([]byte, error) {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
		if sub == "--user" && len(args) > 1 { // systemctl's verb sits after --user
			sub = args[1]
		}
	}
	s.ran = append(s.ran, sub)
	if len(s.ran) == 1 {
		return []byte(s.teardownOut), s.teardownErr
	}
	return []byte(s.deciderOut), s.deciderErr
}

// installedSupervisorFor returns a supervisor for p whose unit file exists (so requireUnit
// passes) and whose init-system calls go to s instead of the real launchctl/systemctl. The
// platform is pinned regardless of the host GOOS: notLoaded() serves BOTH Stop branches, so
// both must be checked on every box.
func installedSupervisorFor(t *testing.T, p Platform, s *stopScript) *hostSupervisor {
	t.Helper()
	dir := t.TempDir()
	path, err := UnitPath(p, dir)
	if err != nil {
		t.Fatalf("UnitPath(%s): %v", p, err)
	}
	if err := os.MkdirAll(UnitDir(dir), 0o700); err != nil {
		t.Fatalf("create unit dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("unit"), 0o600); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	return &hostSupervisor{platform: p, unitPath: path, run: s.run}
}

// TestHostStop_ClassifiesNothingByMessage is the rule: with the exit status and the decider
// held FIXED, no teardown output -- however worded -- may change Stop's outcome. Anything
// else is a decision made on a string neither Apple nor systemd documents.
func TestHostStop_ClassifiesNothingByMessage(t *testing.T) {
	for _, p := range []Platform{PlatformLaunchd, PlatformSystemd} {
		t.Run(string(p), func(t *testing.T) {
			var want error
			var wantOut string
			for i, out := range teardownOutputs {
				s := &stopScript{
					teardownErr: errors.New("exit status 3"),
					teardownOut: out,
					deciderErr:  errors.New("exit status 113"),
				}
				err := installedSupervisorFor(t, p, s).Stop()
				if i == 0 {
					want, wantOut = err, out
					continue
				}
				if (err == nil) != (want == nil) {
					t.Fatalf("Stop() = %v for output %q but %v for output %q; the exit status is the "+
						"same in both. The outcome is being decided by the message.", err, out, want, wantOut)
				}
			}
		})
	}
}

// TestHostStop_ConsultsSomethingBeyondTheFailedTeardown: one ambiguous command cannot
// decide. bootout fails identically whether the job was never loaded or the boot-out was
// refused, so Stop must ask something else -- the same shape as Ensure, where kickstart and
// not bootstrap decides. This test does not care WHICH command; only that one was made.
func TestHostStop_ConsultsSomethingBeyondTheFailedTeardown(t *testing.T) {
	for _, p := range []Platform{PlatformLaunchd, PlatformSystemd} {
		t.Run(string(p), func(t *testing.T) {
			s := &stopScript{
				teardownErr: errors.New("exit status 5"),
				teardownOut: "Boot-out failed: 5: Input/output error",
			}
			_ = installedSupervisorFor(t, p, s).Stop()
			if len(s.ran) < 2 {
				t.Fatalf("Stop() decided after a single call (%v) whose exit status is ambiguous. "+
					"Nothing it ran can tell a job that was never loaded from one that refused to "+
					"boot out, so the outcome is being read off the message.", s.ran)
			}
		})
	}
}

// TestHostStop_OutcomeFollowsTheExitStatusNotTheProse is the mutation control for the two
// tests above, and the one that makes them impossible to satisfy trivially. Message-
// independence alone is satisfied by a Stop that always returns nil (and consults something
// it then ignores); this requires the decider's EXIT STATUS to actually change the answer.
//
// Direction-agnostic on purpose: it asserts that the two outcomes DIFFER, not which is
// which -- `launchctl print` failing means the job is gone, `systemctl is-active`
// succeeding means it is still up, and either polarity is a correct implementation.
func TestHostStop_OutcomeFollowsTheExitStatusNotTheProse(t *testing.T) {
	for _, p := range []Platform{PlatformLaunchd, PlatformSystemd} {
		t.Run(string(p), func(t *testing.T) {
			stop := func(deciderErr error) error {
				s := &stopScript{
					teardownErr: errors.New("exit status 5"),
					teardownOut: "Boot-out failed: 5: Input/output error",
					deciderErr:  deciderErr,
				}
				return installedSupervisorFor(t, p, s).Stop()
			}
			deciderFailed := stop(errors.New("exit status 113"))
			deciderSucceeded := stop(nil)
			if (deciderFailed == nil) == (deciderSucceeded == nil) {
				t.Fatalf("Stop() = %v when the decider failed and %v when it succeeded; the two must "+
					"differ, or nothing Stop runs distinguishes a job that is gone from one that is "+
					"still loaded", deciderFailed, deciderSucceeded)
			}
		})
	}
}

// TestHostStop_DeciderPolarity pins the DIRECTION of Stop's second opinion, which the three
// tests above deliberately leave open.
//
// They are direction-agnostic on purpose -- they must not prescribe WHICH command decides --
// but that flexibility has a measured cost: inverting BOTH polarities in the shipped
// implementation (`perr != nil` -> `perr == nil` on each arm) leaves every one of them GREEN.
// The direction would then rest on human review alone, which is this project's standing
// defect class: a guard that cannot fail.
//
// So this test constrains exactly ONE thing the others do not. The decider must be a PRESENCE
// query -- a nonzero exit means the job is GONE. It still never names the command. That is the
// convention both shipped deciders follow, measured on this host:
//
//	launchctl print gui/<uid>/<absent label>  -> exit 113  (nonzero: not in the domain)
//	launchctl print gui/<uid>                 -> exit 0
//	systemctl --user is-active <unit>         -> exit 0 only for active/reloading
//
// The foreign-domain exit 112 is unreachable here: launchdDomain() is always gui/$(getuid).
//
// A decider written the other way round -- an ABSENCE query, where a nonzero exit means still
// loaded -- fails this test. That is intended, and it is a far narrower constraint than naming
// the command: any presence-sense probe satisfies it.
func TestHostStop_DeciderPolarity(t *testing.T) {
	// A distinctive teardown output, so the "still present" leg can check that the failure is
	// PROPAGATED rather than merely that some error came back.
	const teardownOut = "Boot-out failed: 5: Input/output error"

	for _, p := range []Platform{PlatformLaunchd, PlatformSystemd} {
		t.Run(string(p), func(t *testing.T) {
			// The job is STILL THERE (decider exits 0). Stop must report the teardown failure:
			// this is the case where a gateway survived its revoke, and Ensure is a documented
			// no-op against a running job -- so the NEXT phone would be served by a process
			// still holding the revoked device's epoch. It is also the only thing that keeps
			// stopGatewayIfQuiescent's warning (cmd/swarm/remote.go) from being dead code.
			t.Run("still present", func(t *testing.T) {
				s := &stopScript{
					teardownErr: errors.New("exit status 5"),
					teardownOut: teardownOut,
					deciderErr:  nil, // exit 0: the job is in the domain / the unit is active
				}
				err := installedSupervisorFor(t, p, s).Stop()
				if err == nil {
					t.Fatalf("Stop() = nil though the teardown failed and the decider exited 0 -- "+
						"the job is STILL LOADED. A gateway that survives its revoke serves the next "+
						"phone under the old epoch, and this is the only place the operator hears "+
						"about it. calls=%v", s.ran)
				}
				if !strings.Contains(err.Error(), teardownOut) {
					t.Errorf("Stop() error = %q, drops the teardown's own output %q -- the init "+
						"system explains itself there and nowhere else", err, teardownOut)
				}
			})

			// The job is GONE (decider exits nonzero). Quiescent is exactly where Stop was asked
			// to leave it, so that is a success, not the spurious revoke-time warning the prose
			// classifier used to produce.
			t.Run("gone", func(t *testing.T) {
				s := &stopScript{
					teardownErr: errors.New("exit status 5"),
					teardownOut: teardownOut,
					deciderErr:  errors.New("exit status 113"), // nonzero: not in the domain / not active
				}
				if err := installedSupervisorFor(t, p, s).Stop(); err != nil {
					t.Fatalf("Stop() = %v though the decider exited nonzero -- there is nothing left "+
						"to stop, which is the state Stop exists to reach. calls=%v", err, s.ran)
				}
			})
		})
	}
}

// TestHostStop_SuccessfulTeardownIsSuccess pins the happy path both other tests leave open:
// a teardown that exits 0 removed the job, and Stop must say so without consulting anything
// that could talk it out of it.
func TestHostStop_SuccessfulTeardownIsSuccess(t *testing.T) {
	for _, p := range []Platform{PlatformLaunchd, PlatformSystemd} {
		t.Run(string(p), func(t *testing.T) {
			s := &stopScript{deciderErr: errors.New("exit status 113")}
			if err := installedSupervisorFor(t, p, s).Stop(); err != nil {
				t.Fatalf("Stop() = %v after a teardown that exited 0; calls=%v", err, s.ran)
			}
			if len(s.ran) == 0 {
				t.Fatalf("Stop() ran no init-system command at all")
			}
		})
	}
}
