package protocol

// FAILING-FIRST (TDD RED, GG-5) for the Wave R5 round-2 review fix-pack (bead
// agents-tracker-hggx.6), protocol half:
//
//   - MAJOR 1: phone-supplied bytes must not reach argv AS FLAGS. The adapters append
//     InitialPrompt as the last argv token with no `--` separator, so a prompt of
//     `--dangerously-skip-permissions` would be parsed by the CLI as the exact flag
//     remoteForbiddenOptions denies three statements earlier. On the REMOTE tier a
//     flag-shaped prompt (leading `-` after space-trim) is refused CodePolicy BEFORE
//     any launch -- the same one-line guard optionFlags already applies to `model`.
//   - MAJOR 2: subject_operation_id must be BOUND into the signed tuple.
//     handleOperationStatus authorized with contentHash=nil and then read
//     c.SubjectOperationID straight off the wire, so a compromised gateway could
//     re-point any validly-signed status query at another operation id (the file's
//     own R-POL.9 comment states the rule it skipped). The binding is
//     schema.OperationStatusContentHash(subject), recomputed daemon-side from the
//     forwarded subject exactly as SessionLaunchContentHash is.
//   - The launch_presets reply names the SIGNING device's own capability tier
//     (device_capability), read machine-side from the pinned registry via the
//     optional DeviceCapabilitySource seam -- never from the wire, and never
//     invented when the backend has no seam. It is the phone's only honest source
//     for the TIER_FORBIDS availability state (capability is pinned at enrollment
//     and no other reply carries it to the device it describes).
//
// This file must fail (compile-RED on schema.OperationStatusContentHash /
// DeviceCapabilitySource / Control.DeviceCapability, behavioral-RED on the prompt
// guard) until the round-2 GREEN slice lands.

import (
	"bytes"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// TestR5Round2_FlagShapedPromptRefusedBeforeLaunch: a remote initial prompt whose
// first non-space byte is `-` is refused CodePolicy with ZERO backend Launch calls
// and a D10 refusal record -- the denylist floor cannot be bypassed by typing the
// forbidden flag into the prompt box.
func TestR5Round2_FlagShapedPromptRefusedBeforeLaunch(t *testing.T) {
	for _, tc := range []struct{ name, prompt string }{
		{"exact-forbidden-flag", "--dangerously-skip-permissions"},
		{"any-flag-shape", "-rf tmp"},
		{"space-padded", "  --model opus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newR5Backend(r5PresetView(t, "preset-api"))
			sock := serveRemoteAPI(t, b)
			rc := rawDial(t, sock)
			rep := rc.hello(Version, []string{CapRemoteGateway})

			rc.writeControl(r5Frame(rep, "devA:01JR2-"+tc.name, &schema.SessionLaunchReq{
				PresetID: "preset-api", PresetRevision: "rev-1",
				InitialPrompt: tc.prompt, Cols: 80, Rows: 24,
			}))
			got := rc.readControl()
			if got.Op != OpError || got.ErrorCode != CodePolicy {
				t.Fatalf("flag-shaped prompt = op %q code %q, want error/policy: the adapters append "+
					"the prompt as a bare argv token, so a leading dash IS a flag to the CLI", got.Op, got.ErrorCode)
			}
			if n := len(b.launchSpecs()); n != 0 {
				t.Errorf("daemon Launch called %d times for a flag-shaped prompt, want 0: the refusal "+
					"must precede argv composition", n)
			}
			recs := b.activityRecords()
			if len(recs) != 1 || recs[0].Outcome != "refused" {
				t.Errorf("activity records = %+v, want one refusal record (D10)", recs)
			}
		})
	}

	// Negative control: an ordinary prompt still launches -- the guard is a floor,
	// not a prompt lint.
	b := newR5Backend(r5PresetView(t, "preset-api"))
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	rc.writeControl(r5Frame(rep, "devA:01JR2-plain", &schema.SessionLaunchReq{
		PresetID: "preset-api", PresetRevision: "rev-1",
		InitialPrompt: "fix the flaky test - then rerun", Cols: 80, Rows: 24,
	}))
	if got := rc.readControl(); got.Op != OpSessionLaunch {
		t.Fatalf("ordinary prompt refused: op %q err %q code %q -- only a LEADING dash is a flag", got.Op, got.Error, got.ErrorCode)
	}
}

