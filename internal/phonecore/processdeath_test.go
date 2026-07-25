package phonecore

// FAILING-FIRST (TDD RED, GG-5) acceptance test for PB-STATE-2, the single test the spec
// calls "the guard for §4.3 in both directions (liveness and replay)":
//
//	Kill the core process mid-session and restart. Typing, launch and kill must still
//	succeed, and a frame captured before the kill must still be rejected as a replay.
//
// The kill is a REAL SIGKILL of a REAL second process -- the standard Go re-exec pattern
// this repo already uses for crash injection (internal/persist/crash_test.go). Android
// kills backgrounded processes as routine behaviour, so anything less than a signal-killed
// process would be testing a graceful shutdown path that never runs on a handset. Nothing
// in the child's memory can survive to answer for the durable state, and no Close() is
// ever called -- durability that depends on a clean exit fails here.
//
// The MACHINE does not restart: one crypto.MailboxReceiver (the real gateway guard) spans
// the whole test, seeded by nothing, exactly as a gateway that was running before the
// phone died and is still running after. That is §4.3's scenario verbatim.
//
// RED, today, in both directions:
//   - liveness: the second process starts its bare atomic.Uint64 at 1, so the gateway
//     refuses every post-restart frame with crypto.ErrStaleSeq -- the exit criterion
//     fails on the second app launch;
//   - replay: the second process builds a fresh crypto.MailboxReceiver whose `seen ==
//     false` skips the staleness test entirely, so the frame the relay retained from
//     before the kill is applied again.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remotegw"
)

const (
	phoneHelperEnv  = "SWARM_PHONE_HELPER"
	phoneDirEnv     = "SWARM_PHONE_DIR"
	phoneSpoolEnv   = "SWARM_PHONE_SPOOL"
	phoneRunEnv     = "SWARM_PHONE_RUN"
	phoneEpoch      = uint32(7)
	phoneSession    = "m1/s1"
	phoneKeystrokes = "ls -la\r"
)

// TestHelperPhoneCoreSession is the victim process. It runs only when re-exec'd; during a
// normal test run it is a no-op skip.
//
// It does exactly what an Android app does on launch: Resume from the state directory,
// drain whatever the relay is holding, then take control and drive a session -- until the
// OS kills it, at an arbitrary point, with no warning.
func TestHelperPhoneCoreSession(t *testing.T) {
	if os.Getenv(phoneHelperEnv) != "1" {
		t.Skip("phone-core crash helper; runs only when re-exec'd")
	}
	dir, spool, run := os.Getenv(phoneDirEnv), os.Getenv(phoneSpoolEnv), os.Getenv(phoneRunEnv)

	core, err := Resume(Config{Dir: dir, Machine: "m1", Ack: noopAcker{}})
	if err != nil {
		os.Exit(10)
	}
	// OUTBOUND seals use the deterministic test key rather than restored state ON
	// PURPOSE: the key's own durability is asserted by
	// TestState_EveryResumeCriticalFieldSurvivesARestart, and holding it fixed here keeps
	// every rejection this test reports a SEQUENCE coordinate that did not survive the
	// process death, never a key that did not.
	key := testContentKey()
	// The INBOUND path necessarily uses the router, which is keyed from restored state.
	// If the epoch key did not survive Resume, say so in one word instead of letting an
	// AEAD failure masquerade as a replay verdict.
	restoredKey := core.State().Keys.ContentKey != (crypto.ContentKey{})

	// (1) Drain the relay: every inbound envelope that has no verdict yet.
	envs, _ := filepath.Glob(filepath.Join(spool, "in", "*.env"))
	sort.Strings(envs)
	for _, p := range envs {
		result := strings.TrimSuffix(p, ".env") + ".result"
		if _, err := os.Stat(result); err == nil {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			os.Exit(11)
		}
		var cursor uint64
		cur, err := os.ReadFile(strings.TrimSuffix(p, ".env") + ".cursor")
		if err != nil {
			os.Exit(12)
		}
		if _, err := fmt.Sscanf(string(cur), "%d", &cursor); err != nil {
			os.Exit(13)
		}
		verdict := "applied"
		if !restoredKey {
			verdict = "resume-restored-no-epoch-key"
		} else if _, err := core.Router().AcceptCommit(raw, cursor); err != nil {
			verdict = err.Error()
		}
		if err := writeAtomic(result, []byte(verdict)); err != nil {
			os.Exit(14)
		}
	}

	// (2) Re-lease first: PB-STATE-8 makes the take_control the frame that absorbs any
	// seq gap the reservation burned, because the gateway silently drops gapped input.
	emit := 0
	out := func(raw []byte) {
		emit++
		if err := writeAtomic(filepath.Join(spool, "out", fmt.Sprintf("%s-%08d.env", run, emit)), raw); err != nil {
			os.Exit(15)
		}
	}
	auth := takeControlAuth()

	seq, err := core.Seq().NextCommand()
	if err != nil {
		os.Exit(16)
	}
	raw, err := SealTakeControlEnvelope(key, phoneEpoch, seq, auth, "gate-token", 3600)
	if err != nil {
		os.Exit(17)
	}
	out(raw)

	// (3) launch, then kill -- the other two verbs the exit criterion names.
	if seq, err = core.Seq().NextCommand(); err != nil {
		os.Exit(18)
	}
	launchAuth := auth
	launchAuth.Action, launchAuth.OperationID = protocol.ActionLaunch, "op-launch-"+run
	if raw, err = SealLaunchEnvelope(key, phoneEpoch, seq, launchAuth, &protocol.LaunchReq{}); err != nil {
		os.Exit(19)
	}
	out(raw)

	if seq, err = core.Seq().NextCommand(); err != nil {
		os.Exit(20)
	}
	killAuth := auth
	killAuth.Action, killAuth.OperationID = protocol.ActionKill, "op-kill-"+run
	if raw, err = SealCommandEnvelope(key, phoneEpoch, seq, killAuth); err != nil {
		os.Exit(21)
	}
	out(raw)

	// (4) Type, until the OS kills us.
	for {
		if seq, err = core.Seq().NextInput(); err != nil {
			os.Exit(22)
		}
		if raw, err = SealInputData(key, phoneEpoch, seq, phoneSession, []byte(phoneKeystrokes)); err != nil {
			os.Exit(23)
		}
		out(raw)
		time.Sleep(5 * time.Millisecond)
	}
}

