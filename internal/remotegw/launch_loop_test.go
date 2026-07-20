// FAILING-FIRST (TDD RED, GG-5) tests for launch over the command loop
// (agents-tracker-sev): the sealed command envelope carries the LaunchReq spec so
// CommandBridge can forward a remote launch (previously refused for lack of a body).
//
// THE CONTRACT these tests freeze (undefined symbols -> compile-fail RED):
//   - protocol.RemoteCommand{ DeviceCommandAuth (embedded); Launch *LaunchReq }
//   - func OpenRemoteCommand(key, raw) (protocol.RemoteCommand, error): opens the
//     sealed envelope to the wrapper, backward-compatible with a bare-auth envelope
//     (Launch nil).
//   - CommandBridge forwards OpLaunch WITH the launch body when the action is launch.
package remotegw

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

func sealedRemote(t *testing.T, key crypto.ContentKey, seq uint64, rc protocol.RemoteCommand) []byte {
	t.Helper()
	plain, err := json.Marshal(rc)
	if err != nil {
		t.Fatalf("marshal remote command: %v", err)
	}
	env, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{Version: crypto.VersionV1, EpochID: 1, Seq: seq}, plain)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return env.Marshal()
}

func TestOpenRemoteCommand_BackwardCompatWithBareAuth(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 7)
	}
	// A bare-auth envelope (the pre-launch shape) still opens, with Launch nil.
	bare := sealedCmd(t, key, 1, protocol.DeviceCommandAuth{Action: protocol.ActionKill, Session: "m/s1", OperationID: "op-1", DeviceID: "d", Sig: "s"})
	rc, err := OpenRemoteCommand(key, bare)
	if err != nil {
		t.Fatalf("OpenRemoteCommand(bare): %v", err)
	}
	if rc.Action != protocol.ActionKill || rc.OperationID != "op-1" {
		t.Fatalf("bare auth fields lost: %+v", rc)
	}
	if rc.Launch != nil {
		t.Fatalf("bare auth envelope decoded a non-nil launch: %+v", rc.Launch)
	}
}

func TestCommandBridge_ForwardsLaunchWithSpec(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 7)
	}
	launch := &protocol.LaunchReq{Agent: "claude", Cwd: "/tmp", Cols: 80, Rows: 24, InitialPrompt: "hi"}
	auth := protocol.DeviceCommandAuth{
		Action:      protocol.ActionLaunch,
		Session:     protocol.LaunchSessionSentinel,
		OperationID: "op-launch-1",
		DeviceID:    "d1",
		Sig:         "sig",
		ContentHash: protocol.LaunchContentHash(launch),
	}
	mb := &fakeMailbox{inbox: []relay.Item{
		{Cursor: 1, Envelope: sealedRemote(t, key, 1, protocol.RemoteCommand{DeviceCommandAuth: auth, Launch: launch})},
	}}
	fwd := &fakeForwarder{}
	b := NewCommandBridge(CommandBridgeConfig{Mailbox: mb, Forwarder: fwd, Key: key, EpochID: 1, ReplyTarget: "phone"})

	n, err := b.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if n != 1 {
		t.Fatalf("processed %d, want 1 (launch must forward now that the spec rides in-envelope)", n)
	}
	if len(fwd.ops) != 1 || fwd.ops[0] != protocol.OpLaunch {
		t.Fatalf("forwarded ops = %v, want [launch]", fwd.ops)
	}
	if fwd.launches[0] == nil || fwd.launches[0].Agent != "claude" {
		t.Fatalf("launch spec not forwarded intact: %+v", fwd.launches[0])
	}
}
