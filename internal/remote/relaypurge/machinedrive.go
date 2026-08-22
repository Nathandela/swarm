package relaypurge

// The MACHINE-SIDE driver: one shared implementation of the drive rulings and the
// relay dial, so every drive site (`swarm remote pair` and `swarm remote revoke`
// today; the gateway's connect-time site of bead agents-tracker-x1en when it lands)
// shares one set of rulings -- two independently assembled drive paths would diverge,
// which is the codex ruling SH2 records for the relay's serve path and it applies
// here identically.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/machineid"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/relaycfg"
)

// StorePath is the one obligation file both binaries share, beside u37c's
// push-gateway revoke obligation in the remote state dir.
func StorePath(stateDir string) string {
	return filepath.Join(stateDir, "remote", "relay-purge-obligation.json")
}

// driveOpTimeout bounds one purge dial, matching cmd/swarm's remoteRelayOpTimeout.
const driveOpTimeout = 10 * time.Second

// errDriveTransient marks a failure that happened BEFORE the relay answered the purge
// -- an unreachable relay, a local config/identity read inside the dial -- so the
// substantive-refusal resolution below can never fire on it: only an ANSWER can be
// substantive.
var errDriveTransient = errors.New("relaypurge: transient drive failure")

// DriveMachineObligations drives every deferred relay purge this machine owes
// (ADR-007 D9, SH5) and returns how many are STILL owed afterwards -- `swarm remote
// pair` refuses to proceed while that count is nonzero. logf receives one line per
// consequential ruling, printf-style.
//
// The rulings, each loud:
//
//   - LIVE PAIRING: the registry is re-read FRESH for each obligation immediately
//     before it is acted on; an obligation whose routing id belongs to a registered
//     device is retired WITHOUT purging (u37c round 3: the routing id is per-install,
//     so a re-paired handset returns on the id a stale obligation names). Routing ids
//     compare by the canonical derivation relay.RoutingID(RelayAuthPub), never the
//     self-reported record field.
//   - PAIRED MACHINE NEVER DIALS (checked AFTER the dial-free rulings: mismatch and
//     deprovisioning retire loudly regardless of pairing state, since they present
//     nothing to any relay): a non-live obligation found while ANY device is
//     registered is kept, undialed -- this machine's gateway owns the single relay
//     connection for its identity then, and a CLI dial would supersede it
//     (withMachineRelay's "the gateway is by construction not running" invariant).
//     It is driven at the machine's next zero-device relay moment.
//   - RELAY MISMATCH: an obligation owed against a different relay URL than the
//     machine now points at can never land here; claiming it did would lie, keeping
//     it would block pairing forever behind a relay this machine no longer dials. It
//     is retired LOUDLY, naming the old relay for manual cleanup there.
//   - NOT PROVISIONED: a machine with no relay identity cannot land any purge -- but
//     the obligation itself proves the machine WAS provisioned at revoke time, so the
//     OWED relay still holds the device's state; the retire is loud and names it.
//   - TRANSIENT ANSWERS (timeout, closed connection, a rate-window quota, a
//     superseded connection) keep the obligation for the next drive; a SUBSTANTIVE
//     refusal from a reachable relay RESOLVES it loudly with the reason -- an
//     obligation nothing can ever land must not brick `swarm remote pair` forever.
//
// Fail-closed on a registry or provisioning read error: THAT obligation is not
// driven (each obligation re-reads both, fresh). A purge deferred once more is
// recoverable; a live pairing destroyed is not.
func DriveMachineObligations(stateDir string, logf func(format string, args ...any)) (pendingLeft int) {
	if stateDir == "" {
		// An unresolvable state dir means "nothing provisioned here"; driving would
		// create ./remote in whatever the working directory happens to be.
		return 0
	}
	store, err := Open(StorePath(stateDir))
	if err != nil {
		logf("deferred relay purge: %v", err)
		return 1
	}
	pending, err := store.Pending()
	if err != nil {
		logf("deferred relay purge: %v", err)
		return 1
	}
	if len(pending) == 0 {
		return 0
	}
	// Provisioning is resolved into THREE states, not a boolean: a read ERROR on
	// relay.json or machine.key is transient and must fail CLOSED (keep the
	// obligation), exactly like the registry read below -- a fail-open here retired
	// security obligations on a permission blip (round-2 review, Fable defect 1).
	// Genuinely absent provisioning retires, but LOUDLY, naming the owed relay: an
	// obligation on file proves the machine WAS provisioned at revoke time, so the OLD
	// relay still holds that device's state.
	currentURL := ""
	currentMachineRID := ""
	provisionErr := error(nil)
	provisioned := false
	if cfg, found, err := relaycfg.Load(stateDir); err != nil {
		provisionErr = err
	} else if found && cfg.RelayURL != "" {
		currentURL = cfg.RelayURL
		switch _, err := os.Stat(filepath.Join(stateDir, "remote", "machine.key")); {
		case err == nil:
			provisioned = true
			if id, err := machineid.Load(filepath.Join(stateDir, "remote", "machine.key")); err != nil {
				provisionErr = err
			} else {
				currentMachineRID = string(relay.RoutingID(id.RelayAuthPublic()))
			}
		case !errors.Is(err, os.ErrNotExist):
			provisionErr = err
		}
	}
	err = Drive(context.Background(), store,
		func(_ context.Context, ob Obligation) error {
			live, others, err := registryHolds(stateDir, ob.RoutingID)
			if err != nil {
				pendingLeft++
				logf("deferred relay purge for routing id %s NOT driven: the device registry could "+
					"not be read (%v), and a purge without the live-pairing guard could sever a "+
					"re-paired handset", ob.RoutingID, err)
				return err
			}
			if live {
				logf("deferred relay purge for routing id %s retired without running: a device on "+
					"that routing id is paired again, and the purge would sever the live pairing",
					ob.RoutingID)
				return nil
			}
			if provisionErr != nil {
				pendingLeft++
				logf("deferred relay purge for routing id %s NOT driven: this machine's relay "+
					"provisioning could not be read (%v); kept for the next drive", ob.RoutingID,
					provisionErr)
				return provisionErr
			}
			if !provisioned {
				logf("deferred relay purge for routing id %s retired WITHOUT landing: this machine "+
					"is no longer relay-provisioned, but the obligation is owed against %s -- that "+
					"relay still holds the device's mailbox, push wake and route; clean it up there "+
					"by hand", ob.RoutingID, ob.RelayURL)
				return nil
			}
			if ob.MachineRID != "" && currentMachineRID != "" && ob.MachineRID != currentMachineRID {
				logf("deferred relay purge for routing id %s retired WITHOUT landing: it was recorded "+
					"under a previous machine identity (%s; this machine now authenticates as %s), and "+
					"only that identity could present it. The relay still holds the old pairing's "+
					"mailbox, push wake and route; clean it up at %s by hand", ob.RoutingID,
					ob.MachineRID, currentMachineRID, ob.RelayURL)
				return nil
			}
			if ob.RelayURL != "" && ob.RelayURL != currentURL {
				logf("deferred relay purge for routing id %s retired WITHOUT landing: it is owed "+
					"against %s and this machine now points at %s. The old relay still holds that "+
					"device's mailbox, push wake and route; clean it up at that relay by hand",
					ob.RoutingID, ob.RelayURL, currentURL)
				return nil
			}
			if others {
				pendingLeft++
				logf("deferred relay purge for routing id %s not driven while a device is paired: "+
					"the gateway owns this machine's relay connection and a second dial would "+
					"supersede it; it is driven at the next zero-device relay moment", ob.RoutingID)
				return errors.New("a device is paired; dial deferred")
			}
			switch err := purgeAtRelay(stateDir, currentURL, ob.RoutingID); {
			case err == nil:
				logf("deferred relay purge landed for routing id %s", ob.RoutingID)
				return nil
			case errors.Is(err, relay.ErrQuotaExceeded), errors.Is(err, relay.ErrDuplicateConnection):
				// Answers that clear by themselves: kept for the next drive.
				pendingLeft++
				return err
			case errors.Is(err, relay.ErrNotAuthorized):
				// The relay holds no pairing with that routing id UNDER THE DIALING
				// IDENTITY. That settles the obligation only when the dialing identity
				// is provably the one that owed it (post-commit codex #1): an
				// obligation recorded without an identity binding (an unreadable
				// machine.key at record time) could belong to a previous identity
				// whose pairing survives, so it retires LOUDLY as unverifiable
				// instead of silently as settled.
				if ob.MachineRID != "" && ob.MachineRID == currentMachineRID {
					logf("deferred relay purge for routing id %s settled: the relay holds no pairing "+
						"for it", ob.RoutingID)
					return nil
				}
				logf("deferred relay purge for routing id %s retired UNVERIFIED: the relay holds no "+
					"pairing for it under this machine's current identity, but the obligation carries "+
					"no identity binding -- if the machine identity changed since the revoke, the old "+
					"pairing may survive at %s; verify there by hand", ob.RoutingID, ob.RelayURL)
				return nil
			case errors.Is(err, relay.ErrRelayAnswered):
				// A SUBSTANTIVE refusal: the relay is reachable and answering no, and
				// nothing this machine re-presents changes that answer -- re-presenting
				// a dead request on every drive forever is a wedge, not durability
				// (u37c's own Refusal resolution; round-2 Opus R2-1 reproduced the
				// pair lockout the wedge causes, and round-2 codex #3 demanded the
				// unlanded purge stay on the record -- the RESOLVED TOMBSTONE serves
				// both: excluded from Pending, reason preserved in the store). The
				// relay-side state survives and is named.
				if rerr := store.Resolve(ob.RoutingID, err.Error()); rerr != nil {
					pendingLeft++
					logf("deferred relay purge for routing id %s: refusal could not be recorded "+
						"(%v); kept", ob.RoutingID, rerr)
					return rerr
				}
				logf("deferred relay purge for routing id %s RESOLVED WITHOUT landing -- the relay "+
					"refused it (%v). Nothing will re-present it; the relay still holds that "+
					"device's mailbox, push wake and route, and cleaning it up there is now a "+
					"manual task", ob.RoutingID, err)
				return nil
			default:
				// Everything else -- errDriveTransient's pre-answer failures, a
				// timeout, a dead connection, a reply that would not DECODE (a
				// truncated or oversized frame, a version-skewed relay) -- is NOT an
				// answer, and only an answer may tombstone (round-3 review F1:
				// substantive is an ALLOWLIST anchored on relay.ErrRelayAnswered,
				// never a fallthrough).
				pendingLeft++
				return err
			}
		})
	if err != nil {
		logf("deferred relay purge still pending: %v", err)
	}
	// The returned count is a FRESH read, not the loop's counter: an obligation a
	// concurrent revoke recorded mid-drive must gate `swarm remote pair` too (round-2
	// codex #4). The loop counter stands in only if the re-read itself fails.
	if fresh, ferr := store.Pending(); ferr == nil {
		return len(fresh)
	}
	if pendingLeft == 0 {
		pendingLeft = 1
	}
	return pendingLeft
}

