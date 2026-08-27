package protocol

// FAILING-FIRST (TDD RED, GG-5) for Wave R5 deliverables 2+4's daemon-boundary half
// (bead agents-tracker-hggx.6, playbook "Wave R5", ADR-007 B144(b)): the REAL
// session_launch / launch_presets / operation_status handlers behind the SAME
// requireRemoteAuthz choke point every mutating op rides -- replacing the Wave R1
// op_not_implemented stub (handleRefusalOp) that r1_refusalops_test.go pinned. When
// this slice lands, that pin's session_launch/operation_status rows are superseded BY
// DESIGN (the op becomes implemented); the R1 file's forged-signature and
// missing-fields rows are inherited here unchanged and re-asserted against the real
// handler, so nothing about the choke-point ordering weakens.
//
// The contract this file freezes:
//
//   - Wire: OpLaunchPresets/ActionLaunchPresets ("launch_presets"); Control gains
//     SessionLaunch *schema.SessionLaunchReq{PresetID, PresetRevision, InitialPrompt,
//     Cols, Rows}, Presets []schema.LaunchPresetView, PresetPolicyRevision string, and
//     OperationOutcome *schema.OperationOutcomeView{State, SessionID}, and
//     SubjectOperationID string -- operation_status's QUERY SUBJECT, distinct from
//     the query's own OperationID exactly as ADR-007 D7 separates operation_id from
//     interaction_id: the asking op and the asked-about op are different coordinates.
//   - schema.SessionLaunchContentHash(req) binds the signed tuple to the preset id,
//     revision and prompt -- a gateway cannot swap WHICH preset a valid signature
//     launches (R-POL.9's LaunchContentHash rule, applied to the preset op).
//   - Stable codes: schema.CodeUnknownPreset ("unknown_preset") and
//     schema.CodeStalePreset ("stale_preset"), joining the existing kill_switch /
//     not_authorized / policy / invalid_field taxonomy. Every refusal below fires
//     BEFORE argv composition -- observable here as "the backend's Launch was never
//     called", since argv is composed at/after daemon Launch (skeleton adapter).
//   - Backend seams (fail-closed absent, mirroring LaunchPolicy): LaunchPresetSource
//     { LaunchPresetList() ([]schema.LaunchPresetView, string);
//       ResolveLaunchPreset(id, rev string) (schema.LaunchPresetView, error) } with
//     sentinels ErrUnknownPreset/ErrStalePreset; OperationStatusSource
//     { RemoteOperationOutcome(op string) (schema.OperationOutcomeView, bool) };
//     ActivityRecorder { RecordRemoteActivity(schema.ActivityRecord) } (ADR-007 D10:
//     every remote-originated mutation -- and its refusal -- is logged).
//   - Outcome states: schema.OutcomeApplied ("applied"), schema.OutcomeUnknown
//     ("outcome_unknown"), schema.OutcomeUnknownOperation ("unknown_operation").
//   - Composition: the launch spec handed to the daemon comes from the RESOLVED preset
//     alone (resolved root as Cwd, preset options, agent), carries the signed
//     operation_id, carries NO client env, and flows through the EXISTING remote-tier
//     denylist + allowed-root policy -- the exact path handleLaunch already walks, not
//     a parallel one (deliverable 3).
//
// This file must fail to compile (undefined: OpLaunchPresets, schema.SessionLaunchReq,
// schema.CodeStalePreset, ...) until the GREEN slice adds the surface.

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// The three authenticator denials of TestR5SessionLaunch_ForgedSignature... -- the
// authenticator is ONE choke point, so a wrong key, a revoked (unknown) device, and an
// insufficient tier are all its errors; errForged is remote_devicesig_test.go's.
var (
	errUnknownDevice = errors.New(`unknown device "devA"`)
	errTierForbids   = errors.New(`device "devA" capability does not permit "session_launch"`)
	errNotUnderRoot  = errors.New("cwd is not under an allowed remote launch root")
)