// TestR5Round2_OperationStatusSubjectIsBoundIntoTheSignedTuple: the authz tuple for
// operation_status carries ContentHash = schema.OperationStatusContentHash(subject),
// so a gateway cannot re-point a valid signature at a different operation id --
// the handleDeviceRevoke/session_launch binding rule, applied to the read that
// returns another operation's session id.
func TestR5Round2_OperationStatusSubjectIsBoundIntoTheSignedTuple(t *testing.T) {
	b := newR5Backend()
	b.outcomes["devA:01JSUBJ"] = schema.OperationOutcomeView{State: schema.OutcomeApplied, SessionID: "sess9"}
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	exp := time.Now().Add(time.Minute)
	rc.writeControl(Control{
		Op: OpOperationStatus, EndpointID: rep.EndpointID,
		OperationID: "devA:01JR2ASK", DeviceID: "devA", DeviceSig: "sig", ExpiresAt: &exp,
		BodyVersion:        schema.CurrentProfileVersion,
		SubjectOperationID: "devA:01JSUBJ",
	})
	got := rc.readControl()
	if got.Op != OpOperationStatus || got.OperationOutcome == nil {
		t.Fatalf("operation_status reply = op %q (outcome %v), want an outcome", got.Op, got.OperationOutcome)
	}

	tuples := b.authorizedTuples()
	if len(tuples) != 1 {
		t.Fatalf("authenticator saw %d tuples, want 1", len(tuples))
	}
	want := schema.OperationStatusContentHash("devA:01JSUBJ")
	if len(want) == 0 {
		t.Fatal("OperationStatusContentHash returned nothing; the binding must be a real digest")
	}
	if !bytes.Equal(tuples[0].ContentHash, want) {
		t.Errorf("authz content hash = %x, want OperationStatusContentHash(subject) %x: unbound, "+
			"a compromised gateway can re-point a valid signature at another operation id and "+
			"read back that operation's session id", tuples[0].ContentHash, want)
	}
	if other := schema.OperationStatusContentHash("devA:01JOTHER"); bytes.Equal(want, other) {
		t.Error("two different subjects hash identically; the binding binds nothing")
	}
	if tuples[0].Session != OperationSessionSentinel {
		t.Errorf("authz session = %q, want the operation sentinel %q (the subject binds via the "+
			"content slot, not the session slot)", tuples[0].Session, OperationSessionSentinel)
	}
}

// r5CapBackend is r5Backend plus the DeviceCapabilitySource seam: the machine-side
// pinned-registry read behind the launch_presets reply's device_capability field.
type r5CapBackend struct {
	*r5Backend
	tier  string
	asked []string
}

func (b *r5CapBackend) DeviceCapability(deviceID string) (string, bool) {
	b.r5mu.Lock()
	defer b.r5mu.Unlock()
	b.asked = append(b.asked, deviceID)
	if b.tier == "" {
		return "", false
	}
	return b.tier, true
}

func (b *r5CapBackend) askedIDs() []string {
	b.r5mu.Lock()
	defer b.r5mu.Unlock()
	return append([]string(nil), b.asked...)
}

// TestR5Round2_LaunchPresetsReplyNamesTheSignersTier: the launch_presets reply
// carries the SIGNING device's capability as pinned machine-side -- the phone's one
// honest source for the TIER_FORBIDS availability state -- and a backend without
// the seam answers an empty field, never an invented tier.
func TestR5Round2_LaunchPresetsReplyNamesTheSignersTier(t *testing.T) {
	b := &r5CapBackend{r5Backend: newR5Backend(r5PresetView(t, "preset-api")), tier: "read_only"}
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	exp := time.Now().Add(time.Minute)
	rc.writeControl(Control{
		Op: OpLaunchPresets, EndpointID: rep.EndpointID,
		OperationID: "devA:01JR2TIER", DeviceID: "devA", DeviceSig: "sig", ExpiresAt: &exp,
		BodyVersion: schema.CurrentProfileVersion,
	})
	got := rc.readControl()
	if got.Op != OpLaunchPresets {
		t.Fatalf("launch_presets reply op = %q (err %q)", got.Op, got.Error)
	}
	if got.DeviceCapability != "read_only" {
		t.Errorf("reply device_capability = %q, want the registry-pinned read_only: the phone "+
			"has no other wire source for its own tier", got.DeviceCapability)
	}
	if asked := b.askedIDs(); len(asked) != 1 || asked[0] != "devA" {
		t.Errorf("capability source asked for %v, want exactly the authenticated signer devA "+
			"(never a wire-named device)", asked)
	}

	// Fail-honest absent: no seam, no invented tier.
	b2 := newR5Backend(r5PresetView(t, "preset-api"))
	sock2 := serveRemoteAPI(t, b2)
	rc2 := rawDial(t, sock2)
	rep2 := rc2.hello(Version, []string{CapRemoteGateway})
	rc2.writeControl(Control{
		Op: OpLaunchPresets, EndpointID: rep2.EndpointID,
		OperationID: "devA:01JR2TIER2", DeviceID: "devA", DeviceSig: "sig", ExpiresAt: &exp,
		BodyVersion: schema.CurrentProfileVersion,
	})
	if got2 := rc2.readControl(); got2.DeviceCapability != "" {
		t.Errorf("a backend with no capability seam answered device_capability %q, want empty -- "+
			"a tier must be pinned registry fact or absent, never fabricated", got2.DeviceCapability)
	}
}
