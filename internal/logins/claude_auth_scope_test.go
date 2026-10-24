package logins

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestClaudeAuthScopeRequiredBeforeClaim(t *testing.T) {
	s, _, _, ref := newClaudeAuthUnboundTest(t)
	if _, _, err := s.ClaimClaudeAuthAttempt(ref, currentClaudeAuthWorker(t)); !errors.Is(err, ErrInUse) {
		t.Fatalf("unbound native claim admitted: %v", err)
	}
}

func claudeAuthTestScope(t *testing.T, ref ClaudeAuthRef) ClaudeAuthScope {
	w := currentClaudeAuthWorker(t)
	return ClaudeAuthScope{HostMountNS: "mnt:[1]", HostUserNS: "user:[1]", Profile: "systemd249-user-v1", Unit: "swarm-claude-auth-" + ref.Nonce + ".service", BootID: "00000000-0000-0000-0000-000000000001", BusID: "00000000000000000000000000000001", ManagerOwner: ":1.1", Manager: w, MountID: 1, Device: 1, Inode: 1, Cgroup: "/user.slice/swarm-claude-auth-" + ref.Nonce + ".service", InvocationID: "00000000000000000000000000000001", Worker: w}
}
func TestClaudeAuthScopeUnpublishedNativeCanBeContained(t *testing.T) {
	s, _, _, ref := newClaudeAuthTest(t)
	attempt := claimClaudeAuthTest(t, s, ref)
	if err := s.RecordClaudeAuthScopeContained(ref, attempt.Number, claudeAuthTestScope(t, ref)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishClaudeAuthAttempt(ref, attempt.Number); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("missing profile unexpectedly accepted: %v", err)
	}
	if err := retireClaudeAuthTest(t, s, ref); err != nil {
		t.Fatal(err)
	}
}
func TestClaudeAuthScopePreparedRevocationFencesLateBind(t *testing.T) {
	s, _, _, ref := newClaudeAuthUnboundTest(t)
	if err := retireClaudeAuthTest(t, s, ref); err != nil {
		t.Fatal(err)
	}
	if err := s.BindClaudeAuthScope(ref, claudeAuthTestScope(t, ref)); err == nil {
		t.Fatal("revoked birth bound")
	}
}

func TestClaudeAuthScopeBindingIsImmutableAndSchemaFenced(t *testing.T) {
	s, _, _, ref := newClaudeAuthTest(t)
	if snapshot(t, s).SchemaVersion != claudeAuthScopeSchemaVersion {
		t.Fatal("scope ownership lacks reader fence")
	}
	changed := claudeAuthTestScope(t, ref)
	changed.Inode++
	if err := s.BindClaudeAuthScope(ref, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("replaced kernel group rebound: %v", err)
	}
	attempt := claimClaudeAuthTest(t, s, ref)
	if err := s.RecordClaudeAuthScopeContained(ref, attempt.Number, changed); err == nil {
		t.Fatal("foreign group containment accepted")
	}
	if err := s.RecordClaudeAuthScopeRetired(ref, attempt.Number, claudeAuthTestScope(t, ref)); !errors.Is(err, ErrInUse) {
		t.Fatalf("outside verifier retired live owner: %v", err)
	}
	if err := s.RetireClaudeAuth(ref); !errors.Is(err, ErrInUse) {
		t.Fatalf("running unpublished native treated as unadmitted: %v", err)
	}
}

