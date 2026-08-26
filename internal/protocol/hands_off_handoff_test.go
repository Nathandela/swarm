package protocol

// Phase 4 of the hands-off handoff sweep: the reserved launch-option key and its
// negotiated capability. Nothing new reaches the roster wire -- no SessionView
// field -- so this slice is entirely about the option key, the handshake, and the
// refusals that keep an older or lower-tier peer from mistaking the request for an
// ordinary launch.
//
// Seam for the implementer (protocol layer):
//   - OptionHandoffFrom = "handoff_from", carrying the NAMESPACED source session id.
//   - CapHandsOffHandoff = "hands-off-handoff", advertised in serverCaps.
//   - handleLaunch guards, in order: remote-tier refusal (CodePolicy, zero side
//     effects), capability refusal (CodeCapabilityRefused), then mutual exclusion
//     with the two resume keys (CodeInvalidField).

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/wire"
)

// handoffSource is a namespaced source session id -- the shape OptionHandoffFrom
// carries. The protocol layer forwards it verbatim; resolving it is the assembly's
// job (Phase 5), so nothing here depends on the session existing. Deliberately
// UNLIKE any id the stub daemon mints ("ep-N/sessN"), so the raw-frame leak check
// below cannot pass by colliding with a legitimately-echoed session id.
const handoffSource = "ep-9/source-session"

// handoffLaunchReq is policyLaunchReq plus exactly the options under test, so each
// case isolates the one guard it names.
func handoffLaunchReq(t *testing.T, options map[string]string) LaunchReq {
	t.Helper()
	req := policyLaunchReq(t)
	req.Options = options
	return req
}

// TestHandsOffHandoff_RefusedOnRemoteTier: handoff_from is owner-tier-only. The
// daemon composes the successor's prompt from the SOURCE session's transcript, so
// a phone-signed launch must never be able to ask for it. Refused with CodePolicy
// and ZERO daemon side effects -- nothing launched, no session created.
func TestHandsOffHandoff_RefusedOnRemoteTier(t *testing.T) {
	stub := newStubDaemon()
	sock := serveRemoteAPI(t, allowAllLaunchPolicy{stub})
	rc := rawDial(t, sock)
	hello := rc.hello(Version, []string{CapRemoteGateway, CapHandsOffHandoff})
	req := handoffLaunchReq(t, map[string]string{OptionHandoffFrom: handoffSource})
	rc.writeControl(remoteLaunchControl(hello.EndpointID, req))

	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodePolicy {
		t.Fatalf("reply = %#v; want remote policy refusal", got)
	}
	if specs := stub.launchSpecs(); len(specs) != 0 {
		t.Fatalf("remote hands-off handoff reached DaemonAPI.Launch: %#v", specs)
	}
	if metas := stub.List(); len(metas) != 0 {
		t.Fatalf("remote hands-off handoff created %d session(s); want zero side effects", len(metas))
	}
}

// TestHandsOffHandoff_RequiresNegotiatedCapability is the LOAD-BEARING guard.
// Without it an older daemon does not recognise the option key, silently ignores
// it, and performs a BARE LAUNCH -- a context-free agent loose in the user's
// checkout, which the design names as the worst available outcome. Negotiation
// turns that silent degrade into a refusal the client can see.
func TestHandsOffHandoff_RequiresNegotiatedCapability(t *testing.T) {
	req := handoffLaunchReq(t, map[string]string{OptionHandoffFrom: handoffSource})

	t.Run("older client capability set is refused", func(t *testing.T) {
		stub := newStubDaemon()
		rc := rawDial(t, serveStub(t, stub))
		hello := rc.hello(Version, nil)
		rc.writeControl(Control{Op: OpLaunch, EndpointID: hello.EndpointID, Launch: &req})
		got := rc.readControl()
		if got.Op != OpError || got.ErrorCode != CodeCapabilityRefused {
			t.Fatalf("reply = %#v; want capability_refused", got)
		}
		if specs := stub.launchSpecs(); len(specs) != 0 {
			t.Fatalf("capability refusal reached DaemonAPI.Launch: %#v", specs)
		}
	})

	t.Run("negotiated owner capability reaches the daemon", func(t *testing.T) {
		stub := newStubDaemon()
		rc := rawDial(t, serveStub(t, stub))
		hello := rc.hello(Version, []string{CapHandsOffHandoff})
		rc.writeControl(Control{Op: OpLaunch, EndpointID: hello.EndpointID, Launch: &req})
		if got := rc.readControl(); got.Op == OpError {
			t.Fatalf("hands-off handoff refused: %#v", got)
		}
		specs := stub.launchSpecs()
		if len(specs) != 1 || specs[0].Options[OptionHandoffFrom] != handoffSource {
			t.Fatalf("launch specs = %#v; want one carrying %s=%q", specs, OptionHandoffFrom, handoffSource)
		}
	})
}

