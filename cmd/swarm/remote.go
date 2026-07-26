package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/machineid"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/qrterm"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/supervise"
	"github.com/Nathandela/swarm/internal/remotegw"
)

const remoteUsage = `usage: swarm remote <command>

  swarm remote init      provision this machine's pairing identity
  swarm remote devices   list paired devices
  swarm remote revoke    revoke a paired device
  swarm remote regrant   re-issue a paired device's epoch grant
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
	case "regrant":
		return runRemoteRegrant(args[1:], stdout, stderr)
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

// gatewayBinary is the gateway sidecar's binary name, resolved into the supervision unit's
// ExecStart. It is the name .goreleaser.yaml's swarm-remote build ships in the SAME archive
// as swarm, so an installed swarm always has one next to it to point at.
const gatewayBinary = "swarm-remote"

// remoteSocketFile is the default remote-tier UDS under the state dir. ADR-007 D4: the
// gateway dials the dedicated remote socket, never the owner socket.
const remoteSocketFile = "remote.sock"

// gatewayLogFile is where the supervision unit sends the gateway's stdout and stderr.
// Inside the state dir, which is already the 0700 tree that guards the machine identity.
const gatewayLogFile = "gateway.log"

// newGatewaySupervisor is the CLI's hook into gateway supervision. ADR-007 D5 forbids the
// daemon spawning the gateway, so the owner-invoked CLI is the ONLY thing that installs
// (`swarm remote init`) or activates (`swarm remote pair`) the unit. It is a var so tests
// substitute a fake and never touch the real launchd/systemd.
var newGatewaySupervisor = func(stateDir string) (supervise.Supervisor, error) {
	return supervise.Host(stateDir)
}

// osExecutable is os.Executable, as a var so a test can place this binary in a synthetic
// install layout. resolveGatewayBinary looks for the gateway BESIDE it.
var osExecutable = os.Executable

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
// VALIDATED (see validateRelayURL) and then persisted to
// <stateDir>/remote/relay.json ({"relay_url":"..."}, 0600) — the exact shape
// internal/skeleton.loadRelayURL reads back. Without the flag, relay.json is
// left untouched (absent), so remote pairing stays unconfigured. An invalid URL
// is refused BEFORE any filesystem work, so a rejected run provisions nothing
// and a corrected re-run starts clean.
func runRemoteInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("remote init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	relayURL := fs.String("relay-url", "", "relay server URL for remote pairing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *relayURL != "" {
		if err := validateRelayURL(*relayURL); err != nil {
			fmt.Fprintf(stderr, "remote init: %v\n", err)
			return 1
		}
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

	// PB-LIFE-2, install half: provisioning a machine also installs its gateway
	// supervision unit, so a later `swarm remote pair` has something to activate.
	//
	// It then converges on the state the machine's DEVICE COUNT already implies. With zero
	// paired devices that is quiescence (PB-LIFE-3(a)) and nothing is started -- starting a
	// gateway there would be the crash loop that requirement exists to prevent. With the one
	// device of single-device v1 it is active, and this is the ONLY command that can get
	// there: `swarm remote pair` is refused while a device is paired (internal/skeleton's
	// "a device is already paired; revoke it first"), so an owner whose gateway is down --
	// a transient launchctl refusal at pair time, an upgrade from a build that installed no
	// unit -- would otherwise have no supported way to start it. init is idempotent and
	// always available, which is what makes it the right place.
	if installGatewayUnit(stateDir, stderr) && supervise.Desired(pairedDeviceCount(stateDir)) == supervise.StateActive {
		ensureGatewayRunning("init", stderr)
	}

	fmt.Fprintln(stdout, id.String())
	return 0
}

// pairedDeviceCount reads the durable device roster straight from <stateDir>/devices, the
// same registry the daemon and cmd/swarm-remote open. It is a READ, so it does not dial
// (and therefore never auto-starts) a daemon: `swarm remote init` provisions a machine and
// must keep working on one where nothing is running yet.
//
// An unreadable or malformed registry reports 0. Nothing is lost by that: 0 is quiescent,
// so the only consequence is that init installs the unit without activating it, which is
// exactly what it did before -- and a registry the CLI cannot read is one the daemon
// refuses to start on anyway (device.Open is fail-closed), so it will be reported there.
func pairedDeviceCount(stateDir string) int {
	reg, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		return 0
	}
	return reg.Count()
}

// installGatewayUnit writes/refreshes this machine's gateway supervision unit under
// <stateDir>/remote/units, reporting whether a unit is now installed.
//
// Every failure here is a WARNING, never a nonzero exit: `swarm remote init`'s durable
// work -- the machine identity -- is already done and must not be undone by a missing
// gateway binary, and a source checkout with no swarm-remote anywhere must still be able
// to provision an identity and pair. Silence is not an option either: the operator would
// otherwise learn about it only when a paired phone receives nothing.
func installGatewayUnit(stateDir string, stderr io.Writer) bool {
	warn := func(err error) {
		fmt.Fprintf(stderr, "remote init: no gateway supervision unit installed (%v); "+
			"the gateway will not start on its own\n", err)
	}

	exe, err := resolveGatewayBinary()
	if err != nil {
		warn(err)
		return false
	}
	// Review F4: supervise.Spec accepts an empty RemoteSocket and simply omits the variable
	// from the rendered unit, but cmd/swarm-remote reads SWARM_DAEMON_REMOTE_SOCK with NO
	// fallback -- so such a unit hands the gateway an empty dial target, which fails, which
	// the supervisor restarts forever. That is PB-LIFE-7 again. runRemoteInit writes the
	// machine identity before it reaches here, so this is unreachable today; the guard is
	// what keeps that ordering from becoming an unwritten rule a second install site breaks.
	sock := gatewaySocket(stateDir)
	if sock == "" {
		warn(errors.New("this machine has no remote identity, so the unit would name no remote " +
			"socket and the gateway would have nothing to dial; run `swarm remote init` first"))
		return false
	}
	sup, err := newGatewaySupervisor(stateDir)
	if err != nil {
		warn(err)
		return false
	}
	if err := sup.Install(supervise.Spec{
		Exec:         exe,
		Owner:        gatewayOwner(),
		StateDir:     stateDir,
		RemoteSocket: sock,
		LogPath:      filepath.Join(stateDir, "remote", gatewayLogFile),
		// Backoff left zero: supervise.DefaultBackoff is PB-LIFE-5's floor.
	}); err != nil {
		warn(err)
		return false
	}
	return true
}

// resolveGatewayBinary finds the swarm-remote this install ships, as an ABSOLUTE path -- a
// supervisor resolves a relative one against a working directory nobody controls.
//
// ADJACENCY FIRST. swarm and swarm-remote ride in one archive (.goreleaser.yaml), so the
// gateway is a sibling of the running binary, and that is the relationship the install
// actually guarantees. PATH is not: a Homebrew cask links only the binaries it declares, a
// tarball is unpacked wherever the operator likes, and an older swarm-remote earlier on
// PATH would silently win. Symlinks are resolved first because the sibling is next to the
// REAL file -- `brew` puts a link in its bin directory pointing into the Caskroom, where
// both binaries live together.
//
// PATH remains the fallback: a source checkout has `go install`ed both into GOBIN with no
// archive layout at all.
func resolveGatewayBinary() (string, error) {
	if self, err := osExecutable(); err == nil {
		if self, err = filepath.EvalSymlinks(self); err == nil {
			sibling := filepath.Join(filepath.Dir(self), gatewayBinary)
			if isExecutableFile(sibling) {
				return sibling, nil
			}
		}
	}
	exe, err := exec.LookPath(gatewayBinary)
	if err != nil {
		return "", err
	}
	return filepath.Abs(exe)
}

// isExecutableFile reports whether path is a regular file this user could exec.
func isExecutableFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// gatewayOwner is the user the gateway will run as: whoever runs this command. Both unit
// types run as the user that loads them, so this is a statement of FACT recorded in the
// unit, not an authority the file confers. The environment fallback is what a
// CGO_ENABLED=0 macOS build needs, where os/user cannot read the directory service.
func gatewayOwner() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}

// gatewaySocket is the ONE definition of this machine's remote-tier UDS (ADR-007 B15):
// the socket the supervised gateway dials AND the socket the daemon itself opens
// (skeletonConfigFromEnv reads it from here too). Two independent defaults that had to
// agree was the PB-LIFE-7 bug class -- the daemon required an env var while the unit
// defaulted to D4's canonical path, so a stock install paired and then went silent.
//
// Explicit configuration wins; otherwise <stateDir>/remote.sock (D4), but ONLY once the
// machine is PROVISIONED for remote. The machine identity is the marker: `swarm remote
// init` writes it before anything here is consulted, it is what the daemon's pairing
// config loads, and an install that never ran it opens no remote socket at all -- remote
// control stays as absent as it was before, without a second definition to drift.
func gatewaySocket(stateDir string) string {
	if sock := os.Getenv(daemon.EnvRemoteSocket); sock != "" {
		return sock
	}
	if _, err := os.Stat(filepath.Join(stateDir, "remote", remoteIdentityFile)); err != nil {
		return "" // not provisioned: no remote tier
	}
	return filepath.Join(stateDir, remoteSocketFile)
}