func TestClaudeAuthScopePrepareFencesBeforeUnitBirthAndLateRecreation(t *testing.T) {
	s, _, _, ref := newClaudeAuthUnboundTest(t)
	path, err := s.PrepareClaudeAuthWorker(ref, "/bin/sh", "2.1.310", "qualified-pin")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot(t, s).SchemaVersion != claudeAuthScopeSchemaVersion {
		t.Fatal("worker config birth preceded schema fence")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("worker config not private")
	}
	if _, err := s.PrepareClaudeAuthWorker(ref, "/bin/sh", "2.1.310", "changed-pin"); !errors.Is(err, ErrConflict) {
		t.Fatalf("immutable config rebound: %v", err)
	}
	if err := retireClaudeAuthTest(t, s, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareClaudeAuthWorker(ref, "/bin/sh", "2.1.310", "qualified-pin"); err == nil {
		t.Fatal("late revoked birth created config")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("late birth recreated retired payload: %v", err)
	}
}
func TestClaudeAuthScopeFailureCannotSkipContainment(t *testing.T) {
	s, _, _, ref := newClaudeAuthTest(t)
	a := claimClaudeAuthTest(t, s, ref)
	if err := s.FailClaudeAuthAttempt(ref, a.Number); !errors.Is(err, ErrInUse) {
		t.Fatalf("failure released unpublished writer: %v", err)
	}
	if err := s.RecordClaudeAuthScopeContained(ref, a.Number, claudeAuthTestScope(t, ref)); err != nil {
		t.Fatal(err)
	}
	if err := s.FailClaudeAuthAttempt(ref, a.Number); err != nil {
		t.Fatal(err)
	}
	if err := retireClaudeAuthTest(t, s, ref); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeAuthScopeSchemaUpgradeCannotWrapRevision(t *testing.T) {
	for _, operation := range []string{"prepare", "bind"} {
		t.Run(operation, func(t *testing.T) {
			s, _, _, ref := newClaudeAuthUnboundTest(t)
			r := snapshot(t, s)
			r.Revision = ^uint64(0)
			if err := s.save(r); err != nil {
				t.Fatal(err)
			}
			var err error
			if operation == "prepare" {
				_, err = s.PrepareClaudeAuthWorker(ref, "/bin/sh", "2.1.310", "pin")
			} else {
				err = s.BindClaudeAuthScope(ref, claudeAuthTestScope(t, ref))
			}
			if !errors.Is(err, ErrPending) {
				t.Fatalf("exhausted registry revision admitted schema ownership: %v", err)
			}
			if snapshot(t, s).Revision != ^uint64(0) {
				t.Fatal("revision wrapped")
			}
		})
	}
}
func TestClaudeAuthScopeRejectsCorruptKernelTuple(t *testing.T) {
	_, _, _, ref := newClaudeAuthUnboundTest(t)
	for _, change := range []func(*ClaudeAuthScope){
		func(s *ClaudeAuthScope) { s.HostMountNS = "mnt:[junk" },
		func(s *ClaudeAuthScope) { s.HostUserNS = "user:[1]suffix" },
		func(s *ClaudeAuthScope) { s.ManagerOwner = ":garbage" },
		func(s *ClaudeAuthScope) { s.Cgroup = "/user.slice//" + s.Unit },
		func(s *ClaudeAuthScope) { s.Cgroup = "/user.slice/\x01/" + s.Unit },
	} {
		scope := claudeAuthTestScope(t, ref)
		change(&scope)
		if validClaudeAuthScope(ref, scope) {
			t.Fatal("corrupt durable tuple accepted")
		}
	}
}

func TestClaudeAuthScopeRetirementRetainsBindingUntilExactUnitReceipt(t *testing.T) {
	s, _, b, ref := newClaudeAuthUnboundTest(t)
	config, err := s.PrepareClaudeAuthWorker(ref, "/bin/sh", "2.1.310", "pin")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	start, err := procstart.StartTime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	scope := claudeAuthTestScope(t, ref)
	scope.Worker = processcontain.Identity{PID: cmd.Process.Pid, StartTime: start}
	if err := s.BindClaudeAuthScope(ref, scope); err != nil {
		t.Fatal(err)
	}
	if err := s.RetireClaudeAuth(ref); !errors.Is(err, ErrPending) {
		t.Fatalf("Remove/Retire discarded live preclaim unit binding: %v", err)
	}
	held, err := s.ReadClaudeAuth(ref)
	if err != nil || held.Phase != "retiring" || held.Scope != scope || held.UnitRetired {
		t.Fatalf("scope lost: %+v %v", held, err)
	}
	if _, err := os.Stat(config); err != nil {
		t.Fatal("proof payload removed before unit custody resolved")
	}
	if err := s.RecordClaudeAuthUnitRetired(ref, scope); err == nil {
		t.Fatal("live exact worker declared retired")
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if err := s.RecordClaudeAuthUnitRetired(ref, scope); err != nil {
		t.Fatal(err)
	}
	if err := s.RetireClaudeAuth(ref); err != nil {
		t.Fatal(err)
	}
	next, created, err := s.BeginClaudeAuth(snapshot(t, s).Revision, b.ID)
	if err != nil || !created || next.Nonce == ref.Nonce {
		t.Fatalf("settled unit prevented new operation: %v %v", created, err)
	}
	nextConfig, err := s.PrepareClaudeAuthWorker(next, "/bin/sh", "2.1.310", "pin")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RetireClaudeAuth(ref); !errors.Is(err, ErrConflict) {
		t.Fatalf("old nonce retirement affected new operation: %v", err)
	}
	if _, err := os.Stat(nextConfig); err != nil {
		t.Fatal("old cleanup removed current nonce")
	}
}

func TestClaudeAuthScopeCannotHideBelowSchemaFloor(t *testing.T) {
	s, _, _, ref := newClaudeAuthTest(t)
	r := snapshot(t, s)
	r.SchemaVersion = claudeAuthSchemaVersion
	if err := s.save(r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadClaudeAuth(ref); err == nil {
		t.Fatal("scope ownership accepted under schema8")
	}
}

func TestClaudeAuthScopeLateScratchOverflowCannotPublishFreshProfile(t *testing.T) {
	s, _, _, ref := newClaudeAuthTest(t)
	a := claimClaudeAuthTest(t, s, ref)
	stop := nativeClaudeAuthTest(t, s, ref, a)
	writeClaudeAuthProfileTest(t, a)
	if err := s.ObserveClaudeAuthProfile(ref, a.Number); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(a.AuthorityHome, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 17; n++ {
		file, err := os.OpenFile(filepath.Join(a.ConfigHome, fmt.Sprintf("late-%d", n)), os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		err = file.Truncate(2 << 20)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	stop()
	if err := s.RecordClaudeAuthScopeContained(ref, a.Number, claudeAuthTestScope(t, ref)); err != nil {
		t.Fatal(err)
	}
	result, err := s.FinishClaudeAuthAttempt(ref, a.Number)
	if !errors.Is(err, ErrCapacity) || result.Phase != "failed" {
		t.Fatalf("late quota overflow published authentication: %+v %v", result, err)
	}
	after, err := os.ReadFile(filepath.Join(a.AuthorityHome, ".credentials.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("quota failure changed canonical grant")
	}
}