// r5Backend is stubDaemon plus every R5 seam: preset custody, allowed-root policy,
// operation outcomes, and the activity log. It satisfies the remote-tier construction
// guards through the embedded stubDaemon (KillSwitch on, OperationClaimer fresh).
type r5Backend struct {
	*stubDaemon

	r5mu      sync.Mutex
	presets   []schema.LaunchPresetView
	policyRev string
	allow     func(resolvedCwd string) error // nil => allow every root
	outcomes  map[string]schema.OperationOutcomeView
	resolves  []string // "<id>@<revision>" per ResolveLaunchPreset call, in order
	activity  []schema.ActivityRecord
}

func newR5Backend(presets ...schema.LaunchPresetView) *r5Backend {
	return &r5Backend{
		stubDaemon: newStubDaemon(),
		presets:    presets,
		policyRev:  "policy-rev-1",
		outcomes:   map[string]schema.OperationOutcomeView{},
	}
}

func (b *r5Backend) RemoteLaunchAllowed(resolvedCwd string) error {
	if b.allow != nil {
		return b.allow(resolvedCwd)
	}
	return nil
}

func (b *r5Backend) LaunchPresetList() ([]schema.LaunchPresetView, string) {
	b.r5mu.Lock()
	defer b.r5mu.Unlock()
	return append([]schema.LaunchPresetView(nil), b.presets...), b.policyRev
}

func (b *r5Backend) ResolveLaunchPreset(id, rev string) (schema.LaunchPresetView, error) {
	b.r5mu.Lock()
	b.resolves = append(b.resolves, id+"@"+rev)
	presets := b.presets
	b.r5mu.Unlock()
	for _, p := range presets {
		if p.ID != id {
			continue
		}
		if p.Revision != rev {
			return schema.LaunchPresetView{}, ErrStalePreset
		}
		return p, nil
	}
	return schema.LaunchPresetView{}, ErrUnknownPreset
}

func (b *r5Backend) RemoteOperationOutcome(op string) (schema.OperationOutcomeView, bool) {
	b.r5mu.Lock()
	defer b.r5mu.Unlock()
	out, ok := b.outcomes[op]
	return out, ok
}

func (b *r5Backend) RecordRemoteActivity(rec schema.ActivityRecord) {
	b.r5mu.Lock()
	defer b.r5mu.Unlock()
	b.activity = append(b.activity, rec)
}

func (b *r5Backend) resolveCalls() []string {
	b.r5mu.Lock()
	defer b.r5mu.Unlock()
	return append([]string(nil), b.resolves...)
}

func (b *r5Backend) activityRecords() []schema.ActivityRecord {
	b.r5mu.Lock()
	defer b.r5mu.Unlock()
	return append([]schema.ActivityRecord(nil), b.activity...)
}

// r5KillOff is r5Backend with the kill switch OFF (overrides the embedded default).
type r5KillOff struct{ *r5Backend }

func (r5KillOff) RemoteControlEnabled() bool { return false }

// r5PresetView is a well-formed machine-authored preset view over an existing dir.
func r5PresetView(t *testing.T, id string) schema.LaunchPresetView {
	t.Helper()
	return schema.LaunchPresetView{
		ID:          id,
		DisplayName: "API repo",
		Agent:       "claude",
		Root:        t.TempDir(),
		Options:     map[string]string{"model": "sonnet"},
		Worktree:    true,
		Revision:    "rev-1",
	}
}

// r5Frame builds a signed session_launch control frame over body.
func r5Frame(rep Control, opID string, body *schema.SessionLaunchReq) Control {
	exp := time.Now().Add(time.Minute)
	return Control{
		Op: OpSessionLaunch, EndpointID: rep.EndpointID,
		OperationID: opID, DeviceID: "devA", DeviceSig: "sig", ExpiresAt: &exp,
		BodyVersion:   schema.CurrentProfileVersion,
		SessionLaunch: body,
	}
}

