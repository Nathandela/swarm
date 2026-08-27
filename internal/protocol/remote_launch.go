package protocol

// Wave R5 (bead agents-tracker-hggx.6, ADR-007 B144(b), playbook "Wave R5 -- phone
// remote launch"): the REAL session_launch / launch_presets / operation_status
// handlers, replacing the Wave R1 op_not_implemented stub for exactly these ops.
//
// Every handler runs requireRemoteAuthz FIRST -- the SAME choke point kill/delete/
// launch/approve ride -- so the kill switch (R-KS.1, first gate), a forged key, a
// revoked pairing and an insufficient tier are all refused before anything R5 added
// runs. The refusal DECISIONS are machine-side, before any argv composition (argv is
// composed at/after DaemonAPI.Launch), and never trusted from the phone: the phone
// contributes a preset id, the revision it confirmed, and one free-text prompt --
// nothing else survives into the spec.

import (
	"errors"
	"strconv"
	"strings"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

// presetDefaultCols / presetDefaultRows are the launch geometry when the phone named
// none (or an out-of-range value): the conventional grid every other launch surface
// defaults to. Geometry is cosmetic (excluded from the content hash), so a bad value
// is defaulted rather than refused -- a launch must never fail over a display field.
const (
	presetDefaultCols = 80
	presetDefaultRows = 24
)

// launchPresetSource resolves the optional Wave R5 preset custody seam
// (fail-closed absent, mirroring launchPolicy).
func (cc *clientConn) launchPresetSource() (LaunchPresetSource, bool) {
	src, ok := cc.srv.d.(LaunchPresetSource)
	return src, ok
}

// operationStatusSource resolves the optional operation_status reconciliation seam.
func (cc *clientConn) operationStatusSource() (OperationStatusSource, bool) {
	src, ok := cc.srv.d.(OperationStatusSource)
	return src, ok
}

// recordRemoteActivity appends one D10 activity record when the backend keeps a log.
// A backend without an ActivityRecorder simply keeps none (optional-interface rule).
func (cc *clientConn) recordRemoteActivity(rec schema.ActivityRecord) {
	if ar, ok := cc.srv.d.(ActivityRecorder); ok {
		ar.RecordRemoteActivity(rec)
	}
}

// requireBodyVersion enforces the R1 body-version binding shared by the whole
// semantic-op family: schema.CurrentProfileVersion is the sole accepted value, and
// zero (unset) is refused identically -- there is no body version 0.
func (cc *clientConn) requireBodyVersion(c Control) bool {
	if c.BodyVersion != schema.CurrentProfileVersion {
		cc.replyErrorCode(c.Op+": body_version "+strconv.Itoa(c.BodyVersion)+
			" not accepted, this machine accepts "+strconv.Itoa(schema.CurrentProfileVersion), CodeInvalidField)
		return false
	}
	return true
}

// refuseSessionLaunch is the SEMANTIC session_launch refusal: it appends the D10
// activity record (a refusal that leaves no trace is invisible to the terminal owner
// auditing the machine) and then replies the stable code. Structural refusals
// (missing body, wrong body version) and authz refusals stay unrecorded: the former
// are screen bugs, the latter fire before the device identity is trusted at all.
func (cc *clientConn) refuseSessionLaunch(c Control, code ErrorCode, msg string) {
	// Round-4 fix-pack, MAJOR 2: the UNDECIDABLE answer is recorded as undecidable, not
	// as a refusal. The D10 log is what the terminal owner audits when a phone reports a
	// launch it cannot account for, and "refused" written beside an operation that may
	// well be running is the same untruth on disk that the reply avoids on the wire.
	outcome := "refused"
	if code == schema.CodeOutcomeUnknown {
		outcome = schema.OutcomeUnknown
	}
	cc.recordRemoteActivity(schema.ActivityRecord{
		Action:      ActionSessionLaunch,
		DeviceID:    c.DeviceID,
		OperationID: c.OperationID,
		Outcome:     outcome,
		Code:        code,
	})
	cc.replyErrorCode(msg, code)
}

// handleSessionLaunch serves the signed session_launch op: the phone's confirmed
// selection of ONE machine-authored preset. The launch spec is composed from the
// RESOLVED preset alone -- its (already canonical) root as Cwd, its agent, its own
// options COPIED, NO client env ever (D8: env comes from daemon policy, never a
// phone) -- carries the signed operation_id so the EXISTING two-phase idempotent
// reservation engages inside daemon launch, and flows through the EXACT remote
// execution path free-form launch rides: the same hard-coded option denylist
// (R-POL.4) and the same allowed-root LaunchPolicy seam (R-POL.3). Not a parallel
// path (deliverable 3).
func (cc *clientConn) handleSessionLaunch(c Control) {
	req := c.SessionLaunch
	// R-POL.9: the signed tuple binds WHICH preset (id + revision) and the prompt via
	// SessionLaunchContentHash, so a gateway cannot re-point a valid signature at a
	// different preset. Session subject is pinned to OperationSessionSentinel by the
	// dispatch contract (a launch names no pre-existing session), mirroring
	// handleLaunch's unconditional LaunchSessionSentinel.
	if !cc.requireRemoteAuthz(c, ActionSessionLaunch, OperationSessionSentinel, SessionLaunchContentHash(req)) {
		return
	}
	if !cc.requireBodyVersion(c) {
		return
	}
	if req == nil {
		// The gateway refuses a stripped body too; this is the daemon's own gate, which
		// cannot rely on the gateway's.
		cc.replyErrorCode("session_launch: missing session_launch body", CodeInvalidField)
		return
	}
	src, ok := cc.launchPresetSource()
	if !ok {
		// Fail-closed absent, mirroring launchPolicy (F4): a backend without preset
		// custody launches nothing rather than inventing an empty custody.
		cc.refuseSessionLaunch(c, CodePolicy, "session_launch: no launch preset source configured")
		return
	}
	p, err := src.ResolveLaunchPreset(req.PresetID, req.PresetRevision)
	switch {
	case errors.Is(err, ErrUnknownPreset):
		cc.refuseSessionLaunch(c, schema.CodeUnknownPreset,
			"session_launch: preset "+strconv.Quote(req.PresetID)+" is not authored on this machine; list the presets again")
		return
	case errors.Is(err, ErrStalePreset):
		cc.refuseSessionLaunch(c, schema.CodeStalePreset,
			"session_launch: preset "+strconv.Quote(req.PresetID)+" was re-authored after the confirmed revision; pick it again from a fresh list")
		return
	case err != nil:
		cc.refuseSessionLaunch(c, CodePolicy, "session_launch: preset "+strconv.Quote(req.PresetID)+": "+err.Error())
		return
	}
	// Round-2 fix-pack, MAJOR 1: the phone's one free-text field must never reach argv
	// AS A FLAG. The adapters append InitialPrompt as the last argv token with no `--`
	// separator (claude.go/codex.go), so a prompt of `--dangerously-skip-permissions`
	// would be parsed by the CLI as the exact flag remoteForbiddenOptions denies three
	// statements below. On the remote tier a flag-shaped prompt (leading `-` after
	// space-trim) is refused outright -- the same one-line guard optionFlags applies to
	// `model` -- BEFORE any spec exists. A prompt with an interior dash is untouched.
	if strings.HasPrefix(strings.TrimSpace(req.InitialPrompt), "-") {
		cc.refuseSessionLaunch(c, CodePolicy,
			"session_launch: initial prompt begins with '-'; a flag-shaped prompt would reach the agent CLI as a flag and is not permitted on the remote tier")
		return
	}
	// R-POL.4: the SAME hard-coded, value-aware denylist free-form remote launch rides.
	// A machine-side authoring mistake cannot bypass the floor by riding a preset.
	for k, v := range p.Options {
		if forbidden, ok := remoteForbiddenOptions[k]; ok && forbidden == v {
			cc.refuseSessionLaunch(c, CodePolicy,
				"session_launch: preset option "+strconv.Quote(k)+"="+strconv.Quote(v)+" not permitted on the remote tier")
			return
		}
	}
	// handoff_from is OWNER-TIER ONLY (ADR-010 Amendment 4 E1/E7) and this path does not
	// pass through handleLaunch, where that tier guard lives. Refused on PRESENCE, not on
	// value: an authored preset carrying the key at all is refused, whatever it holds.
	// The value-aware denylist above cannot express this -- it matches one forbidden
	// VALUE per key, and here every value is forbidden, including the empty string.
	if _, present := p.Options[OptionHandoffFrom]; present {
		cc.refuseSessionLaunch(c, CodePolicy,
			"session_launch: preset option "+strconv.Quote(OptionHandoffFrom)+" is owner-tier only and not permitted on the remote tier")
		return
	}
	// R-POL.3: the SAME machine-configured allowed-root policy, fail-closed absent
	// (F4). The preset root is stored canonical (resolved at authoring and re-resolved
	// by the preset source), so the path the policy checks is the path the shim gets
	// (D8: no check-on-resolved/use-on-original gap).
	lp, ok := cc.launchPolicy()
	if !ok {
		cc.refuseSessionLaunch(c, CodePolicy, "session_launch: no remote launch policy configured")
		return
	}
	if err := lp.RemoteLaunchAllowed(p.Root); err != nil {
		cc.refuseSessionLaunch(c, CodePolicy, "session_launch: "+err.Error())
		return
	}

	cols, rows := req.Cols, req.Rows
	if cols < 1 || cols > maxDim {
		cols = presetDefaultCols
	}
	if rows < 1 || rows > maxDim {
		rows = presetDefaultRows
	}
	// Composed from the resolved preset ALONE: options copied (a policy object shared
	// by reference is a policy an executed launch can edit), no client env ever, the
	// signed operation_id so the existing two-phase reservation engages, and the
	// phone's one free-text contribution (the initial prompt).
	opts := make(map[string]string, len(p.Options)+1)
	for k, v := range p.Options {
		opts[k] = v
	}
	if p.Worktree {
		opts[OptionWorktree] = "true"
	}
	spec := daemon.LaunchSpec{
		AgentType:     p.Agent,
		Cwd:           p.Root,
		ClientEnv:     nil,
		Cols:          cols,
		Rows:          rows,
		Options:       opts,
		OperationID:   c.OperationID,
		InitialPrompt: req.InitialPrompt,
	}
	m, err := cc.srv.d.Launch(spec)
	if err != nil {
		// Round-4 fix-pack, MAJOR 2: an UNDECIDABLE launch is not a refusal. The daemon
		// answers ErrLaunchOutcomeUnknown when this signed operation is in flight under
		// another driver (the redelivery racing the original), and the honest wire word
		// for that is ADR-017 T9's outcome_unknown -- the same state operation_status
		// reports, reaching the phone one hop earlier. Replied as `policy` the user is
		// told the machine turned the launch away; replied as the success op (the
		// pre-fix behaviour) they are told a session exists that may never have.
		code := CodePolicy
		if errors.Is(err, daemon.ErrLaunchOutcomeUnknown) {
			code = schema.CodeOutcomeUnknown
		}
		cc.refuseSessionLaunch(c, code, "session_launch: "+err.Error())
		return
	}
	cc.recordRemoteActivity(schema.ActivityRecord{
		Action:      ActionSessionLaunch,
		DeviceID:    c.DeviceID,
		OperationID: c.OperationID,
		SessionID:   m.ID,
		Outcome:     schema.OutcomeApplied,
	})
	// OperationID for handleLaunch's stated reason: a reply is claimed BY operation id.
	_ = cc.writeControl(Control{Op: OpSessionLaunch, EndpointID: cc.endpointID, OperationID: cc.opID,
		Session: cc.stampView(m, status.Derive(m.Status))})
}

// handleLaunchPresets serves the signed launch_presets read: EXACTLY the
// machine-authored list plus its policy revision. Empty custody answers an empty
// list -- never an invented default (ADR-007 B135).
func (cc *clientConn) handleLaunchPresets(c Control) {
	if !cc.requireRemoteAuthz(c, ActionLaunchPresets, OperationSessionSentinel, nil) {
		return
	}
	if !cc.requireBodyVersion(c) {
		return
	}
	src, ok := cc.launchPresetSource()
	if !ok {
		cc.replyErrorCode("launch_presets: no launch preset source configured", CodePolicy)
		return
	}
	views, rev := src.LaunchPresetList()
	// Round-2 fix-pack: the reply names the SIGNING device's own registry-pinned tier
	// (the phone's only honest source for its tier-denied availability state). Read
	// through the optional DeviceCapabilitySource seam for the AUTHENTICATED c.DeviceID
	// -- requireRemoteAuthz verified it above -- and left empty when the backend has no
	// seam: an absent fact, never an invented tier.
	tier := ""
	if src, ok := cc.srv.d.(DeviceCapabilitySource); ok {
		if t, known := src.DeviceCapability(c.DeviceID); known {
			tier = t
		}
	}
	_ = cc.writeControl(Control{Op: OpLaunchPresets, EndpointID: cc.endpointID, OperationID: cc.opID,
		Presets: views, PresetPolicyRevision: rev, DeviceCapability: tier})
}

// handleOperationStatus serves the operation_status reconciliation read: it reads the
// two-phase record's answer and ONLY reads it -- applied is authoritative with its
// session id, outcome_unknown is honest undecidability, and an id the machine has no
// record of answers unknown_operation rather than an invented state. It never calls
// Launch: status cannot authorize a retry (playbook:449); the replay of the SAME
// signed operation id through session_launch is the one re-driver.
func (cc *clientConn) handleOperationStatus(c Control) {
	// Round-2 fix-pack, MAJOR 2: the subject IS bound into the signed tuple.
	// Authorizing with a nil content hash and then reading c.SubjectOperationID off
	// the wire let a compromised gateway re-point any validly-signed status query at
	// another operation id -- and operation_status is READ class, so any paired tier
	// could read back that operation's namespaced session id. The binding is the
	// handleDeviceRevoke/session_launch rule applied to this read: the subject rides
	// the content slot (the session slot stays the sentinel -- the query names no
	// session instance), recomputed here from the forwarded subject so a swap breaks
	// the signature.
	if !cc.requireRemoteAuthz(c, ActionOperationStatus, OperationSessionSentinel,
		schema.OperationStatusContentHash(c.SubjectOperationID)) {
		return
	}
	if !cc.requireBodyVersion(c) {
		return
	}
	if c.SubjectOperationID == "" {
		cc.replyErrorCode("operation_status: missing subject_operation_id (the operation being asked about)", CodeInvalidField)
		return
	}
	src, ok := cc.operationStatusSource()
	if !ok {
		cc.replyErrorCode("operation_status: no operation-status source configured", CodePolicy)
		return
	}
	out, ok := src.RemoteOperationOutcome(c.SubjectOperationID)
	if !ok {
		out = schema.OperationOutcomeView{State: schema.OutcomeUnknownOperation}
	}
	_ = cc.writeControl(Control{Op: OpOperationStatus, EndpointID: cc.endpointID, OperationID: cc.opID,
		SubjectOperationID: c.SubjectOperationID, OperationOutcome: &out})
}
