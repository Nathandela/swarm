package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/machineid"
)

const remoteUsage = `usage: swarm remote <command>

  swarm remote init      provision this machine's pairing identity
  swarm remote devices   list paired devices
  swarm remote revoke    revoke a paired device
  swarm remote pair      pair a new device
  swarm remote off       disable remote control
  swarm remote on        enable remote control
  swarm remote status    show remote control status
`

// runRemote is the `swarm remote` role: it dispatches to a remote-control verb.
// With no verb it prints usage (nonzero exit); an unrecognized verb is an error
// (nonzero exit). `init`, `devices`, `revoke`, `pair`, the `off`/`on` manual kill
// switch, and the `status` read are wired.
func runRemote(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, remoteUsage)
		return 2
	}
	switch args[0] {
	case "init":
		return runRemoteInit(args[1:], stdout, stderr)
	case "devices":
		return runRemoteDevices(args[1:], stdout, stderr)
	case "revoke":
		return runRemoteRevoke(args[1:], stdout, stderr)
	case "pair":
		return runRemotePair(args[1:], os.Stdin, stdout, stderr)
	case "off":
		return runRemoteSetControl(false, stdout, stderr)
	case "on":
		return runRemoteSetControl(true, stdout, stderr)
	case "status":
		return runRemoteStatus(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "remote: unknown remote command %q\n", args[0])
		return 2
	}
}

// remoteIdentityFile is the machine identity `runRemoteInit` persists, at
// <stateDir>/remote/machine.key. internal/skeleton.loadPairingConfig reads it
// back from the same path (see internal/skeleton/pairing_config.go) — the CLI
// and the daemon assembly must agree on it.
const remoteIdentityFile = "machine.key"

// remoteRelayFile mirrors remoteRelayFile in internal/skeleton/pairing_config.go:
// <stateDir>/remote/relay.json is the exact path loadRelayURL reads, and the path
// `swarm remote init --relay-url` must agree on.
const remoteRelayFile = "relay.json"

// remoteStateFile mirrors remoteStateFile in internal/skeleton/killswitch.go: the
// durable kill-switch file at <stateDir>/remote-state.json (directly under the state
// dir, NOT the remote/ subdir) that `swarm remote off`/`on` write and `swarm remote
// status` reads back for the manual override.
const remoteStateFile = "remote-state.json"

// runRemoteInit is the `swarm remote init` verb (machine key custody, A4-1b). It
// resolves the state dir the same way dialClient does (SWARM_DAEMON_STATE env,
// falling back to persist.DefaultDir), then either loads the existing machine
// identity at <stateDir>/remote/machine.key (IDEMPOTENT: a second run never
// rotates keys) or generates and saves a fresh one at 0600. It prints only the
// identity's redacted, public fingerprint (identity.String()) to stdout — never
// any private material. An optional --relay-url flag, when non-empty, is
// persisted to <stateDir>/remote/relay.json ({"relay_url":"..."}, 0600) — the
// exact shape internal/skeleton.loadRelayURL reads back. Without the flag,
// relay.json is left untouched (absent), so remote pairing stays unconfigured.
func runRemoteInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("remote init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	relayURL := fs.String("relay-url", "", "relay server URL for remote pairing")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	stateDir := os.Getenv(daemon.EnvStateDir)
	if stateDir == "" {
		var err error
		if stateDir, err = persist.DefaultDir(); err != nil {
			fmt.Fprintf(stderr, "remote init: %v\n", err)
			return 1
		}
	}

	remoteDir := filepath.Join(stateDir, "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		fmt.Fprintf(stderr, "remote init: %v\n", err)
		return 1
	}
	path := filepath.Join(remoteDir, remoteIdentityFile)

	var id *machineid.Identity
	if _, err := os.Stat(path); err == nil {
		// Identity already provisioned: load it rather than rotating (idempotent).
		id, err = machineid.Load(path)
		if err != nil {
			fmt.Fprintf(stderr, "remote init: %v\n", err)
			return 1
		}
	} else if os.IsNotExist(err) {
		hostname, hErr := os.Hostname()
		if hErr != nil {
			hostname = "unknown"
		}
		id, err = machineid.Generate(hostname)
		if err != nil {
			fmt.Fprintf(stderr, "remote init: %v\n", err)
			return 1
		}
		if err := id.Save(path); err != nil {
			fmt.Fprintf(stderr, "remote init: %v\n", err)
			return 1
		}
	} else {
		fmt.Fprintf(stderr, "remote init: %v\n", err)
		return 1
	}

	if *relayURL != "" {
		relayPath := filepath.Join(remoteDir, remoteRelayFile)
		b, err := json.Marshal(map[string]string{"relay_url": *relayURL})
		if err != nil {
			fmt.Fprintf(stderr, "remote init: %v\n", err)
			return 1
		}
		if err := os.WriteFile(relayPath, b, 0o600); err != nil {
			fmt.Fprintf(stderr, "remote init: %v\n", err)
			return 1
		}
	}

	fmt.Fprintln(stdout, id.String())
	return 0
}

