package remotegw

import (
	"encoding/json"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// OpenCommandEnvelope opens a command envelope the phone sealed under the epoch content
// key and decodes the device-signed command inside (the command-IN counterpart to the
// journal-OUT RelaySink). The gateway then forwards the command to the daemon via
// ForwardCommand; it never inspects or alters the device signature -- the daemon
// verifies it (R-POL.9). Fail-closed: a malformed/wrong-key envelope or a non-command
// plaintext returns an error and no command.
func OpenCommandEnvelope(key crypto.ContentKey, raw []byte) (protocol.DeviceCommandAuth, error) {
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		return protocol.DeviceCommandAuth{}, err
	}
	plain, err := crypto.OpenMailbox(key, env)
	if err != nil {
		return protocol.DeviceCommandAuth{}, err
	}
	var cmd protocol.DeviceCommandAuth
	if err := json.Unmarshal(plain, &cmd); err != nil {
		return protocol.DeviceCommandAuth{}, err
	}
	return cmd, nil
}

// OpenRemoteCommand opens a command envelope to the full RemoteCommand wrapper (the
// signed tuple plus, for a launch, the LaunchReq spec). It is backward-compatible
// with a bare-auth envelope (Launch is nil then). Fail-closed like OpenCommandEnvelope.
func OpenRemoteCommand(key crypto.ContentKey, raw []byte) (protocol.RemoteCommand, error) {
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		return protocol.RemoteCommand{}, err
	}
	plain, err := crypto.OpenMailbox(key, env)
	if err != nil {
		return protocol.RemoteCommand{}, err
	}
	var rc protocol.RemoteCommand
	if err := json.Unmarshal(plain, &rc); err != nil {
		return protocol.RemoteCommand{}, err
	}
	return rc, nil
}

// OpenRemoteCommandGuarded opens a command envelope like OpenRemoteCommand, but through a
// crypto.MailboxReceiver so a replayed or reordered seq for the sender/epoch is rejected
// with crypto.ErrStaleSeq instead of being opened again (mirrors the phone-side receive
// path, phonecore.JournalReceiver.Accept). Fail-closed: no command is returned on error.
func OpenRemoteCommandGuarded(recv *crypto.MailboxReceiver, key crypto.ContentKey, raw []byte) (protocol.RemoteCommand, error) {
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		return protocol.RemoteCommand{}, err
	}
	res, err := recv.Accept(key, env)
	if err != nil {
		return protocol.RemoteCommand{}, err
	}
	var rc protocol.RemoteCommand
	if err := json.Unmarshal(res.Plaintext, &rc); err != nil {
		return protocol.RemoteCommand{}, err
	}
	return rc, nil
}

// kindCommandReply tags a mailbox plaintext as a daemon reply to a phone command. The
// phone decoder (phonecore.MailboxRouter) demuxes it off the SHARED mailbox on this
// discriminator so a reply is never mistaken for a kind-less journal record (C8 / codex#7);
// it MUST match phonecore's kindCommandReply.
const kindCommandReply = "command_reply"

// replyFrame is the sealed command-reply plaintext: the daemon's protocol.Control fields
// (promoted via anonymous embedding so its frozen json tags stay the single source of
// truth) plus a kind tag. It mirrors phonecore's replyFrame exactly -- the phone unmarshals
// this shape, and the tolerant OpenControlReply ignores the extra kind key.
type replyFrame struct {
	Kind            string `json:"kind"`
	protocol.Control        // op, session_id, operation_id, ... (promoted)
}

// SealControlReply seals a daemon reply Control as a mailbox envelope under the epoch
// content key so the gateway can return it to the phone through the untrusted relay
// (the request/response counterpart of OpenCommandEnvelope). The plaintext carries an
// explicit kind:"command_reply" tag so the phone routes it to its reply cache instead of
// swallowing it into the session cache (C8). seq must be unique.
func SealControlReply(key crypto.ContentKey, epochID uint32, seq uint64, reply protocol.Control) ([]byte, error) {
	plaintext, err := json.Marshal(replyFrame{Kind: kindCommandReply, Control: reply})
	if err != nil {
		return nil, err
	}
	env, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{
		Version: crypto.VersionV1,
		EpochID: epochID,
		Seq:     seq,
	}, plaintext)
	if err != nil {
		return nil, err
	}
	return env.Marshal(), nil
}
