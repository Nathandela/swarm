package skeleton

import (
	"net"
	"os"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remotegw"
	"github.com/Nathandela/swarm/internal/wire"
)

func TestOwnerLaunchReceiptUnavailableThroughSignedRemoteStatus(t *testing.T) {
	sk, remoteSocket := assembleWithRemote(t)
	ks := registerPhone(t, sk, device.CapFull)
	id, err := protocol.NewLaunchOperationID()
	if err != nil {
		t.Fatal(err)
	}
	c := dialClient(t, sk, protocol.CapOwnerLaunch)
	req := protocol.LaunchReq{Agent: "fake", Cwd: t.TempDir(), Cols: 80, Rows: 24, Options: map[string]string{"script": mustScript(t, "idle 60s\n")}}
	if _, _, err := c.LaunchOperation(id, req); err != nil {
		t.Fatal(err)
	}
	for _, m := range sk.Core().List() {
		local := m.ID
		t.Cleanup(func() { _ = sk.Core().Kill(local) })
	}
	auth, err := phonecore.SignCommand(ks, phonecore.CommandInput{Action: protocol.ActionOperationStatus, Machine: sk.api.endpointID, Session: protocol.OperationSessionSentinel, OperationID: "remote-read-owner", ExpiresAt: time.Now().Add(time.Minute), ContentHash: schema.OperationStatusContentHash(id)})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := remotegw.New(remoteSocket, nil).ForwardCommand(protocol.OpOperationStatus, protocol.RemoteCommand{DeviceCommandAuth: auth, BodyVersion: 1, SubjectOperationID: id})
	if err != nil || reply.Op != protocol.OpOperationStatus || reply.OperationOutcome == nil || reply.OperationOutcome.State != schema.OutcomeUnknownOperation || reply.OperationOutcome.SessionID != "" {
		t.Fatalf("signed alternate status exposed owner receipt: %+v, %v", reply, err)
	}
	if out, known := sk.api.RemoteOperationOutcome(id); known {
		t.Fatalf("remote custody exposed owner receipt: %+v", out)
	}
	if out, err := c.LaunchOperationStatus(id); err != nil || out.State != schema.OutcomeApplied {
		t.Fatalf("owner lost its receipt: %+v, %v", out, err)
	}
}

func TestOwnerLaunchDisconnectedReceiptReconcilesOriginal(t *testing.T) {
	sk := assemble(t)
	id, err := protocol.NewLaunchOperationID()
	if err != nil {
		t.Fatal(err)
	}
	req := protocol.LaunchReq{Agent: "fake", Cwd: t.TempDir(), Cols: 80, Rows: 24, Options: map[string]string{"script": mustScript(t, "idle 60s\n")}}
	conn, err := net.Dial("unix", sk.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	write := func(c protocol.Control) {
		t.Helper()
		body, err := protocol.EncodeControl(c)
		if err != nil {
			t.Fatal(err)
		}
		if err := wire.WriteFrame(conn, wire.TControl, body); err != nil {
			t.Fatal(err)
		}
	}
	write(protocol.Control{Op: protocol.OpHello, ProtocolVersion: protocol.Version, Capabilities: []string{protocol.CapOwnerLaunch}})
	_, body, err := wire.ReadFrame(conn)
	if err != nil {
		t.Fatal(err)
	}
	hello, err := protocol.DecodeControl(body)
	if err != nil {
		t.Fatal(err)
	}
	write(protocol.Control{Op: protocol.OpOwnerLaunch, EndpointID: hello.EndpointID, OperationID: id, Launch: &req})
	_ = conn.Close() // The daemon can accept the launch, but its receipt is lost.
	c := dialClient(t, sk, protocol.CapOwnerLaunch)
	var out schema.OperationOutcomeView
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		out, err = c.LaunchOperationStatus(id)
		if err != nil {
			t.Fatal(err)
		}
		if out.State == schema.OutcomeApplied {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if out.State != schema.OutcomeApplied {
		t.Fatalf("lost launch never reconciled: %+v", out)
	}
	for _, m := range sk.Core().List() {
		local := m.ID
		t.Cleanup(func() { _ = sk.Core().Kill(local) })
	}
	again, _, err := c.LaunchOperation(id, req)
	if err != nil || again != out.SessionID || len(sk.Core().List()) != 1 {
		t.Fatalf("exact replay = %s, %v, outcome %+v", again, err, out)
	}
	req.Env = []string{"CHANGED=1"}
	if _, _, err := c.LaunchOperation(id, req); err == nil {
		t.Fatal("changed owner environment inherited original receipt")
	}
}

func TestOwnerResumeExistingConversationHasDurableReceipt(t *testing.T) {
	buildBinaries(t)
	source := endedCodexSource("source", nil)
	source.Cwd = t.TempDir()
	api := continuityAPIWithConfig(t, func(cfg *daemon.Config) { cfg.ShimBinary = swarmBin }, source)
	live, err := api.core.Launch(daemon.LaunchSpec{AgentType: "codex", Argv: []string{fakeAgentBin, mustScript(t, "idle 60s\n")}, Cwd: source.Cwd, ClientEnv: []string{"PATH=" + os.Getenv("PATH")}, ConversationID: source.ConversationID, ResumedFrom: source.ID, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = api.core.Kill(live.ID) })
	spec := daemon.LaunchSpec{AgentType: "codex", Cwd: source.Cwd, Options: map[string]string{protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, source.ID)}, OperationID: "launch:resume", OwnerLaunchHash: "exact-resume"}
	got, err := api.Launch(spec)
	if err != nil || got.ID != live.ID {
		t.Fatalf("resume = %s, %v", got.ID, err)
	}
	out, known := api.OwnerLaunchOutcome(spec.OperationID)
	if !known || out.State != schema.OutcomeApplied || out.SessionID != protocol.NamespacedID(testEndpoint, live.ID) {
		t.Fatalf("resume receipt = %+v, %v", out, known)
	}
	if err := api.core.Delete(source.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := api.Launch(spec); err != nil || got.ID != live.ID {
		t.Fatalf("resume receipt lost after source deletion: %s, %v", got.ID, err)
	}
}