// remoteSocketProbeTimeout bounds the liveness probe below. A UDS dial answers or refuses
// immediately, so this only covers a listener whose backlog is full -- it is a ceiling on
// how long an owner-invoked command can block, not an expected wait.
const remoteSocketProbeTimeout = 2 * time.Second

// remoteSocketServed reports whether something is accepting on this machine's remote-tier
// socket RIGHT NOW. It performs the gateway's own first act (the sidecar dials
// Spec.RemoteSocket) before the gateway is handed to a supervisor that would restart it
// forever on failure. Reading the configuration cannot answer this: the daemon's listener
// is a property of the process that is running, not of the state dir as it stands now.
func remoteSocketServed(path string) bool {
	c, err := net.DialTimeout("unix", path, remoteSocketProbeTimeout)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// validateRelayURL checks --relay-url BEFORE it is persisted, because this is the last
// moment an operator is present to fix it. The string is carried VERBATIM into the pairing
// QR (PB-PAIR-7) as the only address a scanning phone will ever have, and it is the ONE
// free variable in that QR's size budget (PB-PAIR-1(b)) — every other field is
// fixed-width. So two properties have to hold, and neither is checkable later: a phone
// must be able to dial it, and a standard terminal must still be able to DRAW the symbol
// that carries it. Past pairing.MaxRelayURLLen the symbol steps to a version no 24-row
// terminal can show, and `swarm remote pair` then draws nothing at all — with the config
// file, not the terminal, as the cause.
//
// Nothing is normalized or trimmed here, only refused: a rewritten URL is a different
// destination, and the machine's own dial target is the one endpoint known reachable.
func validateRelayURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("--relay-url %q is blank", raw)
	}
	if raw != strings.TrimSpace(raw) {
		return fmt.Errorf("--relay-url %q has leading or trailing whitespace; it is carried "+
			"verbatim into the pairing QR and is never trimmed", raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("--relay-url %q is not a URL: %w", raw, err)
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return fmt.Errorf("--relay-url %q has scheme %q; the relay is a websocket endpoint, "+
			"so the scheme must be ws or wss", raw, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("--relay-url %q carries no host; a phone that scans the pairing QR "+
			"would have nothing to dial", raw)
	}
	if len(raw) > pairing.MaxRelayURLLen {
		return fmt.Errorf("--relay-url is %d characters; at most %d fit. Past that the pairing "+
			"QR needs a symbol larger than a standard %dx%d terminal can draw (PB-PAIR-1(b)), "+
			"so `swarm remote pair` would print no QR at all",
			len(raw), pairing.MaxRelayURLLen, defaultTermCols, defaultTermRows)
	}
	return nil
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
	// PB-STATE-10: this listing is the recovery's "identify the stranded device" step,
	// and an operator who has just read a device id has to be told what to do with it --
	// otherwise the next step is reachable only by already knowing the verb.
	//
	// ON STDERR, because stdout is the TABLE. A trailing prose line there is a row to
	// anything parsing the listing, which is what a device id printed for the purpose of
	// being copied invites.
	if len(devices) > 0 {
		fmt.Fprintln(stderr, "to unregister one: swarm remote revoke <device-id>")
	}
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
// daemon, revokes the device, purges the state the revocation orphans on BOTH
// sides, and prints a confirmation naming the step that finishes the recovery.
//
// THE ORDER IS LOAD-BEARING (PB-STATE-10, ADR-007 B22):
//
//   - the routing id is read BEFORE the daemon revoke, because the revoke deletes
//     the registry record that carries it and the relay purge cannot be addressed
//     without it;
//   - the gateway is stopped BEFORE the relay is dialled, because the gateway holds
//     the machine's one relay connection under the same relay-auth identity and a
//     second connection for a routing id SUPERSEDES the first;
//   - the outbox is purged AFTER the gateway is stopped, so nothing is writing it.
//
// EVERY PURGE FAILURE IS A WARNING, never a nonzero exit. The revocation itself is
// already durable by then -- the device is de-registered and the epoch rotated -- so
// failing the command would tell the owner the revoke did not happen when it did,
// and leave them no forward step. This is stopGatewayIfQuiescent's rule, for the
// same reason.
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

	stateDir := remoteStateDir()
	routingID := deviceRoutingID(stateDir, deviceID)

	if err := client.RevokeDevice(deviceID); err != nil {
		fmt.Fprintf(stderr, "remote revoke: %v\n", err)
		return 1
	}
	stopGatewayIfQuiescent(stderr)
	purgeRelayState(stateDir, routingID, stderr)
	purgeOutboundCustody(stateDir, stderr)

	fmt.Fprintf(stdout, "revoked device %s\n", deviceID)
	// PB-STATE-10: the revoke is the MIDDLE of a four-step recovery, not the end of a
	// job. An owner who stops here has a machine with no device and a handset that
	// still cannot pair, because nothing told them there was another step.
	fmt.Fprintln(stdout, "run `swarm remote pair` to pair a device again")
	return 0
}

// remoteStateDir resolves the state dir every remote verb reads, the same way
// dialClient does. An unresolvable one is "": the callers below all treat that as
// "nothing provisioned here", which is the truth.
func remoteStateDir() string {
	if dir := os.Getenv(daemon.EnvStateDir); dir != "" {
		return dir
	}
	dir, err := persist.DefaultDir()
	if err != nil {
		return ""
	}
	return dir
}

// deviceRecord reads one device out of the durable registry, as a READ exactly like
// pairedDeviceCount: it does not dial, so it works on a machine whose daemon is not
// running. A missing record is reported rather than guessed at -- both callers do
// nothing at all rather than address the relay with something invented.
func deviceRecord(stateDir, deviceID string) (device.Record, bool) {
	if stateDir == "" {
		return device.Record{}, false
	}
	reg, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		return device.Record{}, false
	}
	return reg.Get(deviceID)
}

