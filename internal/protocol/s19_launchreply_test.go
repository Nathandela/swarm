package protocol

// FAILING-FIRST (TDD RED, GG-5) test for slice S19's third production hole, which is
// PB-SYNC-2's own subject: a SUCCESSFUL launch reply is UNATTRIBUTABLE.
//
// handleLaunch's success path writes Control{Op, EndpointID, Session} and no OperationID,
// unlike replyOK and replyError, which both echo cc.opID. The consequence is not cosmetic:
// the gateway seals the daemon's reply verbatim onto the phone's reply bucket
// (remotegw.CommandBridge.forward -> sealReply), and phonecore.foldContent DROPS a
// command_reply carrying no operation id -- deliberately, because mis-keying it would
// attribute one op's verdict to another. So App.Outcome(id) never resolves for a launch that
// SUCCEEDED, and PendingOpCount stays >= 1 for the life of the process, hiding every op
// genuinely in flight behind a launch that actually ran.
//
// WHY NO SHIPPED TEST COULD HAVE CAUGHT IT. Every launch assertion in this package reads the
// reply's Session (namespacing, policy, resolved cwd, idempotency), and the phone-side suites
// resolve their outcomes against replies their own fixtures seal. The one place the two meet
// is a real phone driving a real daemon, which is what PB-E2E-1 is.
//
// THE REFUSAL PATH IS THE CONTROL. replyError has always echoed the id, so a test that only
// asserted "some reply names the op" would pass on today's code by refusing the launch. This
// one asserts the SUCCESS reply, and asserts the refusal too so a regression that broke both
// is not read as this defect being absent.

import (
	"testing"
	"time"
)

// TestS19_ASuccessfulRemoteLaunchReplyNamesTheOperationItAnswers.
func TestS19_ASuccessfulRemoteLaunchReplyNamesTheOperationItAnswers(t *testing.T) {
	stub := newStubDaemon()
	sock := serveRemoteAPI(t, allowAllLaunchPolicy{stub})
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	const opID = "devA:01JS19LAUNCHREPLY0000000"
	exp := time.Now().Add(time.Minute)
	rc.writeControl(Control{
		Op:          OpLaunch,
		EndpointID:  rep.EndpointID,
		Launch:      &LaunchReq{Agent: "claude", Cwd: t.TempDir(), Cols: 80, Rows: 24},
		OperationID: opID,
		DeviceID:    "devA", DeviceSig: "sig", ExpiresAt: &exp,
	})

	got := rc.readControl()
	if got.Op == OpError {
		t.Fatalf("the launch was refused (%q / %q); this test is about the SUCCESS reply, so a "+
			"refusal here makes it vacuous", got.Error, got.ErrorCode)
	}
	if got.Op != OpLaunch {
		t.Fatalf("launch answered with op %q, want %q", got.Op, OpLaunch)
	}
	if got.OperationID != opID {
		t.Fatalf("a SUCCESSFUL launch replied with operation_id %q, want %q. The phone claims a "+
			"verdict BY operation id (PB-SYNC-2) and phonecore.foldContent drops an untagged "+
			"command reply rather than mis-key it, so this launch ran on the machine and stays "+
			"in flight on the phone forever", got.OperationID, opID)
	}
}

// TestS19_ARefusedRemoteLaunchAlsoNamesTheOperation is the control: the refusal path has
// always been attributable, so it fences the success half against a repair that made both
// paths untagged and against one that only ever exercised the refusal.
func TestS19_ARefusedRemoteLaunchAlsoNamesTheOperation(t *testing.T) {
	stub := newStubDaemon()
	sock := serveRemoteAPI(t, allowAllLaunchPolicy{stub})
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	const opID = "devA:01JS19LAUNCHREFUSE000000"
	exp := time.Now().Add(time.Minute)
	// An absent cwd: refused by handleLaunch's own validation, after the operation id is
	// captured and before any daemon side effect.
	rc.writeControl(Control{
		Op:          OpLaunch,
		EndpointID:  rep.EndpointID,
		Launch:      &LaunchReq{Agent: "claude", Cwd: "/nonexistent/s19", Cols: 80, Rows: 24},
		OperationID: opID,
		DeviceID:    "devA", DeviceSig: "sig", ExpiresAt: &exp,
	})

	got := rc.readControl()
	if got.Op != OpError {
		t.Fatalf("a launch into a nonexistent cwd answered %q, want a refusal", got.Op)
	}
	if got.OperationID != opID {
		t.Fatalf("a REFUSED launch replied with operation_id %q, want %q. A phone with two ops in "+
			"flight can neither retry the right one nor mark the right one failed",
			got.OperationID, opID)
	}
}