// TestR5SessionLaunch_KillSwitchOffRefusesBeforePresetResolution: the kill switch is
// the FIRST gate (R-KS.1). Off, the refusal is CodeKillSwitch and neither the preset
// source nor the daemon is ever consulted -- well before any argv could exist.
func TestR5SessionLaunch_KillSwitchOffRefusesBeforePresetResolution(t *testing.T) {
	b := newR5Backend(r5PresetView(t, "preset-api"))
	sock := serveRemoteAPI(t, r5KillOff{b})
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	rc.writeControl(r5Frame(rep, "devA:01JKS", &schema.SessionLaunchReq{
		PresetID: "preset-api", PresetRevision: "rev-1", Cols: 80, Rows: 24,
	}))
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeKillSwitch {
		t.Fatalf("kill-switch-off session_launch = op %q code %q, want error/kill_switch", got.Op, got.ErrorCode)
	}
	if n := len(b.resolveCalls()); n != 0 {
		t.Errorf("preset source consulted %d times behind an OFF kill switch, want 0", n)
	}
	if n := len(b.launchSpecs()); n != 0 {
		t.Errorf("daemon Launch called %d times behind an OFF kill switch, want 0", n)
	}
}

// TestR5SessionLaunch_ForgedSignatureRefusedBeforePresetResolution: the device
// signature verifies BEFORE anything R5 added runs. A wrong key, a revoked pairing,
// and an insufficient capability tier (read_only / read_approve) all surface here as
// the authenticator's error -- one choke point, one stable not_authorized code
// (R-POL.9; the tier mapping itself is skeleton's actionClass, already control-class
// for session_launch).
func TestR5SessionLaunch_ForgedSignatureRefusedBeforePresetResolution(t *testing.T) {
	for _, tc := range []struct {
		name string
		deny error
	}{
		{"wrong-key", errForged},
		{"revoked-pairing", errUnknownDevice},
		{"read-only-tier", errTierForbids},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newR5Backend(r5PresetView(t, "preset-api"))
			b.authzFn = func(DeviceCommandAuth) error { return tc.deny }
			sock := serveRemoteAPI(t, b)
			rc := rawDial(t, sock)
			rep := rc.hello(Version, []string{CapRemoteGateway})

			rc.writeControl(r5Frame(rep, "devA:01J"+tc.name, &schema.SessionLaunchReq{
				PresetID: "preset-api", PresetRevision: "rev-1", Cols: 80, Rows: 24,
			}))
			got := rc.readControl()
			if got.Op != OpError || got.ErrorCode != CodeNotAuthorized {
				t.Fatalf("%s = op %q code %q, want error/not_authorized", tc.name, got.Op, got.ErrorCode)
			}
			if n := len(b.resolveCalls()); n != 0 {
				t.Errorf("preset source consulted %d times for an unauthorized frame, want 0", n)
			}
			if n := len(b.launchSpecs()); n != 0 {
				t.Errorf("daemon Launch called %d times for an unauthorized frame, want 0", n)
			}
		})
	}
}

// TestR5SessionLaunch_MissingBodyIsInvalidField: a session_launch naming no preset
// body is structurally invalid -- refused CodeInvalidField, nothing resolved, nothing
// launched. (The gateway refuses a stripped body too; this is the daemon's own gate,
// which cannot rely on the gateway's.)
func TestR5SessionLaunch_MissingBodyIsInvalidField(t *testing.T) {
	b := newR5Backend(r5PresetView(t, "preset-api"))
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	rc.writeControl(r5Frame(rep, "devA:01JNOBODY", nil))
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeInvalidField {
		t.Fatalf("bodyless session_launch = op %q code %q, want error/invalid_field", got.Op, got.ErrorCode)
	}
	if n := len(b.launchSpecs()); n != 0 {
		t.Errorf("daemon Launch called %d times for a bodyless frame, want 0", n)
	}
}

// TestR5SessionLaunch_UnknownPresetRefusesItsStableCode: an id this machine never
// authored answers unknown_preset -- its OWN stable code, distinct from stale_preset
// (different remedy: re-list, not re-confirm) -- and no launch happens.
func TestR5SessionLaunch_UnknownPresetRefusesItsStableCode(t *testing.T) {
	b := newR5Backend(r5PresetView(t, "preset-api"))
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	rc.writeControl(r5Frame(rep, "devA:01JUNK", &schema.SessionLaunchReq{
		PresetID: "preset-never-authored", PresetRevision: "rev-1", Cols: 80, Rows: 24,
	}))
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != schema.CodeUnknownPreset {
		t.Fatalf("unknown preset = op %q code %q, want error/unknown_preset", got.Op, got.ErrorCode)
	}
	if n := len(b.launchSpecs()); n != 0 {
		t.Errorf("daemon Launch called %d times for an unknown preset, want 0: the refusal "+
			"precedes argv composition", n)
	}
}