// deviceRoutingID is the relay mailbox address the machine appends to for a device. It
// must be read BEFORE the revoke, which deletes the record that carries it.
func deviceRoutingID(stateDir, deviceID string) string {
	rec, ok := deviceRecord(stateDir, deviceID)
	if !ok {
		return ""
	}
	return string(rec.RoutingID)
}

// errRelayNotProvisioned means this machine has no relay identity or no relay URL, so
// it holds no relay-side state at all. Every caller below treats it as "nothing to do"
// rather than as a failure: `swarm remote init` without --relay-url is a supported
// state, and a local-only machine must not be told its relay work failed.
var errRelayNotProvisioned = errors.New("this machine is not provisioned for a relay")

// withMachineRelay runs fn against an authenticated relay connection opened with THIS
// MACHINE's own relay-auth identity -- the same identity cmd/swarm-remote's gateway
// uses, so anything fn can do is something the owner's machine can do.
//
// THE CLI DIALS THE RELAY ITSELF, and that is a new responsibility rather than a wiring
// change (ADR-007 B22). Neither the CLI nor the daemon holds a relay connection; only
// the gateway sidecar does. But the two owner acts that must reach the relay -- purging
// a revoked device's mailbox, and authorizing a freshly paired one -- happen at moments
// when the gateway is by construction NOT running: revoke stops it, and pairing is only
// permitted when the device count is zero, which is quiescent (PB-LIFE-3).
//
// The connection is short-lived on purpose. The relay supersedes an older connection for
// the same routing id, so a long-lived CLI client would sever a gateway that came up
// underneath it.
func withMachineRelay(stateDir string, fn func(context.Context, *relay.Client) error) error {
	if stateDir == "" {
		return errRelayNotProvisioned
	}
	relayURL := readRelayURL(stateDir)
	idPath := filepath.Join(stateDir, "remote", remoteIdentityFile)
	if relayURL == "" || !statFileExists(idPath) {
		return errRelayNotProvisioned
	}
	id, err := machineid.Load(idPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), remoteRelayOpTimeout)
	defer cancel()
	cl, err := relay.Dial(ctx, relayURL, relay.ClientAuth{
		RelayAuthPub: id.RelayAuthPublic(),
		Sign:         func(challenge []byte) ([]byte, error) { return id.RelayAuthSign(challenge), nil },
	})
	if err != nil {
		return err
	}
	defer cl.Close()
	return fn(ctx, cl)
}