// remoteRevokeUsage is `swarm remote revoke`'s usage message, printed to stderr
// (and matched by TestRemoteRevoke_RequiresOneArg's "usage" substring check) when
// the device-id arg is missing or extra args are given.
const remoteRevokeUsage = `usage: swarm remote revoke <device-id>
`

// runRemoteDevices is the `swarm remote devices` verb: it dials the daemon
// (requesting the CapPairing capability device_list needs), lists paired devices,
// and prints them as a table (device id, name, capability, paired-at) to stdout. An
// empty registry prints just the header, exit 0.
func runRemoteDevices(_ []string, stdout, stderr io.Writer) int {
	client, err := dialClient([]string{protocol.CapPairing})
	if err != nil {
		fmt.Fprintf(stderr, "remote devices: %v\n", err)
		return 1
	}
	defer client.Close()

	devices, err := client.ListDevices()
	if err != nil {
		fmt.Fprintf(stderr, "remote devices: %v\n", err)
		return 1
	}

	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "DEVICE ID\tNAME\tCAPABILITY\tPAIRED AT")
	for _, d := range devices {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", d.DeviceID, d.Name, d.Capability, d.PairedAt.Format(timeFormat))
	}
	tw.Flush()
	return 0
}

// timeFormat is the timestamp layout `swarm remote devices` prints PairedAt in.
const timeFormat = "2006-01-02 15:04:05"

// runRemoteSetControl is the `swarm remote off` (enabled=false) / `swarm remote on`
// (enabled=true) verb: the durable manual kill switch. It dials the owner daemon
// (CapPairing, like runRemoteDevices), durably flips the remote-control master override
// via the owner-tier remote_set_control op, and prints a confirmation. `off` severs remote
// control at the daemon choke point regardless of paired devices; `on` returns to the
// device-derived value.
func runRemoteSetControl(enabled bool, stdout, stderr io.Writer) int {
	verb := "off"
	if enabled {
		verb = "on"
	}
	client, err := dialClient([]string{protocol.CapPairing})
	if err != nil {
		fmt.Fprintf(stderr, "remote %s: %v\n", verb, err)
		return 1
	}
	defer client.Close()

	if err := client.SetRemoteControl(enabled); err != nil {
		fmt.Fprintf(stderr, "remote %s: %v\n", verb, err)
		return 1
	}
	if enabled {
		fmt.Fprintln(stdout, "remote control enabled")
	} else {
		fmt.Fprintln(stdout, "remote control disabled")
	}
	return 0
}

