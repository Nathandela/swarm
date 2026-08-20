package phonecore

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
)

// CommandInput is the identity of a remote mutating op the phone authors.
type CommandInput struct {
	Action      string    // a schema.Action* constant
	Machine     string    // target machine endpoint id
	Session     string    // namespaced session id (schema.LaunchSessionSentinel for launch)
	OperationID string    // durable client-generated idempotency key (R-PHC.4)
	ExpiresAt   time.Time // command validity horizon
	ContentHash []byte    // optional 32-byte content binding (e.g. protocol.LaunchContentHash)
}

// SignCommand authors and signs a remote command with the device's command-signing key
// (R-PHC authoring side of R-POL.9). It builds the canonical crypto.Command tuple,
// signs it with the KeyStore, and returns the schema.DeviceCommandAuth the phone
// sends to the gateway -> daemon. The DeviceID is derived canonically from the
// command-signing public key, matching how the daemon registry pins it (R-DEV.1), so a
// signature always verifies against exactly the record its id names.
func SignCommand(ks crypto.KeyStore, in CommandInput) (schema.DeviceCommandAuth, error) {
	msg, err := crypto.Command{
		Action:      in.Action,
		Machine:     in.Machine,
		Session:     in.Session,
		OperationID: in.OperationID,
		ExpiresAt:   in.ExpiresAt.Unix(),
		ContentHash: in.ContentHash,
	}.Canonical()
	if err != nil {
		return schema.DeviceCommandAuth{}, err
	}
	// A custody refusal is returned, never swallowed: a DeviceCommandAuth carrying the
	// base64 of nothing is structurally well-formed, refused by the daemon, and
	// indistinguishable at the call site from a network problem (ADR-007 B14).
	sig, err := ks.SignCommand(msg)
	if err != nil {
		return schema.DeviceCommandAuth{}, err
	}
	return schema.DeviceCommandAuth{
		DeviceID:    device.DeviceIDFor(ks.CommandSigningPublic()),
		Action:      in.Action,
		Machine:     in.Machine,
		Session:     in.Session,
		OperationID: in.OperationID,
		ExpiresAt:   in.ExpiresAt,
		ContentHash: in.ContentHash,
		Sig:         base64.StdEncoding.EncodeToString(sig),
	}, nil
}

// TakeControlInput is the identity of a take_control op the phone authors. GateToken is
// hashed into the signed command (ContentHash = SHA256(GateToken)) AND carried on the
// wire, so the daemon can recompute the hash from the wire token and a relay that swaps
// the one-shot token breaks the signature.
type TakeControlInput struct {
	Machine     string    // target machine endpoint id
	Session     string    // namespaced session id to take control of
	OperationID string    // durable client-generated idempotency key (single-use)
	ExpiresAt   time.Time // command validity horizon
	GateToken   string    // one-shot gate token bound via ContentHash and carried on the wire
}

// SignTakeControl authors and signs a take_control command (A7 input), mirroring
// SignCommand but binding the one-shot gate token into the signature the way launch binds
// its spec: ContentHash = SHA256(GateToken), Action = schema.ActionTakeControl. The
// daemon (handleTakeControl) recomputes SHA256 from the WIRE gate token, so a relay that
// swaps the token yields a different content hash and the device signature fails to verify.
func SignTakeControl(ks crypto.KeyStore, in TakeControlInput) (schema.DeviceCommandAuth, error) {
	h := sha256.Sum256([]byte(in.GateToken))
	return SignCommand(ks, CommandInput{
		Action:      schema.ActionTakeControl,
		Machine:     in.Machine,
		Session:     in.Session,
		OperationID: in.OperationID,
		ExpiresAt:   in.ExpiresAt,
		ContentHash: h[:],
	})
}

// ApproveInput is the identity of an approve op the phone authors (IS-LIFE-4). ContentHash is
// the approval_request's OWN `content_hash`, as text, exactly as the card carried it.
type ApproveInput struct {
	Machine     string    // target machine endpoint id
	Session     string    // namespaced session id the approval belongs to
	OperationID string    // durable client-generated idempotency key; NEVER the interaction id (IS-APR-1)
	ExpiresAt   time.Time // command validity horizon (the COMMAND's, not the approval's)
	ContentHash string    // the item's content_hash, echoed verbatim (IS-APR-2)
}