// remoteRelayOpTimeout bounds the relay round trips these verbs make. The owner is
// sitting at a terminal and the local half of the work is already durable, so a relay
// that is down must cost them a reported delay and not a hang.
const remoteRelayOpTimeout = 10 * time.Second

// readRelayURL reads <stateDir>/remote/relay.json, the file `swarm remote init
// --relay-url` writes and internal/skeleton's loadRelayURL reads back. An absent or
// unreadable file means this machine is not provisioned for remote pairing at all.
func readRelayURL(stateDir string) string {
	raw, err := os.ReadFile(filepath.Join(stateDir, "remote", remoteRelayFile))
	if err != nil {
		return ""
	}
	var cfg struct {
		RelayURL string `json:"relay_url"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return ""
	}
	return cfg.RelayURL
}

// purgeRelayState is the RELAY half of PB-STATE-10's "purge machine and relay state":
// it empties the revoked handset's mailbox and drops its push token, via the relay's own
// device_revoke op -- which until this slice had no production caller anywhere in the
// tree, so the requirement's third step was performed by nothing.
//
// Without it the stranded mailbox keeps whatever the gateway appended while the phone
// was silent, up to the relay's 7-day retention. A handset that recovers WITHOUT a full
// app-data wipe returns on the same routing id (device.key is minted once per install),
// reads frames sealed under the epoch this revoke rotated away, cannot open them, and
// cannot drain them -- and a mailbox that will not drain fills to its depth cap and
// refuses the new session's appends.
//
// relay.ErrNotAuthorized is not a failure here: it says the relay holds no pairing
// between this machine and that routing id, which is the same statement as "there is no
// mailbox of ours to empty" from the other end.
func purgeRelayState(stateDir, routingID string, stderr io.Writer) {
	if routingID == "" {
		return
	}
	err := withMachineRelay(stateDir, func(ctx context.Context, cl *relay.Client) error {
		return cl.DeviceRevoke(ctx, routingID)
	})
	switch {
	case err == nil,
		errors.Is(err, errRelayNotProvisioned),
		errors.Is(err, relay.ErrNotAuthorized):
	default:
		fmt.Fprintf(stderr, "remote revoke: the device is revoked, but its relay-side mailbox and "+
			"push token were not purged: %v\n", err)
	}
}

// authorizeAtRelay opens the machine -> device mailbox route for a device that has just
// paired, and -- ADR-007 B22 -- LIFTS any ban a previous revoke left on its routing id.
//
// IT IS PART OF PAIRING AND NOT ONLY OF GATEWAY STARTUP, which is the change. The gateway
// authorizes on every connect (cmd/swarm-remote/deliver.go), so before this the route
// existed from whenever a supervised process happened to boot. That was survivable for a
// FIRST pairing and is not for a RE-pairing: the recovered handset comes back on the same
// routing id, and it is banned until this op runs. Pairing is the moment the owner grants
// this device access, so it is where the grant is made -- and the gateway's own call
// stays, idempotently, for every reconnect after.
func authorizeAtRelay(stateDir, deviceID string, stderr io.Writer) {
	rec, ok := deviceRecord(stateDir, deviceID)
	// The consent is as load-bearing as the key: without it the relay refuses the
	// authorize outright (ADR-007 B38), so a record missing one is a device this
	// machine can open no route to, and there is nothing to attempt.
	if !ok || len(rec.RelayAuthPub) != ed25519.PublicKeySize || len(rec.ConsentSig) == 0 {
		return
	}
	err := withMachineRelay(stateDir, func(ctx context.Context, cl *relay.Client) error {
		return cl.AuthorizeDevice(ctx, ed25519.PublicKey(rec.RelayAuthPub), rec.ConsentSig)
	})
	if err != nil && !errors.Is(err, errRelayNotProvisioned) {
		fmt.Fprintf(stderr, "remote pair: the device is paired, but the machine could not open its "+
			"relay route: %v\n", err)
	}
}

// purgeOutboundCustody is the MACHINE half of the same step, at the one piece of
// durable machine state a revoke provably orphans and the next pairing then acts on.
//
// PB-GW-8's outbox holds reserved-but-uncommitted entries as the EXACT sealed bytes,
// and a replay re-appends them VERBATIM by contract (re-sealing would mint a fresh
// nonce). A stranded phone stops acking, so entries accumulate; the revoke rotates the
// epoch, so every one of them is now sealed under a key no future device holds -- and
// the gateway that comes up for the RE-PAIRED phone replays them into its mailbox,
// where nothing can ever open them.
func purgeOutboundCustody(stateDir string, stderr io.Writer) {
	if stateDir == "" {
		return
	}
	path := filepath.Join(stateDir, "remote", "outbound-journal.outbox")
	ob, err := remotegw.OpenOutbox(path)
	if err == nil {
		err = ob.Purge()
	}
	if err != nil {
		fmt.Fprintf(stderr, "remote revoke: the device is revoked, but the machine still holds "+
			"undelivered outbound frames sealed under the rotated epoch (%s): %v\n", path, err)
	}
}

// remoteRegrantUsage is `swarm remote regrant`'s usage message.
const remoteRegrantUsage = `usage: swarm remote regrant <device-id>
`

// runRemoteRegrant is the `swarm remote regrant` verb: PB-KEY-3's documented machine-side
// unblock, and the ONLY exit from a lost epoch grant.
//
// It exists because the two obvious remedies are both closed. The relay purges mailbox
// items past its retention cap even when never acked, so a bootstrap grant the phone missed
// is gone for good; and re-pairing is refused outright while a device is registered
// (BeginPairing fail-fasts on a non-empty registry), so the owner cannot simply start over.
// The same verb converges a device that slept through an epoch rotation (PB-KEY-4).
//
// IT BOUNCES THE GATEWAY, and that is not tidiness. The gateway loads the device's grant
// sidecar ONCE, at assembly, and appends it once per session; a running gateway therefore
// keeps delivering the grant that was already lost, and after a rotation it is still sealing
// every frame under an epoch key the phone cannot open. Without the bounce the regrant
// writes a correct sidecar that nothing ever delivers -- a repair that reports success and
// changes nothing on the handset.
func runRemoteRegrant(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprint(stderr, remoteRegrantUsage)
		return 2
	}
	deviceID := args[0]

	client, err := dialClient([]string{protocol.CapPairing})
	if err != nil {
		fmt.Fprintf(stderr, "remote regrant: %v\n", err)
		return 1
	}
	defer client.Close()

	if err := client.RegrantDevice(deviceID); err != nil {
		fmt.Fprintf(stderr, "remote regrant: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "re-granted device %s\n", deviceID)
	restartGatewayForDelivery(stderr)
	return 0
}

// restartGatewayForDelivery stops and re-ensures the gateway unit so it re-reads the grant
// sidecar and delivers the fresh bootstrap frame. Supervisor.Ensure is documented as "never
// a restart", so the stop is what makes this one. A machine with no unit installed is not an
// error -- the owner runs the gateway some other way and restarts it themselves -- but it IS
// reported, because a regrant nothing delivers is indistinguishable from no regrant at all.
func restartGatewayForDelivery(stderr io.Writer) {
	stateDir := remoteStateDir()
	if stateDir == "" {
		return
	}
	sup, err := newGatewaySupervisor(stateDir)
	if err == nil {
		if err = sup.Stop(); err == nil {
			err = sup.Ensure()
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "remote regrant: the grant was re-issued, but its gateway was not "+
			"restarted, so nothing will deliver it: %v\n", err)
	}
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

	// The rendezvous view bootstraps the phone: scanning the QR recovers the relay
	// endpoint, the rendezvous id, and the single-use pairing secret it drives the device
	// leg with.
	//
	// ORDER IS LOAD-BEARING: the session metadata is printed BEFORE the symbol and the
	// symbol is the LAST thing on screen when this blocks on Pending() below. A terminal
	// scrolls, so every row printed after the symbol pushes its top — the upper finder
	// patterns a scanner needs to lock onto — off a 24-row screen. See printPairingQR.
	fmt.Fprintf(stdout, "rendezvous: %s\n", sess.RendezvousID)
	if sess.ExpiresAt != nil {
		fmt.Fprintf(stdout, "expires: %s\n", sess.ExpiresAt.Format(timeFormat))
	}
	printPairingQR(stdout, sess.QR)

	// Block until the phone reaches the SAS gate. A terminal result arriving FIRST (a
	// rendezvous/TTL failure or a dropped session, before any gate) unblocks here fail
	// closed rather than hanging.
	var pending protocol.PairingPending
	select {
	case pending = <-sess.Pending():
	case <-sess.Result():
		fmt.Fprintln(stdout) // terminate printPairingQR's last, deliberately unterminated row
		fmt.Fprintln(stderr, "remote pair: pairing ended before the device connected")
		return 1
	}

	// The scan is over: the phone has connected, so the symbol may now be displaced. This
	// newline is the one printPairingQR left off its last row — spending it here rather
	// than there is what lets the symbol have the whole viewport (see printPairingQR).
	fmt.Fprintln(stdout)

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

	// PB-STATE-10 / ADR-007 B22: open this device's relay route NOW rather than at whatever
	// moment a supervised gateway happens to boot. It is also what lifts the relay ban a
	// previous `swarm remote revoke` left on a handset that recovers on the same routing id,
	// so it runs BEFORE the gateway is ensured -- the phone is already dialling.
	authorizeAtRelay(remoteStateDir(), res.DeviceID, stderr)

	// PB-LIFE-2: the phone that just paired has a gateway to talk to, with no second
	// command and no reboot. This is also what runs the epoch grant delivery
	// (cmd/swarm-remote's deliverEpochGrant) that makes the pairing usable at all.
	ensureGatewayRunning("pair", stderr)
	return 0
}

// ensureGatewayRunning activates this machine's gateway, for the verb that asked: the
// `pair` that just enrolled a device, or the `init` that found one already enrolled.
//
// It can only WARN, and the caller exits 0 regardless. On the pair path the enrollment is
// COMMITTED and durable by the time this runs: the device is in the registry, and it will
// be served the moment a gateway comes up, at the next login if not before. A nonzero exit
// would report that durable enrollment as a failure -- it is not one, and the one thing
// left to fix is not something a different exit status helps with. (It is not that a retry
// would enroll a second device: a second pairing is refused outright while one is paired,
// in internal/skeleton/pairing.go and again in the device registry's AddSole. The retry
// simply cannot happen.) Nothing is swallowed either: a phone that pairs and then goes
// quiet is the symptom, and the operator gets the cause on stderr.
func ensureGatewayRunning(verb string, stderr io.Writer) {
	warn := func(err error) {
		fmt.Fprintf(stderr, "remote %s: the gateway was not started: %v\n", verb, err)
	}

	stateDir := os.Getenv(daemon.EnvStateDir)
	if stateDir == "" {
		var err error
		if stateDir, err = persist.DefaultDir(); err != nil {
			warn(err)
			return
		}
	}
	// PB-LIFE-7 (review F1): ADR-007 B15 made the daemon's listen path and this unit's dial
	// path one DEFINITION, which cannot remove a disagreement of TIME. The daemon decided
	// its listener when IT started; a daemon that predates this machine's provisioning
	// serves nothing at gatewaySocket(), so the gateway handed to the supervisor below would
	// be restarted every throttle interval and never connect. The upgrade path is where this
	// actually bites: a pre-B15 daemon still running on an already-paired machine, and this
	// is the convergence command the owner was told to run.
	//
	// The gateway is still activated -- the unit is correct for the next daemon start and
	// the enrollment is durable -- but the operator is handed the one step that fixes it
	// now, rather than discovering it as a phone that pairs and then goes quiet.
	if sock := gatewaySocket(stateDir); sock != "" && !remoteSocketServed(sock) {
		fmt.Fprintf(stderr, "remote %s: nothing is serving the remote socket at %s, so the gateway "+
			"cannot reach the daemon and will be restarted until it can. The running daemon chose "+
			"its listener before this machine was provisioned for remote -- run `swarm daemon "+
			"restart` to pick it up.\n", verb, sock)
	}
	sup, err := newGatewaySupervisor(stateDir)
	if err != nil {
		warn(err)
		return
	}
	switch err := sup.Ensure(); {
	case err == nil:
	case errors.Is(err, supervise.ErrNotInstalled):
		fmt.Fprintf(stderr, "remote %s: this machine has no gateway supervision unit, so "+
			"the paired device will receive nothing. Run `swarm remote init` to install one.\n", verb)
	default:
		warn(err)
	}
}