// noopAcker stands in for the relay ack in the helper: the harness models a relay that
// retains everything, so acking is a no-op and the durable high-water is the only guard.
type noopAcker struct{}

func (noopAcker) Ack(uint64) error { return nil }

// writeAtomic writes through a temp file in the same directory so the parent, which polls
// the spool, can never observe a half-written frame.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".spool-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// isSIGKILL reports whether err is the exit error of a SIGKILL'ed child. A clean exit or
// any other signal means the helper died of its own accord and the test proved nothing.
func isSIGKILL(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL
}

// waitForPhone polls cond until it holds or the deadline passes.
func waitForPhone(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// outFrames lists the envelopes the given run emitted, in emission (and therefore seq)
// order.
func outFrames(t *testing.T, spool, run string) [][]byte {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(spool, "out", run+"-*.env"))
	if err != nil {
		t.Fatalf("glob run %s: %v", run, err)
	}
	sort.Strings(paths)
	frames := make([][]byte, 0, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		frames = append(frames, b)
	}
	return frames
}

// runPhoneUntil starts the helper, waits for ready, SIGKILLs it, and asserts it really
// died of the signal.
func runPhoneUntil(t *testing.T, dir, spool, run string, ready func() bool) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperPhoneCoreSession$")
	cmd.Env = append(os.Environ(),
		phoneHelperEnv+"=1", phoneDirEnv+"="+dir, phoneSpoolEnv+"="+spool, phoneRunEnv+"="+run)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("run %s: start helper: %v", run, err)
	}
	ok := waitForPhone(ready, 20*time.Second)
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("run %s: SIGKILL: %v", run, err)
	}
	err := cmd.Wait()
	if !ok {
		t.Fatalf("run %s: the helper never reached its ready condition (it exited with %v); the process-death window never opened", run, err)
	}
	if !isSIGKILL(err) {
		t.Fatalf("run %s: helper exit = %v; want death by SIGKILL (a clean exit means no process death was tested)", run, err)
	}
}