// TestR5SessionLaunch_StaleRevisionRefusesStalePreset: the right id at a changed
// revision answers stale_preset (playbook:447-448: "a changed revision receives
// stale_preset instead of silently launching different policy"), names the preset in
// its text, and launches nothing.
func TestR5SessionLaunch_StaleRevisionRefusesStalePreset(t *testing.T) {
	b := newR5Backend(r5PresetView(t, "preset-api")) // current revision is rev-1
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	rc.writeControl(r5Frame(rep, "devA:01JSTALE", &schema.SessionLaunchReq{
		PresetID: "preset-api", PresetRevision: "rev-0-confirmed-before-the-edit", Cols: 80, Rows: 24,
	}))
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != schema.CodeStalePreset {
		t.Fatalf("stale revision = op %q code %q, want error/stale_preset", got.Op, got.ErrorCode)
	}
	if !strings.Contains(got.Error, "preset-api") {
		t.Errorf("stale_preset text = %q, want it to name the preset so the phone can re-list it", got.Error)
	}
	if n := len(b.launchSpecs()); n != 0 {
		t.Errorf("daemon Launch called %d times for a stale preset, want 0", n)
	}
}

// TestR5SessionLaunch_ComposesFromThePresetAloneAndBindsTheContentHash: the success
// path. The signed tuple binds SessionLaunchContentHash(body) (so a gateway cannot
// swap which preset launches under a valid signature), the authz session is the R1
// OperationSessionSentinel (a launch names no pre-existing session), and the spec the
// daemon receives is composed from the RESOLVED preset alone: its root as Cwd, its
// agent, its options, NO client env, the signed operation id, and the phone's one
// free-text contribution (the initial prompt).
func TestR5SessionLaunch_ComposesFromThePresetAloneAndBindsTheContentHash(t *testing.T) {
	p := r5PresetView(t, "preset-api")
	b := newR5Backend(p)
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	body := &schema.SessionLaunchReq{
		PresetID: "preset-api", PresetRevision: "rev-1",
		InitialPrompt: "fix the flaky test", Cols: 80, Rows: 24,
	}
	rc.writeControl(r5Frame(rep, "devA:01JOK", body))
	got := rc.readControl()
	if got.Op != OpSessionLaunch || got.Session == nil {
		t.Fatalf("session_launch success = op %q (session %v), want op session_launch with a session view", got.Op, got.Session)
	}
	if got.OperationID != "devA:01JOK" {
		t.Errorf("success reply operation_id = %q, want devA:01JOK (replies are claimed by op id)", got.OperationID)
	}

	tuples := b.authorizedTuples()
	if len(tuples) != 1 {
		t.Fatalf("authenticator saw %d tuples, want 1", len(tuples))
	}
	if tuples[0].Session != OperationSessionSentinel {
		t.Errorf("authz session = %q, want the operation sentinel %q", tuples[0].Session, OperationSessionSentinel)
	}
	if want := schema.SessionLaunchContentHash(body); !bytes.Equal(tuples[0].ContentHash, want) {
		t.Errorf("authz content hash = %x, want SessionLaunchContentHash(body) %x: without the "+
			"binding a gateway could re-point a valid signature at a different preset", tuples[0].ContentHash, want)
	}

	specs := b.launchSpecs()
	if len(specs) != 1 {
		t.Fatalf("daemon Launch called %d times, want 1", len(specs))
	}
	spec := specs[0]
	if spec.Cwd != p.Root {
		t.Errorf("spec.Cwd = %q, want the preset root %q (already canonical here)", spec.Cwd, p.Root)
	}
	if spec.AgentType != "claude" {
		t.Errorf("spec.AgentType = %q, want the preset's provider claude", spec.AgentType)
	}
	if len(spec.ClientEnv) != 0 {
		t.Errorf("spec.ClientEnv = %v, want empty: no phone-supplied env, and the preset path "+
			"injects none either", spec.ClientEnv)
	}
	if spec.Options["model"] != "sonnet" {
		t.Errorf("spec.Options = %v, want the preset's own options", spec.Options)
	}
	if spec.OperationID != "devA:01JOK" {
		t.Errorf("spec.OperationID = %q, want the signed op id so the existing two-phase "+
			"reservation engages", spec.OperationID)
	}
	if spec.InitialPrompt != "fix the flaky test" {
		t.Errorf("spec.InitialPrompt = %q, want the phone's prompt", spec.InitialPrompt)
	}
}