// stopGatewayIfQuiescent returns this machine's gateway to quiescent once the roster the
// revoke just wrote no longer justifies one (PB-LIFE-3(c)); a machine that somehow still
// has its one device keeps its gateway, since supervise.Desired is the single definition
// of that.
//
// The revoked device's gateway is expected to notice and self-exit (internal/remotegw's
// ErrDeviceRevoked), but that path depends on it being able to READ the registry --
// deviceRevoked() reports false when it cannot -- and a gateway that survives its revoke
// is worse than one that merely lingers: Ensure on the NEXT pairing is a documented no-op
// against a running job, so the new phone would be served by a process still holding the
// revoked device's epoch. Revoke is the moment the owner is present and the desired state
// is unambiguous, so it is where the process is ended.
//
// Like ensureGatewayRunning it can only warn; the revocation itself is already durable. A
// machine with no unit installed has nothing to stop and is told nothing.
func stopGatewayIfQuiescent(stderr io.Writer) {
	stateDir := remoteStateDir()
	if stateDir == "" {
		return
	}
	if supervise.Desired(pairedDeviceCount(stateDir)) != supervise.StateQuiescent {
		return
	}
	sup, err := newGatewaySupervisor(stateDir)
	if err == nil {
		err = sup.Stop()
	}
	if err != nil && !errors.Is(err, supervise.ErrNotInstalled) {
		fmt.Fprintf(stderr, "remote revoke: the device is revoked, but its gateway was not "+
			"stopped: %v\n", err)
	}
}