// runRemoteRevoke is the `swarm remote revoke <device-id>` verb: it requires
// exactly one positional arg (the device id) and refuses with a usage error
// (nonzero exit, no dial attempt) otherwise. With exactly one arg it dials the
// daemon, revokes the device, and prints a confirmation on success.
func runRemoteRevoke(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprint(stderr, remoteRevokeUsage)
		return 2
	}
	deviceID := args[0]

	client, err := dialClient([]string{protocol.CapPairing})
	if err != nil {
		fmt.Fprintf(stderr, "remote revoke: %v\n", err)
		return 1
	}
	defer client.Close()

	if err := client.RevokeDevice(deviceID); err != nil {
		fmt.Fprintf(stderr, "remote revoke: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "revoked device %s\n", deviceID)
	return 0
}

// runRemotePair is the `swarm remote pair` verb: it runs the OWNER side of pairing — the
// local desktop confirm, the independent SECOND gate (ADR D3). It dials the owner daemon
// (CapPairing, like runRemoteDevices), starts the handshake via StartPairing, prints the
// QR + rendezvous for the phone to scan, blocks until the phone reaches the SAS gate and
// shows the SAS emoji + the requesting device name, reads the operator's allow/deny from
// stdin (INJECTED so the confirm is testable without a TTY — never os.Stdin here), sends
// the decision, then blocks on the terminal result and prints it. A declined, dropped, or
// failed pairing exits nonzero — fail closed, nothing enrolled.
func runRemotePair(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("remote pair", flag.ContinueOnError)
	fs.SetOutput(stderr)
	capability := fs.String("capability", "full", "capability tier to grant the new device")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	client, err := dialClient([]string{protocol.CapPairing})
	if err != nil {
		fmt.Fprintf(stderr, "remote pair: %v\n", err)
		return 1
	}
	defer client.Close()

	sess, err := client.StartPairing(protocol.PairStartReq{Capability: *capability})
	if err != nil {
		fmt.Fprintf(stderr, "remote pair: %v\n", err)
		return 1
	}
	defer sess.Close()

	// The rendezvous view bootstraps the phone: scanning the QR recovers the rendezvous
	// id + single-use pairing secret it drives the device leg with.
	fmt.Fprintln(stdout, "Scan this QR on your phone to pair:")
	fmt.Fprintln(stdout, sess.QR)
	fmt.Fprintf(stdout, "rendezvous: %s\n", sess.RendezvousID)
	if sess.ExpiresAt != nil {
		fmt.Fprintf(stdout, "expires: %s\n", sess.ExpiresAt.Format(timeFormat))
	}

	// Block until the phone reaches the SAS gate. A terminal result arriving FIRST (a
	// rendezvous/TTL failure or a dropped session, before any gate) unblocks here fail
	// closed rather than hanging.
	var pending protocol.PairingPending
	select {
	case pending = <-sess.Pending():
	case <-sess.Result():
		fmt.Fprintln(stderr, "remote pair: pairing ended before the device connected")
		return 1
	}

	// The independent second gate (ADR D3): the operator verifies the SAS emoji against
	// the phone's screen and allows or denies at the desktop.
	fmt.Fprintf(stdout, "Device: %s\n", pending.DeviceName)
	// sonnet#4: echo the capability tier being granted so the operator sees the authority
	// they are about to hand this device (default "full") before allowing -- the SAS proves
	// WHICH phone, this line proves WHAT it may do.
	fmt.Fprintf(stdout, "Capability to grant: %s\n", *capability)
	fmt.Fprintf(stdout, "Verify these emoji match your phone: %s\n", strings.Join(pending.SAS, " "))
	fmt.Fprint(stdout, "Allow this device? [y/N]: ")

	allow := readYesNo(stdin)
	if err := sess.Confirm(allow); err != nil {
		fmt.Fprintf(stderr, "remote pair: %v\n", err)
		return 1
	}

	// The single terminal outcome: a real pair_result, or a fail-closed non-paired result
	// on a dropped session / Close.
	res := <-sess.Result()
	if !res.Paired {
		if !allow {
			fmt.Fprintln(stdout, "pairing declined")
		} else {
			fmt.Fprintln(stderr, "remote pair: pairing failed")
		}
		return 1
	}
	name := res.Name
	if name == "" {
		name = res.DeviceID
	}
	fmt.Fprintf(stdout, "paired %s\n", name)
	return 0
}

// readYesNo reads one line from r and reports whether it is an affirmative answer
// (y/yes, case-insensitive). EOF or anything else is a NO: the confirm gate fails closed
// on absent or ambiguous input.
func readYesNo(r io.Reader) bool {
	sc := bufio.NewScanner(r)
	if !sc.Scan() {
		return false
	}
	ans := strings.ToLower(strings.TrimSpace(sc.Text()))
	return ans == "y" || ans == "yes"
}

// runRemoteStatus is the `swarm remote status` verb: a READ-ONLY operator report that
// composes existing reads (no new wire op). It prints three things: (1) whether remote
// control is configured — the machine identity at <stateDir>/remote/machine.key and the
// relay at <stateDir>/remote/relay.json that `swarm remote init` provisions; (2) the
// effective remote-control state — the durable manual override from
// <stateDir>/remote-state.json (A4) composed with the live device roster, mirroring the
// daemon's RemoteControlEnabled (manual off WINS; otherwise device-derived); and (3) the
// paired-device roster from the owner client's ListDevices, dialed like `swarm remote
// devices`. It degrades gracefully: an absent config/state file is a reported state, not
// an error, and an unreachable daemon leaves the roster "unavailable" rather than
// crashing. It exits 0 whenever it can resolve the state dir and produce a report.
func runRemoteStatus(_ []string, stdout, stderr io.Writer) int {
	stateDir := os.Getenv(daemon.EnvStateDir)
	if stateDir == "" {
		var err error
		if stateDir, err = persist.DefaultDir(); err != nil {
			fmt.Fprintf(stderr, "remote status: %v\n", err)
			return 1
		}
	}

	// 1. Configuration presence (machine identity + relay), both under <stateDir>/remote/.
	remoteDir := filepath.Join(stateDir, "remote")
	hasIdentity := statFileExists(filepath.Join(remoteDir, remoteIdentityFile))
	hasRelay := statFileExists(filepath.Join(remoteDir, remoteRelayFile))
	switch {
	case hasIdentity && hasRelay:
		fmt.Fprintln(stdout, "configuration: initialized (identity + relay)")
	case hasIdentity:
		fmt.Fprintln(stdout, "configuration: initialized (identity; no relay configured)")
	default:
		fmt.Fprintln(stdout, "configuration: not initialized (run swarm remote init)")
	}

	// 2. Durable manual kill-switch override from <stateDir>/remote-state.json (A4): the
	// authoritative owner override. The derived on/off is recomputed from device presence,
	// so it is composed with the live roster below rather than trusting the advisory
	// `enabled` mirror.
	manualOff := readRemoteManualOff(stateDir)

	// 3. Device roster (best-effort): dial the owner daemon like `swarm remote devices`.
	// Status is a read that must never crash if the daemon is down.
	devices, listErr := statusListDevices()

	// Effective remote-control state, mirroring coreAPI.RemoteControlEnabled: manual off
	// WINS over device presence; otherwise it is device-derived.
	switch {
	case manualOff:
		fmt.Fprintln(stdout, "remote control: OFF (manual override)")
	case listErr != nil:
		fmt.Fprintln(stdout, "remote control: unknown (daemon unreachable)")
	case len(devices) > 0:
		fmt.Fprintln(stdout, "remote control: ON (device-derived)")
	default:
		fmt.Fprintln(stdout, "remote control: OFF (device-derived; no devices paired)")
	}

	// Roster.
	if listErr != nil {
		fmt.Fprintf(stdout, "paired devices: unavailable (%v)\n", listErr)
		return 0
	}
	fmt.Fprintf(stdout, "paired devices (%d):\n", len(devices))
	for _, d := range devices {
		fmt.Fprintf(stdout, "  %s  %s\n", d.DeviceID, d.Name)
	}
	return 0
}

// statusListDevices dials the owner daemon (CapPairing, like runRemoteDevices) and
// returns the paired-device roster. Any dial or list failure is returned so status can
// report the roster as unavailable rather than crash.
func statusListDevices() ([]protocol.DeviceView, error) {
	client, err := dialClient([]string{protocol.CapPairing})
	if err != nil {
		return nil, err
	}
	defer client.Close()
	return client.ListDevices()
}

// readRemoteManualOff reports the durable owner kill-switch override from
// <stateDir>/remote-state.json (the same file `swarm remote off`/`on` write, A4). An
// absent file means the override was never set (device-derived). A present-but-unreadable
// or corrupt file fails CLOSED (manual off), matching the daemon's loadRemoteState, so
// status never under-reports a severed remote-control surface.
func readRemoteManualOff(stateDir string) bool {
	b, err := os.ReadFile(filepath.Join(stateDir, remoteStateFile))
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		return true
	}
	var st struct {
		ManualOff bool `json:"manual_off"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return true
	}
	return st.ManualOff
}

// statFileExists reports whether path exists (any stat error, including not-exist, is
// treated as absent) — a read-only presence probe for status's configuration report.
func statFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