// TestR5SessionLaunch_ForbiddenPresetOptionRefusedAtTheSameChokePoint: a preset that
// (mis)carries a hard-forbidden option value is refused CodePolicy by the SAME
// hard-coded remote denylist free-form launch rides (R-POL.4) -- the preset path is
// the exact remote execution path, so machine-side authoring mistakes cannot bypass
// the hard-coded floor.
func TestR5SessionLaunch_ForbiddenPresetOptionRefusedAtTheSameChokePoint(t *testing.T) {
	p := r5PresetView(t, "preset-danger")
	p.Options = map[string]string{"dangerously-skip-permissions": "true"}
	b := newR5Backend(p)
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	rc.writeControl(r5Frame(rep, "devA:01JDANGER", &schema.SessionLaunchReq{
		PresetID: "preset-danger", PresetRevision: "rev-1", Cols: 80, Rows: 24,
	}))
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodePolicy {
		t.Fatalf("forbidden preset option = op %q code %q, want error/policy: the hard-coded "+
			"denylist is not bypassable by authoring it into a preset", got.Op, got.ErrorCode)
	}
	if n := len(b.launchSpecs()); n != 0 {
		t.Errorf("daemon Launch called %d times for a forbidden option, want 0", n)
	}
}

// TestR5SessionLaunch_HandoffFromPresetRefusedOnPresence closes a bypass an adversarial
// review found: `session_launch` does NOT pass through handleLaunch, where the hands-off
// handoff's tier, capability, empty-value and exclusion guards all live. It resolves a
// signed preset, copies its options wholesale, and calls Launch directly. The only option
// guard on this path is the value-aware denylist, and that denylist matches ONE forbidden
// value per key -- it cannot express "every value of this key is forbidden".
//
// So an authored preset carrying handoff_from had two ways through:
//
//   - handoff_from=<id> executed an OWNER-TIER-ONLY feature from the REMOTE route, with
//     CapHandsOffHandoff never negotiated;
//   - handoff_from= (empty) reached the core, whose emptiness test read the key as ABSENT
//     and composed an ordinary launch -- a context-free agent in the owner's checkout,
//     the one outcome ADR-010 Amendment 4 E7 says must never happen.
//
// Refused on PRESENCE for exactly that reason: the empty case is the dangerous one, and a
// value-shaped guard would have missed it.
func TestR5SessionLaunch_HandoffFromPresetRefusedOnPresence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"named source", "ep-1/source-session"},
		{"empty value is the dangerous one", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := r5PresetView(t, "preset-handoff")
			p.Options = map[string]string{OptionHandoffFrom: tc.value}
			b := newR5Backend(p)
			sock := serveRemoteAPI(t, b)
			rc := rawDial(t, sock)
			rep := rc.hello(Version, []string{CapRemoteGateway})

			rc.writeControl(r5Frame(rep, "devA:01JHANDOFF", &schema.SessionLaunchReq{
				PresetID: "preset-handoff", PresetRevision: "rev-1", Cols: 80, Rows: 24,
			}))
			got := rc.readControl()
			if got.Op != OpError || got.ErrorCode != CodePolicy {
				t.Fatalf("preset carrying %s=%q = op %q code %q, want error/policy: hands-off "+
					"handoff is owner-tier only and session_launch does not pass through "+
					"handleLaunch's tier guard", OptionHandoffFrom, tc.value, got.Op, got.ErrorCode)
			}
			if n := len(b.launchSpecs()); n != 0 {
				t.Errorf("daemon Launch called %d times for a handoff_from preset, want 0", n)
			}
		})
	}
}