// Terminal box used for the pairing QR when neither the environment nor the controlling
// terminal says otherwise: the standard terminal PB-PAIR-1(b) sizes the symbol against.
const (
	defaultTermCols = 80
	defaultTermRows = 24
)

// printPairingQR puts the pairing payload on the terminal as a SCANNABLE QR symbol
// (PB-PAIR-1) and degrades to manual entry when the terminal cannot take one: TERM=dumb
// cannot draw the glyphs, and a box too small makes the renderer REFUSE rather than emit
// a cropped symbol that only looks scannable (PB-PAIR-1(c)). The fallback never invites
// the operator to scan a bare string, which was the defect it replaces, and it names the
// cause it actually hit (see qrFallbackReason).
//
// The symbol gets the WHOLE terminal, and it gets it because of how a terminal scrolls,
// not despite it. The drawing is the last thing printed before the command blocks on the
// phone: rows above it scroll off the top harmlessly — the heading is simply gone by the
// time the operator lifts the camera — while any row printed after it pushes the symbol
// up instead, taking the upper finder patterns a scanner needs off screen with it. So the
// budget is not shared with the chrome; the only rule is that NOTHING is printed after the
// symbol until the phone has connected. That is worth one more module of quiet zone: on a
// standard 24-row terminal the payload's version-6 symbol draws at 47x24 with a quiet zone
// of 3, where reserving a row for the heading forced it down to 45x23 at the standard's
// floor of 2 — and on a 23-row terminal, forced it to draw nothing at all.
//
// The last symbol row is left UNTERMINATED for the same reason: the newline that would
// end it scrolls the terminal one row and costs the drawing its top. runRemotePair opens
// the post-scan block with that newline instead.
func printPairingQR(stdout io.Writer, payload string) {
	cols, rows := terminalBox()
	if r, err := renderPairingQR(payload, cols, rows); err == nil {
		// The payload stays available for manual entry (PB-PAIR-2), WRAPPED to the terminal
		// width — a line long enough to reflow would displace the symbol — and printed
		// ABOVE it, where it costs the symbol no rows.
		fmt.Fprintln(stdout, "Or enter this pairing code manually:")
		for _, line := range chunkLines(payload, cols) {
			fmt.Fprintln(stdout, line)
		}
		fmt.Fprintln(stdout, "Scan this QR on your phone to pair:")
		fmt.Fprint(stdout, r.Text)
		return
	}
	fmt.Fprintln(stdout, qrFallbackReason(payload, cols, rows))
	fmt.Fprintln(stdout, "Enter this pairing code on your phone:")
	// UNWRAPPED here: there is no symbol above to protect, and manual entry wants one
	// unbroken token to read or copy.
	fmt.Fprintln(stdout, payload)
}

