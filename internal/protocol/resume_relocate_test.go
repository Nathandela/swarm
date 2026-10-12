package protocol

import (
	"strings"
	"testing"
)

func TestResumeRelocationRequiresNegotiatedOwnerCapability(t *testing.T) {
	for _, negotiated := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "negotiated"}[negotiated], func(t *testing.T) {
			stub := newStubDaemon()
			rc := rawDial(t, serveStub(t, stub))
			var caps []string
			if negotiated {
				caps = []string{CapResumeRelocate}
			}
			hello := rc.hello(Version, caps)
			req := policyLaunchReq(t)
			req.Options = map[string]string{OptionResumeFrom: NamespacedID(hello.EndpointID, "source"), OptionResumeRelocate: "true"}
			rc.writeControl(Control{Op: OpLaunch, EndpointID: hello.EndpointID, Launch: &req})
			got := rc.readControl()
			if !negotiated {
				if got.Op != OpError || got.ErrorCode != CodeCapabilityRefused || len(stub.launchSpecs()) != 0 {
					t.Fatalf("unnegotiated relocation reached the daemon: %+v", got)
				}
				return
			}
			specs := stub.launchSpecs()
			if got.Op == OpError || len(specs) != 1 || specs[0].Cwd != req.Cwd || specs[0].Options[OptionResumeRelocate] != "true" {
				t.Fatalf("negotiated relocation did not reach the daemon: %+v, %+v", got, specs)
			}
		})
	}
}

func TestResumeRelocationClientRefusesUnsupportedDaemon(t *testing.T) {
	stub := newStubDaemon()
	c, err := Dial(serveStub(t, stub), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	req := policyLaunchReq(t)
	req.Options = map[string]string{OptionResumeFrom: "endpoint/source", OptionResumeRelocate: "true"}
	_, _, err = c.Launch(req)
	if err == nil || !strings.Contains(err.Error(), "swarm daemon restart") || len(stub.launchSpecs()) != 0 {
		t.Fatalf("unsupported daemon was not refused before launch: %v", err)
	}
}

func TestResumeRelocationIsRefusedOnRemoteTier(t *testing.T) {
	stub := newStubDaemon()
	rc := rawDial(t, serveRemoteAPI(t, allowAllLaunchPolicy{stub}))
	hello := rc.hello(Version, []string{CapRemoteGateway, CapResumeRelocate})
	req := policyLaunchReq(t)
	req.Options = map[string]string{OptionResumeFrom: NamespacedID(hello.EndpointID, "source"), OptionResumeRelocate: "true"}
	rc.writeControl(remoteLaunchControl(hello.EndpointID, req))
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodePolicy || len(stub.launchSpecs()) != 0 {
		t.Fatalf("remote relocation was not refused: %+v", got)
	}
}

func TestResumeRelocationRejectsMalformedRequests(t *testing.T) {
	for _, options := range []map[string]string{
		{OptionResumeRelocate: "true"},
		{OptionResumeRelocate: "", OptionResumeFrom: "endpoint/source"},
		{OptionResumeRelocate: "false", OptionResumeFrom: "endpoint/source"},
	} {
		stub := newStubDaemon()
		rc := rawDial(t, serveStub(t, stub))
		hello := rc.hello(Version, []string{CapResumeRelocate})
		req := policyLaunchReq(t)
		req.Options = options
		rc.writeControl(Control{Op: OpLaunch, EndpointID: hello.EndpointID, Launch: &req})
		got := rc.readControl()
		if got.Op != OpError || got.ErrorCode != CodeInvalidField || len(stub.launchSpecs()) != 0 {
			t.Fatalf("malformed relocation reached the daemon: %+v", got)
		}
	}
}