// SignApprove authors and signs an approve command (IS-LIFE-4), mirroring SignTakeControl but
// binding the INTERACTION CONTENT rather than a token: ADR-007 D7 spends the signed tuple's
// one content slot on it, and the daemon derives the same slot by decoding the WIRE body's
// content_hash -- so a gateway that swaps the hash to redirect the approval breaks the
// signature rather than reaching the machine as a well-formed approve for another card.
//
// The hash is DECODED, never derived. IS-APR-2 makes the phone echo `content_hash` verbatim,
// and a value it cannot decode is refused here: SHA256("") is a valid 32-byte digest, so
// falling back to an empty hash would produce a structurally-perfect command bound to nothing
// -- refused at the machine as a stale card, which reports a phone-side bug as the user's
// problem. This is handleTakeControl's empty-gate-token rule for the other content slot.
func SignApprove(ks crypto.KeyStore, in ApproveInput) (schema.DeviceCommandAuth, error) {
	h, err := hex.DecodeString(in.ContentHash)
	if err != nil || len(h) != sha256.Size {
		return schema.DeviceCommandAuth{}, fmt.Errorf(
			"phonecore: approval content_hash %q is not a 32-byte hex digest; a phone echoes it verbatim and computes none of its own (IS-APR-2)",
			in.ContentHash)
	}
	return SignCommand(ks, CommandInput{
		Action:      schema.ActionApprove,
		Machine:     in.Machine,
		Session:     in.Session,
		OperationID: in.OperationID,
		ExpiresAt:   in.ExpiresAt,
		ContentHash: h,
	})
}

// ComposerSendInput is the identity of a composer_send op the phone authors (Wave R6,
// Mirror M2.4, IS-LIFE-5). ExpectedTurn and Text are the body's own halves beside the
// session: all three are bound into the signature via ComposerSendContentHash.
type ComposerSendInput struct {
	Machine      string    // target machine endpoint id
	Session      string    // namespaced session id the send targets
	OperationID  string    // durable client-generated idempotency key
	ExpiresAt    time.Time // command validity horizon
	ExpectedTurn string    // the turn the phone rendered the send against ("" = idle)
	Text         string    // the message
}

// SignComposerSend authors and signs a composer_send command, mirroring SignApprove: the
// signed tuple's content slot is schema.ComposerSendContentHash over the SAME
// (session, expected_turn, text) body SealComposerSendEnvelope carries, so a gateway that
// alters the text or re-points expected_turn breaks the signature rather than reaching the
// daemon as a well-formed send of something else. Re-deriving the hash at a call site is
// the same forbidden duplication mobile/commands.go records for the take_control token.
func SignComposerSend(ks crypto.KeyStore, in ComposerSendInput) (schema.DeviceCommandAuth, error) {
	return SignCommand(ks, CommandInput{
		Action:      schema.ActionComposerSend,
		Machine:     in.Machine,
		Session:     in.Session,
		OperationID: in.OperationID,
		ExpiresAt:   in.ExpiresAt,
		ContentHash: schema.ComposerSendContentHash(&schema.ComposerSendReq{
			Session: in.Session, ExpectedTurn: in.ExpectedTurn, Text: in.Text,
		}),
	})
}

// SealComposerSendEnvelope seals the SIGNED composer_send command together with its body
// (Wave R6), mirroring SealSessionLaunchEnvelope: the body rides beside the signed tuple so
// the gateway can forward it, the command's ContentHash must be
// schema.ComposerSendContentHash(body) which the daemon recomputes from the forwarded body,
// and BodyVersion is bound to the one profile version this phone read
// (schema.CurrentProfileVersion) -- the daemon refuses any other, absent included. seq must
// be unique per epoch.
func SealComposerSendEnvelope(key crypto.ContentKey, epochID uint32, seq uint64, cmd schema.DeviceCommandAuth, body *schema.ComposerSendReq) ([]byte, error) {
	return sealPhoneFrame(key, epochID, seq, commandFrame{
		Kind: kindPhoneToMachine,
		RemoteCommand: schema.RemoteCommand{
			DeviceCommandAuth: cmd,
			ComposerSend:      body,
			BodyVersion:       schema.CurrentProfileVersion,
		},
	})
}