// qrFallbackReason names why no symbol was drawn. Three causes land here and they are
// fixed in three different places — use another terminal, make this one bigger, or shorten
// the relay URL in <stateDir>/remote/relay.json — so one message covering all three
// misdirects two operators in three. The payload case is the one that bites: a relay URL
// past pairing.MaxRelayURLLen draws no symbol on ANY standard terminal, and reporting that
// as "terminal too small" on an 80x24 terminal that is neither small nor incapable sends
// the operator to resize a window that was never the problem.
func qrFallbackReason(payload string, cols, rows int) string {
	switch {
	case !terminalCanDrawQR():
		return "No QR symbol drawn: this terminal cannot draw the block glyphs a symbol needs " +
			"(TERM is unset or dumb)."
	case !qrFitsBox(payload, defaultTermCols, defaultTermRows):
		return fmt.Sprintf("No QR symbol drawn: this %d-character pairing code needs a symbol "+
			"larger than a standard %dx%d terminal can show. Re-run `swarm remote init "+
			"--relay-url` with a relay URL of at most %d characters.",
			len(payload), defaultTermCols, defaultTermRows, pairing.MaxRelayURLLen)
	default:
		return fmt.Sprintf("No QR symbol drawn: this terminal is %dx%d, too small for the symbol "+
			"(a standard %dx%d one shows it).", cols, rows, defaultTermCols, defaultTermRows)
	}
}