// TestProcessDeath_TypingLaunchAndKillSurviveAKillWhileAReplayDoesNot is PB-STATE-2.
func TestProcessDeath_TypingLaunchAndKillSurviveAKillWhileAReplayDoesNot(t *testing.T) {
	if os.Getenv(phoneHelperEnv) == "1" {
		t.Skip("running as the crash helper")
	}
	dir, spool := t.TempDir(), t.TempDir()
	for _, sub := range []string{"in", "out"} {
		if err := os.MkdirAll(filepath.Join(spool, sub), 0o700); err != nil {
			t.Fatalf("mkdir spool/%s: %v", sub, err)
		}
	}
	key := testContentKey()

	// Pair the phone: the state the helper will Resume from on both launches.
	seed, err := Resume(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Resume (pairing): %v", err)
	}
	st := seed.State()
	st.Machine, st.RoutingID, st.EpochID = "m1", "rid-m1", phoneEpoch
	st.Keys = crypto.EpochKeys{ContentKey: key}
	if err := seed.Save(st); err != nil {
		t.Fatalf("Save paired state: %v", err)
	}

	// The machine side: ONE receiver for the whole test. The gateway did not restart.
	gw := gatewayReceiver()

	// The relay holds one machine -> phone frame. The SAME bytes are re-served after the
	// kill at a FRESH storage cursor, so a resumed read cursor alone cannot mask a
	// missing replay high-water (the discipline S2's replay tests established).
	retained := sealFrame(t, key, 5, marshalReply(t, takeControlReply()))
	placeInbound(t, spool, "0001", retained, 11)

	// ---- RUN 1: a live session, killed mid-typing -------------------------------
	runPhoneUntil(t, dir, spool, "1", func() bool {
		return inboundVerdict(spool, "0001") != "" && len(mustGlob(t, spool, "1")) >= 6
	})
	run1 := outFrames(t, spool, "1")
	for i, raw := range run1 {
		if _, err := remotegw.OpenMailboxFrame(gw, key, raw); err != nil {
			t.Fatalf("run 1 frame %d rejected by the gateway: %v (the FIRST launch must work today; this test is about the second)", i+1, err)
		}
	}

	// ---- RESTART ----------------------------------------------------------------
	placeInbound(t, spool, "0002", retained, 21) // the retaining relay re-serves it
	runPhoneUntil(t, dir, spool, "2", func() bool {
		return inboundVerdict(spool, "0002") != "" && len(mustGlob(t, spool, "2")) >= 6
	})

	// (A) LIVENESS. Typing, launch and kill still succeed at the gateway. Asserted
	// first because it is §4.3's headline: the exit criterion fails on the SECOND app
	// launch, and that must be visible in the RED evidence regardless of what the
	// inbound half does.
	run2 := outFrames(t, spool, "2")
	if len(run2) < 6 {
		t.Fatalf("run 2 emitted %d frames; want >= 6 (take_control, launch, kill and keystrokes)", len(run2))
	}
	var (
		sawTake, sawLaunch, sawKill bool
		typed                       int
		firstInputChecked           bool
	)
	for i, raw := range run2 {
		frame, err := remotegw.OpenMailboxFrame(gw, key, raw)
		if err != nil {
			t.Fatalf("post-restart frame %d refused by the gateway: %v -- this is §4.3: the phone restarted its send-seq under the same epoch and every keystroke, take_control, launch and kill is stale-dropped, permanently, until an epoch rotation or a re-pair",
				i+1, err)
		}
		switch frame.Kind {
		case remotegw.FrameCommand:
			switch frame.Command.Action {
			case protocol.ActionTakeControl:
				sawTake = true
			case protocol.ActionLaunch:
				sawLaunch = true
			case protocol.ActionKill:
				sawKill = true
			}
		case remotegw.FrameInput:
			// PB-STATE-8 on a real process death: the reservation burned a gap, and the
			// gateway DROPS gapped input silently (command_loop.go:331-334). The
			// take_control above had to absorb it.
			if !firstInputChecked {
				firstInputChecked = true
				if frame.Gap {
					t.Errorf("the first post-restart input frame carries the Gap bit; routeInput drops it and the user's first keystroke vanishes with no signal anywhere")
				}
			}
			if string(frame.Input.Data) != phoneKeystrokes {
				t.Errorf("post-restart keystroke payload = %q; want %q", frame.Input.Data, phoneKeystrokes)
			}
			typed++
		}
	}
	if !sawTake || !sawLaunch || !sawKill {
		t.Errorf("post-restart verbs: take_control=%v launch=%v kill=%v; the exit criterion needs all three", sawTake, sawLaunch, sawKill)
	}
	if typed == 0 {
		t.Errorf("no keystroke survived the restart; \"types\" is the verb §4.3 says breaks permanently after one process death")
	}

	// (B) REPLAY. The frame captured before the kill must still be rejected. Its
	// pre-kill application is asserted here too: if the phone could not apply it in the
	// first place there is nothing for the post-restart refusal to prove.
	if got := inboundVerdict(spool, "0001"); got != "applied" {
		t.Errorf("run 1 verdict on the machine frame = %q; want \"applied\" -- \"resume-restored-no-epoch-key\" means Resume brought back no epoch material at all, PB-STATE-1's first enumerated coordinate", got)
	}
	if verdict := inboundVerdict(spool, "0002"); !strings.Contains(verdict, crypto.ErrStaleSeq.Error()) {
		t.Errorf("after the process death the retained machine frame was %q; want it refused with %q -- MailboxReceiver.highest is in-memory, so a process death resets the replay high-water to zero and a retaining relay redelivers freely",
			verdict, crypto.ErrStaleSeq)
	}
}

// placeInbound writes an envelope plus the relay storage cursor it was served at.
func placeInbound(t *testing.T, spool, id string, raw []byte, cursor uint64) {
	t.Helper()
	base := filepath.Join(spool, "in", id)
	if err := os.WriteFile(base+".env", raw, 0o600); err != nil {
		t.Fatalf("place inbound %s: %v", id, err)
	}
	if err := os.WriteFile(base+".cursor", []byte(fmt.Sprintf("%d", cursor)), 0o600); err != nil {
		t.Fatalf("place inbound cursor %s: %v", id, err)
	}
}

// inboundVerdict reads the helper's verdict for an inbound envelope ("" until it acted).
func inboundVerdict(spool, id string) string {
	b, err := os.ReadFile(filepath.Join(spool, "in", id+".result"))
	if err != nil {
		return ""
	}
	return string(b)
}

func mustGlob(t *testing.T, spool, run string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(spool, "out", run+"-*.env"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return paths
}