// TurnInterruptInput is the identity of a turn_interrupt op the phone authors (Wave R6,
// Mirror M2.4). ExpectedTurn is the turn the SCREEN drew the Stop button against; both it
// and the session are bound into the signature via TurnInterruptContentHash.
type TurnInterruptInput struct {
	Machine      string    // target machine endpoint id
	Session      string    // namespaced session id the interrupt targets
	OperationID  string    // durable client-generated idempotency key
	ExpiresAt    time.Time // command validity horizon
	ExpectedTurn string    // the turn the phone rendered the Stop against; never empty
}

// SignTurnInterrupt authors and signs a turn_interrupt command, mirroring SignComposerSend
// exactly -- which is the whole point of the Wave R6 fix-pack's finding B7. The two ops
// answer the SAME race (a tap lands later than it was rendered) and now carry the same
// precondition under the same binding, so a gateway that re-points expected_turn breaks the
// signature rather than reaching the daemon as a well-formed Stop of a different turn.
// Re-deriving the hash at a call site is the forbidden duplication SignComposerSend records.
func SignTurnInterrupt(ks crypto.KeyStore, in TurnInterruptInput) (schema.DeviceCommandAuth, error) {
	return SignCommand(ks, CommandInput{
		Action:      schema.ActionTurnInterrupt,
		Machine:     in.Machine,
		Session:     in.Session,
		OperationID: in.OperationID,
		ExpiresAt:   in.ExpiresAt,
		ContentHash: schema.TurnInterruptContentHash(&schema.TurnInterruptReq{
			Session: in.Session, ExpectedTurn: in.ExpectedTurn,
		}),
	})
}

// SealTurnInterruptEnvelope seals the SIGNED turn_interrupt command together with its body
// (Wave R6), mirroring SealComposerSendEnvelope. The body rides beside the signed tuple so
// the gateway can forward it, the command's ContentHash must be
// schema.TurnInterruptContentHash(body) which the daemon recomputes from the forwarded body,
// and BodyVersion is bound to the one profile version this phone read. seq must be unique
// per epoch.
//
// THE OP WAS BODYLESS UNTIL THE R6 FIX-PACK; see schema.TurnInterruptReq for what that cost.
func SealTurnInterruptEnvelope(key crypto.ContentKey, epochID uint32, seq uint64, cmd schema.DeviceCommandAuth, body *schema.TurnInterruptReq) ([]byte, error) {
	return sealPhoneFrame(key, epochID, seq, commandFrame{
		Kind: kindPhoneToMachine,
		RemoteCommand: schema.RemoteCommand{
			DeviceCommandAuth: cmd,
			TurnInterrupt:     body,
			BodyVersion:       schema.CurrentProfileVersion,
		},
	})
}

// SealInteractionHistoryEnvelope seals the UNSIGNED interaction_history read (Mirror M3.1,
// ADR-014) with its paging body. UNSIGNED is journal_resync's decision verbatim (see
// schema.ActionInteractionHistory): the tuple carries no device signature, sealing under the
// epoch content key is already proof the asker is the paired device, and the gates that
// matter are the daemon's own -- the negotiated `journal` capability and the kill switch.
//
// It carries no BodyVersion for the same reason journal_resync does not: body_version binds
// a durable semantic MUTATION to a profile (ADR-017 T5), and a read mutates nothing.
// seq must be unique per epoch.
func SealInteractionHistoryEnvelope(key crypto.ContentKey, epochID uint32, seq uint64, cmd schema.DeviceCommandAuth, body *schema.InteractionHistoryReq) ([]byte, error) {
	return sealPhoneFrame(key, epochID, seq, commandFrame{
		Kind: kindPhoneToMachine,
		RemoteCommand: schema.RemoteCommand{
			DeviceCommandAuth: cmd,
			History:           body,
		},
	})
}

// SealInteractionDetailEnvelope seals the UNSIGNED interaction_detail read (Mirror M3.3,
// IS-CAP-2), SealInteractionHistoryEnvelope's sibling on every count.
func SealInteractionDetailEnvelope(key crypto.ContentKey, epochID uint32, seq uint64, cmd schema.DeviceCommandAuth, body *schema.InteractionDetailReq) ([]byte, error) {
	return sealPhoneFrame(key, epochID, seq, commandFrame{
		Kind: kindPhoneToMachine,
		RemoteCommand: schema.RemoteCommand{
			DeviceCommandAuth: cmd,
			Detail:            body,
		},
	})
}