// renderPairingQR encodes payload and draws it inside a cols x rows box, erroring when
// the terminal cannot show a symbol at all.
func renderPairingQR(payload string, cols, rows int) (qrterm.Rendering, error) {
	if !terminalCanDrawQR() {
		return qrterm.Rendering{}, errors.New("terminal cannot draw a QR symbol")
	}
	sym, err := qrterm.Encode(payload)
	if err != nil {
		return qrterm.Rendering{}, err
	}
	return sym.Render(cols, rows)
}

// terminalCanDrawQR reports whether the terminal can show a drawn symbol at all. TERM=dumb
// — and an unset TERM, which promises as little — guarantees neither the block glyphs nor
// the SGR colours the drawing needs.
func terminalCanDrawQR() bool {
	t := os.Getenv("TERM")
	return t != "" && t != "dumb"
}

// qrFitsBox reports whether payload's symbol can be drawn inside a cols x rows box,
// independently of the terminal actually in front of the operator. It is how the fallback
// tells "this window is too small" from "no window would be big enough".
func qrFitsBox(payload string, cols, rows int) bool {
	sym, err := qrterm.Encode(payload)
	if err != nil {
		return false
	}
	_, err = sym.Render(cols, rows)
	return err == nil
}

// terminalBox is the drawing box for the pairing QR: COLUMNS/LINES when the environment
// sets them (the POSIX convention, and — since stdout is an injected writer — the only
// channel a caller can drive), else the controlling terminal, else the 80x24 standard.
func terminalBox() (cols, rows int) {
	cols, rows = envDim("COLUMNS"), envDim("LINES")
	if cols > 0 && rows > 0 {
		return cols, rows
	}
	w, h, err := term.GetSize(os.Stdout.Fd())
	if err != nil || w <= 0 || h <= 0 {
		w, h = defaultTermCols, defaultTermRows
	}
	if cols <= 0 {
		cols = w
	}
	if rows <= 0 {
		rows = h
	}
	return cols, rows
}

// envDim reads a positive terminal dimension from the environment; 0 when it is unset,
// unparseable, or nonsensical.
func envDim(name string) int {
	n, err := strconv.Atoi(os.Getenv(name))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// chunkLines splits s into lines of at most width cells.
func chunkLines(s string, width int) []string {
	r := []rune(s)
	var out []string
	for len(r) > width {
		out = append(out, string(r[:width]))
		r = r[width:]
	}
	return append(out, string(r))
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