// TestR5SessionLaunch_PresetRootOutsideAllowedRootsRefused: the machine-configured
// allowed-root policy (R-POL.3) applies to the preset path unchanged: a preset whose
// root falls outside the configured roots is refused CodePolicy before any launch.
func TestR5SessionLaunch_PresetRootOutsideAllowedRootsRefused(t *testing.T) {
	p := r5PresetView(t, "preset-outside")
	b := newR5Backend(p)
	b.allow = func(resolvedCwd string) error {
		return errNotUnderRoot // every root is outside the allowed set
	}
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	rc.writeControl(r5Frame(rep, "devA:01JROOT", &schema.SessionLaunchReq{
		PresetID: "preset-outside", PresetRevision: "rev-1", Cols: 80, Rows: 24,
	}))
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodePolicy {
		t.Fatalf("out-of-root preset = op %q code %q, want error/policy", got.Op, got.ErrorCode)
	}
	if n := len(b.launchSpecs()); n != 0 {
		t.Errorf("daemon Launch called %d times for an out-of-root preset, want 0", n)
	}
}

// TestR5LaunchPresets_AnswersMachineAuthoredIDsAndPolicyRevision: the launch_presets
// read rides the same signed-frame plane and answers EXACTLY the machine-authored
// list plus its policy revision; a machine with no presets answers an empty list,
// never an invented default.
func TestR5LaunchPresets_AnswersMachineAuthoredIDsAndPolicyRevision(t *testing.T) {
	p := r5PresetView(t, "preset-api")
	b := newR5Backend(p)
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	exp := time.Now().Add(time.Minute)
	rc.writeControl(Control{
		Op: OpLaunchPresets, EndpointID: rep.EndpointID,
		OperationID: "devA:01JLIST", DeviceID: "devA", DeviceSig: "sig", ExpiresAt: &exp,
		BodyVersion: schema.CurrentProfileVersion,
	})
	got := rc.readControl()
	if got.Op != OpLaunchPresets {
		t.Fatalf("launch_presets reply op = %q (err %q code %q), want launch_presets", got.Op, got.Error, got.ErrorCode)
	}
	if len(got.Presets) != 1 || got.Presets[0].ID != "preset-api" || got.Presets[0].Revision != "rev-1" {
		t.Errorf("launch_presets reply = %+v, want the one machine-authored preset with its revision", got.Presets)
	}
	if got.PresetPolicyRevision != "policy-rev-1" {
		t.Errorf("policy revision = %q, want policy-rev-1", got.PresetPolicyRevision)
	}
	tuples := b.authorizedTuples()
	if len(tuples) != 1 || tuples[0].Action != ActionLaunchPresets {
		t.Fatalf("authz tuples = %+v, want one launch_presets authorization: the list is signed-"+
			"plane like every semantic op", tuples)
	}

	// Empty custody answers empty.
	b2 := newR5Backend()
	sock2 := serveRemoteAPI(t, b2)
	rc2 := rawDial(t, sock2)
	rep2 := rc2.hello(Version, []string{CapRemoteGateway})
	rc2.writeControl(Control{
		Op: OpLaunchPresets, EndpointID: rep2.EndpointID,
		OperationID: "devA:01JLIST2", DeviceID: "devA", DeviceSig: "sig", ExpiresAt: &exp,
		BodyVersion: schema.CurrentProfileVersion,
	})
	got2 := rc2.readControl()
	if got2.Op != OpLaunchPresets || len(got2.Presets) != 0 {
		t.Errorf("empty custody answered %+v, want an empty preset list -- never a fabricated default", got2.Presets)
	}
}