// registryHolds reports whether routingID belongs to a registered device (live), and
// whether any OTHER device is registered (others) -- both from a fresh read.
func registryHolds(stateDir, routingID string) (live, others bool, err error) {
	reg, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		return false, false, err
	}
	for _, rec := range reg.List() {
		if len(rec.RelayAuthPub) != 0 && string(relay.RoutingID(rec.RelayAuthPub)) == routingID {
			live = true
		} else {
			others = true
		}
	}
	return live, others, nil
}

// purgeAtRelay is one short-lived, authenticated machine dial presenting one
// device_revoke -- the same identity, transport policy and no-Peer decision as
// cmd/swarm's withMachineRelay (ADR-007 B34/B49), rebuilt here so the gateway can
// drive without importing the CLI.
func purgeAtRelay(stateDir, relayURL, routingID string) error {
	cfg, found, err := relaycfg.Load(stateDir)
	if err != nil {
		return fmt.Errorf("%w: %v", errDriveTransient, err)
	}
	if !found {
		return fmt.Errorf("%w: relay config vanished mid-drive", errDriveTransient)
	}
	sec, err := cfg.Security()
	if err != nil {
		return fmt.Errorf("%w: %v", errDriveTransient, err)
	}
	id, err := machineid.Load(filepath.Join(stateDir, "remote", "machine.key"))
	if err != nil {
		return fmt.Errorf("%w: %v", errDriveTransient, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), driveOpTimeout)
	defer cancel()
	cl, err := relay.DialSecure(ctx, relayURL, relay.ClientAuth{
		RelayAuthPub: id.RelayAuthPublic(),
		Sign:         func(challenge []byte) ([]byte, error) { return id.RelayAuthSign(challenge), nil },
	}, sec)
	if err != nil {
		// The relay never answered: unreachable is the CANONICAL transient.
		return fmt.Errorf("%w: %v", errDriveTransient, err)
	}
	defer func() { _ = cl.Close() }()
	if err := cl.DeviceRevoke(ctx, routingID); err != nil {
		return fmt.Errorf("device_revoke %s: %w", routingID, err)
	}
	return nil
}