// TestHandsOffHandoff_MutuallyExclusiveWithResumeOptions: handoff_from, resume_from
// and resume_conversation_id are three different answers to "where does this session
// come from". Combining them is a caller bug, not a merge, so each pairing is refused
// BY NAME rather than resolved by a silent precedence rule.
//
// The exclusion is checked BEFORE the external-resume capability gate, so a combined
// request is refused for what it actually is instead of for whichever capability the
// client happens not to have offered: both cases negotiate hands-off-handoff only,
// and must still be told about the combination.
func TestHandsOffHandoff_MutuallyExclusiveWithResumeOptions(t *testing.T) {
	cases := []struct {
		name  string
		other string
		value string
	}{
		{"resume_from", OptionResumeFrom, "ep-1/sess2"},
		{"resume_conversation_id", OptionResumeConversationID, "4a7a2465-d8f0-4c05-a7a9-c44d8077b22b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newStubDaemon()
			rc := rawDial(t, serveStub(t, stub))
			hello := rc.hello(Version, []string{CapHandsOffHandoff})
			req := handoffLaunchReq(t, map[string]string{
				OptionHandoffFrom: handoffSource,
				tc.other:          tc.value,
			})
			rc.writeControl(Control{Op: OpLaunch, EndpointID: hello.EndpointID, Launch: &req})
			got := rc.readControl()
			if got.Op != OpError {
				t.Fatalf("reply = %#v; want a refusal", got)
			}
			if got.ErrorCode != CodeInvalidField {
				t.Fatalf("error code = %q; want %q", got.ErrorCode, CodeInvalidField)
			}
			if !strings.Contains(got.Error, OptionHandoffFrom) || !strings.Contains(got.Error, tc.other) {
				t.Fatalf("refusal %q does not name both %q and %q; the combination must be refused BY NAME",
					got.Error, OptionHandoffFrom, tc.other)
			}
			if specs := stub.launchSpecs(); len(specs) != 0 {
				t.Fatalf("exclusion refusal reached DaemonAPI.Launch: %#v", specs)
			}
		})
	}
}

// TestHandsOffHandoff_EmptyValueIsAbsent pins the present-but-empty decision, which
// is otherwise accidental. `handoff_from: ""` means ABSENT, exactly as the two resume
// keys already treat "" (handleLaunch tests `!= ""`), so every handoff guard stays
// inert and the request is an ordinary launch: no capability is required and nothing
// is refused. A caller that means a handoff must send the source id.
func TestHandsOffHandoff_EmptyValueIsAbsent(t *testing.T) {
	stub := newStubDaemon()
	rc := rawDial(t, serveStub(t, stub))
	hello := rc.hello(Version, nil) // deliberately NO capability offered
	req := handoffLaunchReq(t, map[string]string{OptionHandoffFrom: ""})
	rc.writeControl(Control{Op: OpLaunch, EndpointID: hello.EndpointID, Launch: &req})
	if got := rc.readControl(); got.Op == OpError {
		t.Fatalf("empty %s refused: %#v; present-but-empty must behave as absent", OptionHandoffFrom, got)
	}
	if specs := stub.launchSpecs(); len(specs) != 1 {
		t.Fatalf("launch specs = %#v; want one ordinary launch", specs)
	}
}

// TestHandsOffHandoff_RawFrames inspects the BYTES on the wire rather than the
// decoded Go structs: a decoded assertion cannot see a field that was added to the
// roster type, and this slice's whole claim is that nothing new reaches it.
func TestHandsOffHandoff_RawFrames(t *testing.T) {
	t.Run("owner hello reply advertises the capability", func(t *testing.T) {
		rc := rawDial(t, serveStub(t, newStubDaemon()))
		rc.writeControl(Control{Op: OpHello, ProtocolVersion: Version, Capabilities: []string{CapHandsOffHandoff}})
		typ, payload, err := rc.readFrame()
		if err != nil {
			t.Fatalf("read hello frame: %v", err)
		}
		if typ != wire.TControl {
			t.Fatalf("frame type = %d, want TControl", typ)
		}
		if !bytes.Contains(payload, []byte(CapHandsOffHandoff)) {
			t.Fatalf("hello reply frame %s does not advertise %q", payload, CapHandsOffHandoff)
		}
	})

	t.Run("remote tier session frames carry nothing new", func(t *testing.T) {
		stub := newStubDaemon()
		sock := serveRemoteAPI(t, allowAllLaunchPolicy{stub})
		rc := rawDial(t, sock)
		hello := rc.hello(Version, []string{CapRemoteGateway, CapHandsOffHandoff})
		req := handoffLaunchReq(t, map[string]string{OptionHandoffFrom: handoffSource})
		rc.writeControl(remoteLaunchControl(hello.EndpointID, req))

		// The refusal frame must not echo the source session id back to the phone, and
		// must carry no `session` object at all -- a launch reply carrying one is proof
		// the remote refusal did not happen and a session was created.
		_, refusal, err := rc.readFrame()
		if err != nil {
			t.Fatalf("read refusal frame: %v", err)
		}
		if bytes.Contains(refusal, []byte(handoffSource)) {
			t.Fatalf("remote refusal frame %s echoes the source session id", refusal)
		}
		if bytes.Contains(refusal, []byte(`"session"`)) {
			t.Fatalf("remote refusal frame %s carries a session; the refusal must have zero side effects", refusal)
		}

		// The roster is the frame this slice promised not to touch.
		rc.writeControl(Control{Op: OpList, EndpointID: hello.EndpointID})
		typ, roster, err := rc.readFrame()
		if err != nil {
			t.Fatalf("read list frame: %v", err)
		}
		if typ != wire.TControl {
			t.Fatalf("frame type = %d, want TControl", typ)
		}
		for _, leaked := range []string{OptionHandoffFrom, CapHandsOffHandoff} {
			if bytes.Contains(roster, []byte(leaked)) {
				t.Fatalf("remote list frame %s carries %q; no roster field was added by this slice", roster, leaked)
			}
		}
	})
}