// TestR5OperationStatus_AnswersAuthoritativeOrOutcomeUnknownNeverInvents: the real
// operation_status handler reads the reconciliation surface and only reads it --
// applied is authoritative with its session id, outcome_unknown is honest
// undecidability, and an id the machine has no record of answers unknown_operation
// rather than an invented state. It never calls Launch (it cannot authorize a retry).
func TestR5OperationStatus_AnswersAuthoritativeOrOutcomeUnknownNeverInvents(t *testing.T) {
	b := newR5Backend()
	b.outcomes["devA:01JDONE"] = schema.OperationOutcomeView{State: schema.OutcomeApplied, SessionID: "sess7"}
	b.outcomes["devA:01JLOST"] = schema.OperationOutcomeView{State: schema.OutcomeUnknown}
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	ask := func(subject string) Control {
		exp := time.Now().Add(time.Minute)
		rc.writeControl(Control{
			Op: OpOperationStatus, EndpointID: rep.EndpointID,
			OperationID: "devA:01JQ-" + subject, DeviceID: "devA", DeviceSig: "sig", ExpiresAt: &exp,
			BodyVersion: schema.CurrentProfileVersion,
			SubjectOperationID: subject,
		})
		return rc.readControl()
	}

	if got := ask("devA:01JDONE"); got.Op != OpOperationStatus ||
		got.OperationOutcome == nil || got.OperationOutcome.State != schema.OutcomeApplied ||
		got.OperationOutcome.SessionID != "sess7" {
		t.Errorf("operation_status(applied) = %+v, want applied/sess7", got.OperationOutcome)
	}
	if got := ask("devA:01JLOST"); got.OperationOutcome == nil || got.OperationOutcome.State != schema.OutcomeUnknown {
		t.Errorf("operation_status(undecidable) = %+v, want outcome_unknown -- never silent, never guessed", got.OperationOutcome)
	}
	if got := ask("devA:01JGHOST"); got.OperationOutcome == nil || got.OperationOutcome.State != schema.OutcomeUnknownOperation {
		t.Errorf("operation_status(unknown id) = %+v, want unknown_operation -- an invented outcome "+
			"would let the phone resolve an op the machine never saw", got.OperationOutcome)
	}
	if n := len(b.launchSpecs()); n != 0 {
		t.Errorf("operation_status called Launch %d times, want 0: status never authorizes a retry", n)
	}
}

// TestR5SessionLaunch_ActivityRecordedForOutcomeAndRefusal: ADR-007 D10's activity
// log covers the new verb: a successful session_launch appends a record naming the
// action, device, operation id and outcome; a stale_preset refusal appends one naming
// the refusal code. A remote mutation that leaves no trace -- or a refusal that does
// not -- is invisible to the terminal owner auditing the machine.
func TestR5SessionLaunch_ActivityRecordedForOutcomeAndRefusal(t *testing.T) {
	b := newR5Backend(r5PresetView(t, "preset-api"))
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	rc.writeControl(r5Frame(rep, "devA:01JACT1", &schema.SessionLaunchReq{
		PresetID: "preset-api", PresetRevision: "rev-1", Cols: 80, Rows: 24,
	}))
	if got := rc.readControl(); got.Op != OpSessionLaunch {
		t.Fatalf("setup launch failed: op %q err %q", got.Op, got.Error)
	}
	rc.writeControl(r5Frame(rep, "devA:01JACT2", &schema.SessionLaunchReq{
		PresetID: "preset-api", PresetRevision: "rev-stale", Cols: 80, Rows: 24,
	}))
	if got := rc.readControl(); got.ErrorCode != schema.CodeStalePreset {
		t.Fatalf("setup refusal failed: op %q code %q", got.Op, got.ErrorCode)
	}

	recs := b.activityRecords()
	if len(recs) != 2 {
		t.Fatalf("activity log has %d records after one launch and one refusal, want 2: every "+
			"remote-originated mutation AND its refusal is logged (D10)", len(recs))
	}
	ok0 := recs[0]
	if ok0.Action != ActionSessionLaunch || ok0.DeviceID != "devA" || ok0.OperationID != "devA:01JACT1" {
		t.Errorf("success record = %+v, want session_launch/devA/devA:01JACT1", ok0)
	}
	if ok0.Outcome != schema.OutcomeApplied {
		t.Errorf("success record outcome = %q, want %q", ok0.Outcome, schema.OutcomeApplied)
	}
	ref := recs[1]
	if ref.Outcome != "refused" || ref.Code != schema.CodeStalePreset {
		t.Errorf("refusal record = %+v, want outcome refused with code stale_preset", ref)
	}
}
