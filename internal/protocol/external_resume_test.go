package protocol

import "testing"

func TestExternalResumeRequiresNegotiatedOwnerCapability(t *testing.T) {
	const conversationID = "4a7a2465-d8f0-4c05-a7a9-c44d8077b22b"
	req := policyLaunchReq(t)
	req.Options = map[string]string{OptionResumeConversationID: conversationID}

	t.Run("older client capability set is refused", func(t *testing.T) {
		stub := newStubDaemon()
		rc := rawDial(t, serveStub(t, stub))
		hello := rc.hello(Version, nil)
		rc.writeControl(Control{Op: OpLaunch, EndpointID: hello.EndpointID, Launch: &req})
		got := rc.readControl()
		if got.Op != OpError || got.ErrorCode != CodeCapabilityRefused {
			t.Fatalf("reply = %#v; want capability_refused", got)
		}
		if len(stub.launchSpecs()) != 0 {
			t.Fatal("capability refusal reached DaemonAPI.Launch")
		}
	})

	t.Run("negotiated owner capability reaches daemon", func(t *testing.T) {
		stub := newStubDaemon()
		rc := rawDial(t, serveStub(t, stub))
		hello := rc.hello(Version, []string{CapExternalResume})
		rc.writeControl(Control{Op: OpLaunch, EndpointID: hello.EndpointID, Launch: &req})
		if got := rc.readControl(); got.Op == OpError {
			t.Fatalf("external resume refused: %#v", got)
		}
		specs := stub.launchSpecs()
		if len(specs) != 1 || specs[0].Options[OptionResumeConversationID] != conversationID {
			t.Fatalf("launch specs = %#v; want one external resume", specs)
		}
	})
}

func TestExternalResumeIsRefusedOnRemoteTier(t *testing.T) {
	stub := newStubDaemon()
	sock := serveRemoteAPI(t, allowAllLaunchPolicy{stub})
	rc := rawDial(t, sock)
	hello := rc.hello(Version, []string{CapRemoteGateway, CapExternalResume})
	req := policyLaunchReq(t)
	req.Options = map[string]string{
		OptionResumeConversationID: "4a7a2465-d8f0-4c05-a7a9-c44d8077b22b",
	}
	rc.writeControl(remoteLaunchControl(hello.EndpointID, req))
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodePolicy {
		t.Fatalf("reply = %#v; want remote policy refusal", got)
	}
	if len(stub.launchSpecs()) != 0 {
		t.Fatal("remote external resume reached DaemonAPI.Launch")
	}
}