// SealApproveEnvelope seals the SIGNED approve command together with its ApproveReq body
// (IS-LIFE-4), mirroring SealLaunchEnvelope: the body rides beside the signed tuple so the
// gateway can reconstruct the approve Control the daemon validates against. seq must be
// unique per epoch.
//
// Only content_hash is bound by the signature. The rest of the body needs no binding it does
// not already have: agent_instance, interaction_id and expires_at are checked against the
// daemon's OWN stored tuple, so altering one yields CodeStaleApproval rather than a misapplied
// decision, and the decision id is deliberately unsigned (IS-LIFE-4) -- it rides inside the
// epoch-sealed frame, unforgeable by the relay and alterable only by the gateway, which is the
// documented D4/D5 owner-uid residual.
func SealApproveEnvelope(key crypto.ContentKey, epochID uint32, seq uint64, cmd schema.DeviceCommandAuth, approve schema.ApproveReq) ([]byte, error) {
	return sealPhoneFrame(key, epochID, seq, commandFrame{
		Kind:          kindPhoneToMachine,
		RemoteCommand: schema.RemoteCommand{DeviceCommandAuth: cmd, Approve: &approve},
	})
}

// SealTakeControlEnvelope seals a signed take_control command together with its wire gate
// token and requested TTL as a mailbox envelope under the epoch content key, mirroring
// SealLaunchEnvelope. The gate token rides alongside the signed tuple (schema.RemoteCommand)
// so the gateway can reconstruct the take_control Control frame; the token is bound into the
// signature via ContentHash = SHA256(gateToken), which the daemon recomputes from this
// forwarded token, so a relay that alters it breaks the signature. TTLSeconds is not signed
// (server-clamped). seq must be unique per epoch.
func SealTakeControlEnvelope(key crypto.ContentKey, epochID uint32, seq uint64, cmd schema.DeviceCommandAuth, gateToken string, ttlSeconds int) ([]byte, error) {
	return sealPhoneFrame(key, epochID, seq, commandFrame{
		Kind: kindPhoneToMachine,
		RemoteCommand: schema.RemoteCommand{
			DeviceCommandAuth: cmd,
			GateToken:         gateToken,
			TTLSeconds:        ttlSeconds,
		},
	})
}

// SealResyncEnvelope seals the journal_resync read command together with the phone's own
// cache cursor, mirroring SealTakeControlEnvelope. The command is UNSIGNED (PB-SYNC-5's
// decision: the gateway serves it and holds no device key), and the cursor is not part of
// any signed tuple -- it does not need to be, since the frame is sealed under the epoch
// content key the relay cannot forge, and a wrong cursor only changes how many events the
// reseed carries. seq must be unique per epoch.
func SealResyncEnvelope(key crypto.ContentKey, epochID uint32, seq uint64, cmd schema.DeviceCommandAuth, from uint64) ([]byte, error) {
	return sealPhoneFrame(key, epochID, seq, commandFrame{
		Kind:          kindPhoneToMachine,
		RemoteCommand: schema.RemoteCommand{DeviceCommandAuth: cmd, ResyncCursor: from},
	})
}

// SealPushPrefsEnvelope seals the SIGNED push_prefs command together with its preference
// body (PB-PUSH-8, PB-APP-7). seq must be unique per epoch.
//
// The body is deliberately NOT bound by ContentHash the way a launch spec is, and
// schema.RemoteCommand.PushPrefs records why: a launch spec is forwarded through the gateway
// in cleartext, so the hash is what stops the gateway altering it, whereas a preference body
// never leaves the gateway -- it arrives sealed under the epoch content key the relay cannot
// forge, and the gateway is itself the custodian that decides delivery. The SIGNATURE is this
// verb's gate; the daemon's capability switch cannot refuse it.
func SealPushPrefsEnvelope(key crypto.ContentKey, epochID uint32, seq uint64, cmd schema.DeviceCommandAuth, prefs schema.PushPrefs) ([]byte, error) {
	return sealPhoneFrame(key, epochID, seq, commandFrame{
		Kind:          kindPhoneToMachine,
		RemoteCommand: schema.RemoteCommand{DeviceCommandAuth: cmd, PushPrefs: &prefs},
	})
}

