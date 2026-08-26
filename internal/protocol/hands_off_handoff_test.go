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

// TestHandsOffHandoff_EmptyValueIsRefused pins ADR-010 Amendment 4 E7: every refusal
// in this flow is named and launches nothing, because an agent loose in the owner's
// checkout with no idea what it is continuing is the worst outcome available -- worse
// than no handoff at all, since the owner would believe the work was carried over.
//
// A caller that SETS handoff_from and computes an empty source id has a bug. Treating
// that as "absent" -- which is what the two resume keys do with "" -- reaches exactly
// the bare context-free launch E7 forbids, by a different route from the one the
// capability gate closes. So hands-off fails closed instead: PRESENT-but-empty is a
// malformed field, while ABSENT stays an ordinary launch requiring no capability.
//
// The two cases are distinguishable at this layer: LaunchReq.Options is a plain
// map[string]string carried by encoding/json with no custom codec, so `{"handoff_from":""}`
// decodes to a PRESENT key and a comma-ok lookup separates it from a key never set.
// The "absent" arm below is what proves that end to end, through the real codec and a
// real socket -- without it, refusing everything would look identical.
func TestHandsOffHandoff_EmptyValueIsRefused(t *testing.T) {
	t.Run("present but empty is refused as a malformed field", func(t *testing.T) {
		stub := newStubDaemon()
		rc := rawDial(t, serveStub(t, stub))
		hello := rc.hello(Version, []string{CapHandsOffHandoff})
		req := handoffLaunchReq(t, map[string]string{OptionHandoffFrom: ""})
		rc.writeControl(Control{Op: OpLaunch, EndpointID: hello.EndpointID, Launch: &req})
		got := rc.readControl()
		if got.Op != OpError || got.ErrorCode != CodeInvalidField {
			t.Fatalf("reply = %#v; want %s refused invalid_field", got, OptionHandoffFrom)
		}
		if !strings.Contains(got.Error, OptionHandoffFrom) {
			t.Fatalf("refusal %q does not name %q", got.Error, OptionHandoffFrom)
		}
		if specs := stub.launchSpecs(); len(specs) != 0 {
			t.Fatalf("empty %s reached DaemonAPI.Launch: %#v; E7 forbids degrading to a bare launch",
				OptionHandoffFrom, specs)
		}
	})

	// E7 again: NO refusal path may launch. A client that both fails to negotiate the
	// capability and sends an empty id is refused by whichever guard fires first, and
	// either way nothing is launched.
	t.Run("present but empty launches nothing without the capability either", func(t *testing.T) {
		stub := newStubDaemon()
		rc := rawDial(t, serveStub(t, stub))
		hello := rc.hello(Version, nil)
		req := handoffLaunchReq(t, map[string]string{OptionHandoffFrom: ""})
		rc.writeControl(Control{Op: OpLaunch, EndpointID: hello.EndpointID, Launch: &req})
		if got := rc.readControl(); got.Op != OpError {
			t.Fatalf("reply = %#v; want a refusal", got)
		}
		if specs := stub.launchSpecs(); len(specs) != 0 {
			t.Fatalf("empty %s reached DaemonAPI.Launch: %#v", OptionHandoffFrom, specs)
		}
	})

	// The tier guard is coarser than the field guard: on the remote tier the key is not
	// permitted AT ALL, so the refusal names the tier rather than the empty value.
	t.Run("present but empty is refused on the remote tier as policy", func(t *testing.T) {
		stub := newStubDaemon()
		rc := rawDial(t, serveRemoteAPI(t, allowAllLaunchPolicy{stub}))
		hello := rc.hello(Version, []string{CapRemoteGateway, CapHandsOffHandoff})
		req := handoffLaunchReq(t, map[string]string{OptionHandoffFrom: ""})
		rc.writeControl(remoteLaunchControl(hello.EndpointID, req))
		got := rc.readControl()
		if got.Op != OpError || got.ErrorCode != CodePolicy {
			t.Fatalf("reply = %#v; want remote policy refusal", got)
		}
		if specs := stub.launchSpecs(); len(specs) != 0 {
			t.Fatalf("empty %s reached DaemonAPI.Launch on the remote tier: %#v", OptionHandoffFrom, specs)
		}
	})

	// ABSENT is untouched: no capability required, no refusal, an ordinary launch.
	// Both an options map that omits the key and no options map at all.
	t.Run("absent stays an ordinary launch", func(t *testing.T) {
		for _, options := range []map[string]string{nil, {"script": "/s.txt"}} {
			stub := newStubDaemon()
			rc := rawDial(t, serveStub(t, stub))
			hello := rc.hello(Version, nil) // deliberately NO capability offered
			req := handoffLaunchReq(t, options)
			rc.writeControl(Control{Op: OpLaunch, EndpointID: hello.EndpointID, Launch: &req})
			if got := rc.readControl(); got.Op == OpError {
				t.Fatalf("launch with options %v refused: %#v; an absent %s must not require the capability",
					options, got, OptionHandoffFrom)
			}
			if specs := stub.launchSpecs(); len(specs) != 1 {
				t.Fatalf("launch specs = %#v; want one ordinary launch for options %v", specs, options)
			}
		}
	})
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
