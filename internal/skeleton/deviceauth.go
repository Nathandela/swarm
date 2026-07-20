package skeleton

import (
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
)

// actionClass maps a canonical action string (protocol.Action*) to its R-POL.6
// capability class. An unknown action is fail-closed (ok=false): a command for an
// action the daemon does not recognise is refused, never silently treated as a read.
func actionClass(action string) (device.Action, bool) {
	switch action {
	case protocol.ActionLaunch, protocol.ActionKill, protocol.ActionDelete:
		return device.ActionControl, true
	case protocol.ActionApprove:
		return device.ActionApprove, true
	default:
		return 0, false
	}
}

// authorizeCommand is the R-POL.9 authorization decision. It returns nil ONLY when the
// device is registered, its command signature verifies over the canonical tuple against
// its pinned key, the command has not expired, and its capability permits the action.
// It authenticates (verify signature) BEFORE it authorizes (capability), and every
// failure -- nil registry, unknown action, unknown device, malformed/forged signature,
// expiry, insufficient capability -- is fail-closed. Capability is read from the
// registry, never from the wire, so a compromised gateway cannot escalate a device by
// editing a capability field: there is none to edit.
func authorizeCommand(reg *device.Registry, now time.Time, cmd protocol.DeviceCommandAuth) error {
	if reg == nil {
		return errors.New("device authorization unavailable")
	}
	class, ok := actionClass(cmd.Action)
	if !ok {
		return fmt.Errorf("unknown action %q", cmd.Action)
	}
	rec, ok := reg.Get(cmd.DeviceID)
	if !ok {
		return fmt.Errorf("unknown device %q", cmd.DeviceID)
	}

	// Authenticate: the signature must verify over the exact canonical tuple against
	// the device's pinned command-signing key. Because the tuple binds operation_id and
	// expires_at, a captured signature cannot be replayed under a new operation_id or a
	// pushed-out expiry.
	sig, err := base64.StdEncoding.DecodeString(cmd.Sig)
	if err != nil {
		return fmt.Errorf("device signature not decodable: %w", err)
	}
	msg, err := crypto.Command{
		Action:      cmd.Action,
		Machine:     cmd.Machine,
		Session:     cmd.Session,
		OperationID: cmd.OperationID,
		ExpiresAt:   cmd.ExpiresAt.Unix(),
		ContentHash: cmd.ContentHash,
	}.Canonical()
	if err != nil {
		return fmt.Errorf("malformed command: %w", err)
	}
	if err := crypto.VerifyCommandSig(rec.CommandSignPub, msg, sig); err != nil {
		return err
	}

	// The command is authentic; is it still valid in time, and does this device's
	// pinned capability permit the action?
	if now.After(cmd.ExpiresAt) {
		return errors.New("command expired")
	}
	if !rec.Capability.Allows(class) {
		return fmt.Errorf("device %q capability does not permit %q", cmd.DeviceID, cmd.Action)
	}
	return nil
}