// SealCommandEnvelope seals a signed command as a mailbox envelope under the epoch
// content key (XChaCha20-Poly1305), so it can travel through the untrusted relay to the
// machine as ciphertext. The command's device signature is verified by the daemon after
// the gateway opens the envelope; sealing adds confidentiality, not the command's
// authenticity (which the signature already carries). seq must be unique per epoch.
func SealCommandEnvelope(key crypto.ContentKey, epochID uint32, seq uint64, cmd schema.DeviceCommandAuth) ([]byte, error) {
	return sealPhoneFrame(key, epochID, seq, commandFrame{
		Kind:          kindPhoneToMachine,
		RemoteCommand: schema.RemoteCommand{DeviceCommandAuth: cmd},
	})
}

// SealLaunchEnvelope seals a signed launch command together with its LaunchReq spec
// as a mailbox envelope under the epoch content key. The spec rides alongside the
// signed tuple (schema.RemoteCommand) so the gateway can forward it to the daemon;
// the command's ContentHash must be crypto/protocol.LaunchContentHash(launch), which
// the daemon recomputes from the forwarded spec, so a relay or gateway that alters
// the spec breaks the signature. seq must be unique per epoch.
func SealLaunchEnvelope(key crypto.ContentKey, epochID uint32, seq uint64, cmd schema.DeviceCommandAuth, launch *schema.LaunchReq) ([]byte, error) {
	return sealPhoneFrame(key, epochID, seq, commandFrame{
		Kind:          kindPhoneToMachine,
		RemoteCommand: schema.RemoteCommand{DeviceCommandAuth: cmd, Launch: launch},
	})
}

// SealSessionLaunchEnvelope seals a signed session_launch command together with its
// preset-selection body (Wave R5), mirroring SealLaunchEnvelope: the body rides beside
// the signed tuple so the gateway can forward it, and the command's ContentHash must be
// schema.SessionLaunchContentHash(req), which the daemon recomputes from the forwarded
// body -- a relay or gateway that alters the preset id, revision or prompt breaks the
// signature. BodyVersion is bound to the one profile version this phone read
// (schema.CurrentProfileVersion): the daemon refuses any other, absent included. seq
// must be unique per epoch.
func SealSessionLaunchEnvelope(key crypto.ContentKey, epochID uint32, seq uint64, cmd schema.DeviceCommandAuth, req *schema.SessionLaunchReq) ([]byte, error) {
	return sealPhoneFrame(key, epochID, seq, commandFrame{
		Kind: kindPhoneToMachine,
		RemoteCommand: schema.RemoteCommand{
			DeviceCommandAuth: cmd,
			SessionLaunch:     req,
			BodyVersion:       schema.CurrentProfileVersion,
		},
	})
}

// SealLaunchPresetsEnvelope seals the signed launch_presets read (Wave R5): the
// machine-authored preset list request. Signed (unlike terminal_watch/journal_resync)
// because the custody lives daemon-side behind the one authorization plane; carries no
// body of its own, only the R1 body-version binding. seq must be unique per epoch.
func SealLaunchPresetsEnvelope(key crypto.ContentKey, epochID uint32, seq uint64, cmd schema.DeviceCommandAuth) ([]byte, error) {
	return sealPhoneFrame(key, epochID, seq, commandFrame{
		Kind: kindPhoneToMachine,
		RemoteCommand: schema.RemoteCommand{
			DeviceCommandAuth: cmd,
			BodyVersion:       schema.CurrentProfileVersion,
		},
	})
}

// OpenControlReply opens a daemon reply Control the gateway sealed and returned via the
// phone's mailbox (the response half of the command round-trip). Fail-closed on a
// malformed/wrong-key envelope or non-Control plaintext.
func OpenControlReply(key crypto.ContentKey, raw []byte) (schema.Control, error) {
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		return schema.Control{}, err
	}
	plain, err := crypto.OpenMailbox(key, env)
	if err != nil {
		return schema.Control{}, err
	}
	var ctrl schema.Control
	if err := json.Unmarshal(plain, &ctrl); err != nil {
		return schema.Control{}, err
	}
	return ctrl, nil
}
