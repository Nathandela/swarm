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
	"github.com/Nathandela/swarm/internal/remote/relaycfg"
	"github.com/Nathandela/swarm/internal/remote/relayhome"
	"github.com/Nathandela/swarm/internal/remote/relaypurge"
	"github.com/Nathandela/swarm/internal/remote/supervise"
	"github.com/Nathandela/swarm/internal/remotegw"
)

const remoteUsage = `usage: swarm remote <command>

  swarm remote init      provision this machine's pairing identity
  swarm remote devices   list paired devices
  swarm remote revoke    revoke a paired device
  swarm remote regrant   re-issue a paired device's epoch grant
  swarm remote pair      pair a new device
  swarm remote presets   author the machine's launch presets (add/list)
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
		_, _ = fmt.Fprint(stderr, remoteUsage)
		return 2
	}
	switch args[0] {
	case "init":
		return runRemoteInit(args[1:], stdout, stderr)
	case "devices":
		return runRemoteDevices(args[1:], stdout, stderr)
	case "revoke":
		return runRemoteRevoke(args[1:], os.Stdin, stdout, stderr)
	case "regrant":
		return runRemoteRegrant(args[1:], stdout, stderr)
	case "pair":
		return runRemotePair(args[1:], os.Stdin, stdout, stderr)
	case "presets":
		return runRemotePresets(args[1:], stdout, stderr)
	case "off":
		return runRemoteSetControl(false, stdout, stderr)
	case "on":
		return runRemoteSetControl(true, stdout, stderr)
	case "status":
		return runRemoteStatus(args[1:], stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "remote: unknown remote command %q\n", args[0])
		return 2
	}
}

// remoteIdentityFile is the machine identity `runRemoteInit` persists, at
// <stateDir>/remote/machine.key. internal/skeleton.loadPairingConfig reads it
// back from the same path (see internal/skeleton/pairing_config.go) — the CLI
// and the daemon assembly must agree on it.
const remoteIdentityFile = "machine.key"

// remoteRelayFile is relaycfg.FileName, re-stated here only for the existence check
// `swarm remote status` makes. The FILE ITSELF is parsed and written in exactly one
// place, internal/remote/relaycfg, so its shape cannot drift between the three machine
// dial paths that read it (ADR-007 B34).
const remoteRelayFile = relaycfg.FileName

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
// <stateDir>/remote/relay.json at 0600, through the one parser that owns that
// file (internal/remote/relaycfg). Without the flag, relay.json is left
// untouched (absent), so remote pairing stays unconfigured.
//
// --relay-pin carries the relay certificate's SPKI pin (ADR-007 B34), the value
// docs/operations/relay-runbook.md section 3 produces. It is OPTIONAL — an
// unpinned machine keeps today's behaviour, which is what a loopback relay in
// local development needs — and MANDATORY IN EFFECT once set: all three machine
// dial paths refuse a relay that does not present the pinned key.
//
// Both flags are refused BEFORE any filesystem work, so a rejected run provisions
// nothing and a corrected re-run starts clean.
func runRemoteInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("remote init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	relayURL := fs.String("relay-url", "", "relay server URL for remote pairing")
	relayNamespace := fs.String("relay-namespace", "", "operator namespace for the relay-v2 machine home")
	relayPin := fs.String("relay-pin", "", "base64 SHA-256 of the relay certificate's SubjectPublicKeyInfo (see the relay runbook); mandatory under --relay-tls-policy pinned_spki, refused under webpki")
	relayTLSPolicy := fs.String("relay-tls-policy", "", "relay TLS verification policy: webpki (default) or pinned_spki (ADR-016)")
	relayPinCompat := fs.String("relay-pin-compat", "", "W9 compatibility SPKI pin published alongside --relay-tls-policy webpki, for handsets that predate ADR-016")
	pushGatewayURL := fs.String("push-gateway-url", "", "public HTTPS push gateway origin advertised for negotiated phone pairing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *relayURL != "" {
		if err := validateRelayURL(*relayURL); err != nil {
			_, _ = fmt.Fprintf(stderr, "remote init: %v\n", err)
			return 1
		}
		if err := relayhome.ValidateNamespace(*relayNamespace); err != nil {
			_, _ = fmt.Fprintf(stderr, "remote init: --relay-namespace: %v\n", err)
			return 1
		}
	} else if *relayNamespace != "" {
		_, _ = fmt.Fprintln(stderr, "remote init: --relay-namespace requires --relay-url")
		return 1
	}
	if err := relaycfg.ValidatePushGatewayURL(*pushGatewayURL); err != nil {
		_, _ = fmt.Fprintf(stderr, "remote init: --push-gateway-url: %v\n", err)
		return 1
	}
	if *pushGatewayURL != "" && *relayURL == "" {
		_, _ = fmt.Fprintln(stderr, "remote init: --push-gateway-url requires --relay-url")
		return 1
	}

	stateDir := os.Getenv(daemon.EnvStateDir)
	if stateDir == "" {
		var err error
		if stateDir, err = persist.DefaultDir(); err != nil {
			_, _ = fmt.Fprintf(stderr, "remote init: %v\n", err)
			return 1
		}
	}

	relayTLSPolicyEffective, relaySPKIPinEffective, relayTLSWarning, err := resolveRelayTLSPolicy(
		stateDir, *relayURL, *relayTLSPolicy, *relayPin, *relayPinCompat)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "remote init: %v\n", err)
		return 1
	}
	if err := validateRelayPin(*relayURL, relaySPKIPinEffective); err != nil {
		_, _ = fmt.Fprintf(stderr, "remote init: %v\n", err)
		return 1
	}
	if relayTLSWarning != "" {
		_, _ = fmt.Fprintf(stderr, "remote init: warning: %s\n", relayTLSWarning)
	}

	remoteDir := filepath.Join(stateDir, "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		_, _ = fmt.Fprintf(stderr, "remote init: %v\n", err)
		return 1
	}
	path := filepath.Join(remoteDir, remoteIdentityFile)

	var id *machineid.Identity
	if _, err := os.Stat(path); err == nil {
		// Identity already provisioned: load it rather than rotating (idempotent).
		id, err = machineid.Load(path)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "remote init: %v\n", err)
			return 1
		}
	} else if os.IsNotExist(err) {
		hostname, hErr := os.Hostname()
		if hErr != nil {
			hostname = "unknown"
		}
		id, err = machineid.Generate(hostname)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "remote init: %v\n", err)
			return 1
		}
		if err := id.Save(path); err != nil {
			_, _ = fmt.Fprintf(stderr, "remote init: %v\n", err)
			return 1
		}
	} else {
		_, _ = fmt.Fprintf(stderr, "remote init: %v\n", err)
		return 1
	}

	if *relayURL != "" {
		effectivePushGatewayURL := *pushGatewayURL
		if effectivePushGatewayURL == "" {
			if existing, found, loadErr := relaycfg.Load(stateDir); loadErr != nil {
				_, _ = fmt.Fprintf(stderr, "remote init: preserve push gateway config: %v\n", loadErr)
				return 1
			} else if found && existing.RelayURL == *relayURL {
				effectivePushGatewayURL = existing.PushGatewayURL
			}
		}
		if err := relaycfg.Save(stateDir, relaycfg.Config{
			RelayURL:          *relayURL,
			OperatorNamespace: *relayNamespace,
			PushGatewayURL:    effectivePushGatewayURL,
			TLSPolicy:         relayTLSPolicyEffective,
			SPKIPin:           strings.TrimSpace(relaySPKIPinEffective),
		}); err != nil {
			_, _ = fmt.Fprintf(stderr, "remote init: %v\n", err)
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

	_, _ = fmt.Fprintln(stdout, id.String())
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
		_, _ = fmt.Fprintf(stderr, "remote init: no gateway supervision unit installed (%v); "+
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
//
// AND THE ANSWER IS THE MOST STABLE NAME FOR THE FILE ADJACENCY PICKED (agents-tracker-nx44.4).
// Resolving symlinks finds the right FILE and produces a path that names one RELEASE: on a
// Homebrew cask that is /usr/local/Caskroom/swarm/<version>/swarm-remote, which the next `brew
// upgrade` deletes while the unit stamped with it goes on being exec'd -- EX_CONFIG on every
// restart until launchd parks the label, and the owner's phone served by nothing. A cask links
// swarm-remote as well as swarm (.goreleaser.yaml's binaries:), and re-points those links on
// every upgrade, so the link is the same program under a name that survives.
func resolveGatewayBinary() (string, error) {
	if self, err := osExecutable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(self); err == nil {
			sibling := filepath.Join(filepath.Dir(resolved), gatewayBinary)
			if isExecutableFile(sibling) {
				if stable := stableGatewayAlias(self, sibling); stable != "" {
					return stable, nil
				}
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

// stableGatewayAlias returns a path that names the same file as target through a location an
// upgrade does not move, or "" when none does.
//
// IT IS A RESOLUTION AND NOT A NAME MATCH, and that is the whole of its safety. The two
// candidates are the places an installer puts a link -- beside the path this binary was
// INVOKED by (pre-symlink: the cask's bin directory), and the gateway PATH answers with -- and
// either is accepted only when EvalSymlinks proves it lands on the very file adjacency picked.
// A different swarm-remote earlier on PATH is precisely what the adjacency rule exists to
// refuse, and preferring a stable-looking name over the shipped sibling would hand the
// supervisor another program under the gateway's name.
func stableGatewayAlias(self, target string) string {
	var candidates []string
	if filepath.IsAbs(self) {
		candidates = append(candidates, filepath.Join(filepath.Dir(self), gatewayBinary))
	}
	if p, err := exec.LookPath(gatewayBinary); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			candidates = append(candidates, abs)
		}
	}
	for _, cand := range candidates {
		if cand == target || !isExecutableFile(cand) {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(cand); err == nil && resolved == target {
			return cand
		}
	}
	return ""
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

// resolveRelayTLSPolicy applies ADR-016 W1's policy/pin independence, its one surviving
// legacy inference, W6's pre-write IP-literal refusal under webpki, and W6's own "nothing
// demotes a policy silently" rule applied to an OMITTED flag rather than a mistyped one
// (the webpki punch list's HIGH finding). It returns the policy to persist, the SPKI pin
// that ends up in relay.json's ONE relay_spki_pin field -- W1's "two spellings for one
// digest": --relay-pin under pinned_spki, --relay-pin-compat under webpki -- and a
// non-fatal warning for a webpki policy naming an obviously-private DNS suffix.
//
// The "nothing demotes a policy silently" guard fires for BOTH the explicit pinned_spki
// shape and the legacy shape relaycfg.Config.Security itself treats as pinned (TLSPolicy ==
// "" with a pin set) -- the legacy shape is the ONLY population that can exist on disk before
// anyone has ever typed --relay-tls-policy. It is skipped when relayURL == "", since that is
// exactly the invocation that never reaches relaycfg.Save (runRemoteInit only writes
// relay.json when a URL is given), so the bare, flagless `swarm remote init` -- the
// documented identity/gateway repair path -- stays available on an already-pinned machine.
// A relay.json that exists but fails to parse is propagated as a refusal rather than
// discarded, per relaycfg.Load's own fail-closed contract.
//
// Every refusal happens here, before any filesystem write, mirroring validateRelayURL and
// validateRelayPin's own contract.
func resolveRelayTLSPolicy(stateDir, relayURL, policyFlag, pin, pinCompat string) (policy, effectivePin, warning string, err error) {
	switch policyFlag {
	case "":
		// The one legacy inference W1 keeps, over the FLAG only: an operator's existing
		// --relay-pin invocation keeps its exact present meaning. Omitted entirely, the
		// default is webpki -- UNLESS this machine is ALREADY provisioned as pinned_spki,
		// in which case defaulting here would demote it with no flag typed at all. `swarm
		// remote init` is documented as idempotent, so a re-run with the destination
		// carried forward and nothing else typed is exactly the shape a real operator
		// reaches for; W1 already refuses a MISTYPED flag that would do this ("a single
		// flag would let one mistyped invocation move a machine between trust models in
		// silence" -- ADR-016:62), and an omitted one reaches the identical outcome through
		// the default rather than a typo. Load is a READ, so a fresh machine's first
		// `remote init` (nothing on disk yet) is unaffected.
		switch {
		case pin != "":
			policy = relaycfg.PolicyPinnedSPKI
		case relayURL == "":
			// No --relay-url means relaycfg.Save (runRemoteInit, below) is never reached on
			// THIS invocation, so there is nothing here that could demote an existing pin.
			// Skipping the disk check keeps the bare, flagless `swarm remote init` --
			// runRemoteInit's own documented identity/gateway repair path, the only
			// supported way to restart a down gateway -- available on an already-pinned
			// machine.
			policy = relaycfg.PolicyWebPKI
		default:
			existing, found, loadErr := relaycfg.Load(stateDir)
			if loadErr != nil {
				// relaycfg.Load's own contract: a file that exists but fails to parse IS an
				// error, "so a corrupt provisioning fails closed rather than silently
				// reverting to unconfigured." Discarding it here would let a truncated or
				// half-written relay.json be silently replaced by a fresh webpki one.
				return "", "", "", fmt.Errorf(
					"reading the existing %s before choosing a default --relay-tls-policy: %w",
					relaycfg.FileName, loadErr)
			}
			// The re-verification round's BLOCKING finding: this guard used to enumerate
			// the "already pinned" population as two exact-equality shapes (TLSPolicy ==
			// "pinned_spki", or the legacy TLSPolicy == "" with a pin set). But
			// relaycfg.Config.Security's OWN predicate is broader than either --
			// `if c.TLSPolicy != PolicyWebPKI { sec.PinnedSPKISHA256 = pin }` pins the
			// machine's REAL dials for ANY value that is not the literal "webpki", legacy
			// shape included. An unrecognised or wrong-cased policy string (a hand-edited
			// relay.json -- operator-runbook.md's own documented `printf` repair path --
			// or an N/N-1 downgrade reading a newer build's policy string) therefore PINS
			// the machine while the two-shape guard read it as unpinned. Matching
			// Security's own condition, rather than re-deriving it as a shape enumeration,
			// is what keeps the two from drifting apart again.
			if found && existing.TLSPolicy != relaycfg.PolicyWebPKI &&
				(existing.TLSPolicy != "" || existing.HasPin()) {
				shape := fmt.Sprintf("relay_tls_policy %q", existing.TLSPolicy)
				if existing.TLSPolicy == "" {
					shape = "no relay_tls_policy field (the legacy shape)"
				}
				return "", "", "", fmt.Errorf(
					"--relay-tls-policy was omitted, but this machine's %s already carries "+
						"a pinning trust model (%s); relaycfg.Config.Security reads "+
						"anything other than %q as pinned, so defaulting to %q here would "+
						"silently move it off the pin (ADR-016 W6: nothing demotes a policy "+
						"silently). Pass --relay-tls-policy pinned_spki --relay-pin <pin> to "+
						"keep the pin unchanged, or --relay-tls-policy webpki "+
						"--relay-pin-compat <pin> to make the move to webpki deliberate",
					relaycfg.FileName, shape, relaycfg.PolicyWebPKI, relaycfg.PolicyWebPKI)
			}
			policy = relaycfg.PolicyWebPKI
			// W9's compatibility pin is withdrawn only by the DELIBERATE act step 6
			// names -- typing --relay-tls-policy webpki with no --relay-pin-compat --
			// never by omission (the re-verification round's MEDIUM finding): a flagless
			// re-run against the SAME relay now carries an already-published
			// compatibility pin forward instead of dropping it, so re-running the
			// documented idempotent provisioning command does not itself un-migrate every
			// phone that has not yet moved off it (B58's "every dial refused" case,
			// reached this time by typing nothing rather than a policy demotion). A
			// changed --relay-url names a different relay, so its old pin is not carried.
			// NOTE: equality on RelayURL is deliberate and EXACT -- validateRelayURL
			// refuses to normalise, so a trailing slash names a different destination
			// and its pin is not carried (the re-verification round's LOW: same
			// omission shape, reached through cosmetic URL drift; carried verbatim
			// into the QR, so exactness wins over convenience).
			if pinCompat == "" && found && existing.TLSPolicy == relaycfg.PolicyWebPKI &&
				existing.HasPin() && existing.RelayURL == relayURL {
				carried, carryErr := existing.PinBase64()
				if carryErr != nil {
					return "", "", "", fmt.Errorf("carrying the compatibility pin forward: %w", carryErr)
				}
				pinCompat = carried
			}
		}
	case relaycfg.PolicyWebPKI, relaycfg.PolicyPinnedSPKI:
		policy = policyFlag
	default:
		return "", "", "", fmt.Errorf("--relay-tls-policy %q is neither %q nor %q",
			policyFlag, relaycfg.PolicyWebPKI, relaycfg.PolicyPinnedSPKI)
	}

	switch policy {
	case relaycfg.PolicyPinnedSPKI:
		if pinCompat != "" {
			return "", "", "", fmt.Errorf("--relay-pin-compat is legal only under --relay-tls-policy " +
				"webpki; under pinned_spki the pin IS the verification and --relay-pin is its one " +
				"spelling")
		}
		if pin == "" {
			return "", "", "", fmt.Errorf("--relay-tls-policy pinned_spki requires --relay-pin: under " +
				"this policy the pin is the whole of verification, and none is configured")
		}
		effectivePin = pin
	case relaycfg.PolicyWebPKI:
		if pin != "" {
			return "", "", "", fmt.Errorf("--relay-pin is refused under --relay-tls-policy webpki (the " +
				"default); use --relay-pin-compat instead to publish a compatibility pin for " +
				"handsets that predate ADR-016")
		}
		effectivePin = pinCompat
		if relayURL != "" {
			w, err := refuseIPLiteralUnderWebPKI(relayURL)
			if err != nil {
				return "", "", "", err
			}
			warning = w
		}
	}
	return policy, effectivePin, warning, nil
}

// refuseIPLiteralUnderWebPKI is ADR-016 W6: a webpki policy dialing an IP-literal wss://
// host is refused before any write -- this deployment's ACME topology has no ordinary
// public-CA path for an IP literal, and a policy that cannot succeed must not be written.
// Cleartext ws:// is untouched: the loopback carve-out is a separate, existing question
// this ADR leaves alone.
//
// A DNS name with an obviously-private suffix (.local, .localhost, .home.arpa) is WARNED
// about, not refused: unlike an IP literal it is a name webpki CAN in principle serve (an
// operator's own internal CA, a split-horizon zone), so writing it is not necessarily a
// mistake. What the warning names is the residual ADR-016 W3's amendment records:
// mobile/pairing.go's originIsPrivate classifies these SAME suffixes as private, so a
// webpki machine on one of them still reaches B45's unverified pairing fallback whenever
// the verified attempt fails -- Noise+SAS remain the content roots, but the pairing dial's
// TLS is not the property W3 recovers for this population.
func refuseIPLiteralUnderWebPKI(rawURL string) (warning string, err error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "wss" {
		return "", nil
	}
	host := u.Hostname()
	if net.ParseIP(host) != nil {
		return "", fmt.Errorf("--relay-url %q names an IP-literal host; --relay-tls-policy webpki (the "+
			"default) cannot succeed against one -- there is no ordinary public-CA path for an IP "+
			"literal. Use --relay-tls-policy pinned_spki with --relay-pin instead", rawURL)
	}
	if isObviouslyPrivateDNSSuffix(host) {
		return fmt.Sprintf("--relay-url %q names %q, a suffix that only resolves on a private "+
			"network; a phone pairing against it still falls back to the unverified pairing dial "+
			"whenever the verified attempt fails (ADR-016 W3's amendment), so consider "+
			"--relay-tls-policy pinned_spki with --relay-pin instead", rawURL, host), nil
	}
	return "", nil
}

// isObviouslyPrivateDNSSuffix mirrors the DNS-suffix half of mobile/pairing.go's
// originIsPrivate (the classifier the phone's own pairing-dial fallback and confirm sheet
// use) -- the same suffixes, so the CLI's warning and the phone's actual behaviour never
// name two different populations.
func isObviouslyPrivateDNSSuffix(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "localhost" || strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".home.arpa")
}

// validateRelayPin checks the effective SPKI pin BEFORE any filesystem work, so a rejected
// run provisions nothing (the same contract validateRelayURL has).
//
// pin is resolveRelayTLSPolicy's EFFECTIVE pin -- --relay-pin under pinned_spki,
// --relay-pin-compat under webpki -- since both end up in relay.json's one relay_spki_pin
// field and must satisfy the same three checks, the three ways an unusable pin is silently
// useless:
//
//   - A pin with no --relay-url has nothing to pin. relay.json is written as a unit, so
//     the pin would either be dropped or land beside no destination.
//   - A pin on a ws:// URL can never be applied: cleartext presents no certificate, and
//     relay.Security refuses the dial rather than running unpinned. Caught here, where the
//     operator is reading the output, instead of at the gateway's next start.
//   - A pin that is not base64 of a 32-byte digest is ErrPinMalformed. The relay runbook's
//     section 3 emits exactly the accepted form, and the value is checked against the same
//     parser every dial path uses, so `remote init` cannot accept something a dial rejects.
func validateRelayPin(relayURL, pin string) error {
	if pin == "" {
		return nil
	}
	if relayURL == "" {
		return fmt.Errorf("--relay-pin was given without --relay-url; a pin names the " +
			"certificate a relay must present, so there is nothing for it to apply to")
	}
	if u, err := url.Parse(relayURL); err == nil && u.Scheme == "ws" {
		return fmt.Errorf("--relay-pin was given with the cleartext relay URL %q; a ws:// "+
			"relay presents no certificate, so the pin can never be checked and the dial is "+
			"refused rather than run unpinned. Use wss://", relayURL)
	}
	if _, err := (relaycfg.Config{RelayURL: relayURL, SPKIPin: pin}).Security(); err != nil {
		return fmt.Errorf("--relay-pin %q is not usable: %w", pin, err)
	}
	return nil
}

// remoteRevokeUsage is `swarm remote revoke`'s usage message, printed to stderr
// when 2+ positional args are given (matched by TestRemoteRevoke_RequiresOneArg's
// "too many args" case). The 0-arg case no longer lands here — see
// runRemoteRevokeInteractive in remote_picker.go.
const remoteRevokeUsage = `usage: swarm remote revoke <device-id>
`

// runRemoteDevices is the `swarm remote devices` verb: it dials the daemon
// (requesting the CapPairing capability device_list needs), lists paired devices,
// and prints them as a table (device id, name, capability, paired-at) to stdout. An
// empty registry prints just the header, exit 0.
func runRemoteDevices(_ []string, stdout, stderr io.Writer) int {
	client, err := dialClient([]string{protocol.CapPairing})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "remote devices: %v\n", err)
		return 1
	}
	defer func() { _ = client.Close() }()

	devices, err := client.ListDevices()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "remote devices: %v\n", err)
		return 1
	}

	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "DEVICE ID\tNAME\tCAPABILITY\tPAIRED AT")
	for _, d := range devices {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", d.DeviceID, d.Name, d.Capability, d.PairedAt.Format(timeFormat))
	}
	_ = tw.Flush()
	// PB-STATE-10: this listing is the recovery's "identify the stranded device" step,
	// and an operator who has just read a device id has to be told what to do with it --
	// otherwise the next step is reachable only by already knowing the verb.
	//
	// ON STDERR, because stdout is the TABLE. A trailing prose line there is a row to
	// anything parsing the listing, which is what a device id printed for the purpose of
	// being copied invites.
	if len(devices) > 0 {
		_, _ = fmt.Fprintln(stderr, "to unregister one: swarm remote revoke <device-id>")
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
		_, _ = fmt.Fprintf(stderr, "remote %s: %v\n", verb, err)
		return 1
	}
	defer func() { _ = client.Close() }()

	if err := client.SetRemoteControl(enabled); err != nil {
		_, _ = fmt.Fprintf(stderr, "remote %s: %v\n", verb, err)
		return 1
	}
	if enabled {
		_, _ = fmt.Fprintln(stdout, "remote control enabled")
	} else {
		_, _ = fmt.Fprintln(stdout, "remote control disabled")
	}
	return 0
}

// runRemoteRevoke is the `swarm remote revoke` verb. With exactly one positional
// arg (the device id) it is the explicit path: dial, revoke, done — UNCHANGED by
// agents-tracker-7lkv, byte for byte. With zero args it is the interactive picker
// entry (runRemoteRevokeInteractive, remote_picker.go): a caller who does not want
// to type a 64-char hex id gets an arrow-key list instead, gated on stdin/stdout
// both being a terminal. Two or more args is a usage error (nonzero exit, no dial
// attempt), same as before.
func runRemoteRevoke(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return runRemoteRevokeInteractive(stdin, stdout, stderr)
	}
	if len(args) != 1 {
		_, _ = fmt.Fprint(stderr, remoteRevokeUsage)
		return 2
	}
	deviceID := args[0]

	client, err := dialClient([]string{protocol.CapPairing})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "remote revoke: %v\n", err)
		return 1
	}
	defer func() { _ = client.Close() }()

	return performRevoke(client, deviceID, stdout, stderr)
}

// performRevoke is the shared body of both revoke paths — the explicit-id verb
// above and the interactive picker's post-confirm step (remote_picker.go) — from a
// DIALED client and a CHOSEN device id onward: revoke, purge the state the
// revocation orphans on both sides, and print the confirmation naming the step that
// finishes the recovery.
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
// THE RELAY HALF DECIDES THE EXIT CODE (ADR-007 B120 F3, D9). Exit 0 here is a claim
// about the RELAY -- that the revoked handset keeps neither connectivity nor a drainable
// mailbox -- so it is made only once the relay has ACKNOWLEDGED the purge. A relay that
// refused and a relay that was never reached are different states with different
// remedies, and both leave the handset draining, so each says which one it is and both
// exit nonzero. B120 measured what "success" used to cover: a revoked handset that
// retained mailbox drain, push wake and a relay re-auth saying it had not been revoked.
// This REPLACES the earlier rule that every purge failure is a warning: that rule made
// the exit code a claim about the LOCAL half only, which is the claim B120 falsified.
//
// A NONZERO EXIT NEVER MEANS "NOTHING HAPPENED". The local half is durable before the
// relay is dialled at all -- the device is de-registered, the epoch rotated
// (2026-07-24 amendment), the gateway stopped, the outbound custody purged -- and the
// confirmation naming it and the next step is printed on every path. The failure is
// scoped, on stderr, to the half that did not finish. Both callers -- the explicit-id
// verb and the interactive picker -- inherit this, because both need the same honesty.
func performRevoke(client *protocol.Client, deviceID string, stdout, stderr io.Writer) int {
	stateDir := remoteStateDir()
	// FAIL CLOSED on a registry READ ERROR, before anything destructive (round-3
	// codex #1): with the routing id unreadable, the relay half could neither run nor
	// be deferred, and the old behavior reported exit-0 success over exactly that.
	if _, _, err := deviceRecordErr(stateDir, deviceID); err != nil {
		_, _ = fmt.Fprintf(stderr, "remote revoke: the device registry could not be read (%v); "+
			"revoking now would lose the relay half unrecoverably -- fix the registry and re-run.\n", err)
		return 1
	}
	routingID := deviceRoutingID(stateDir, deviceID)

	// THE OBLIGATION IS RECORDED BEFORE THE DESTRUCTIVE LOCAL REVOKE (SH5 review D1:
	// RevokeDevice deletes the only record carrying this routing id, and a crash --
	// or the operator's Ctrl-C during the silent relay stall below -- between that
	// delete and a later record loses the purge forever, the 2026-08-21 incident's
	// shape). If the relay half then LANDS, the obligation is retired in the same
	// run; if RevokeDevice itself fails, the stale obligation names a routing id the
	// registry still holds, and the next drive's live-pairing guard retires it. Only
	// a failed record is disclosed and tolerated: refusing to revoke a lost handset
	// because a bookkeeping write failed would invert the priorities.
	obligation := recordPurgeObligation(stateDir, routingID, stderr)

	if err := client.RevokeDevice(deviceID); err != nil {
		_, _ = fmt.Fprintf(stderr, "remote revoke: %v\n", err)
		return 1
	}
	stopGatewayIfQuiescent(stderr)
	purge, purgeErr := purgeRelayState(stateDir, routingID)
	purgeOutboundCustody(stateDir, stderr)

	_, _ = fmt.Fprintf(stdout, "revoked device %s\n", deviceID)
	if purge == relayPurgeDone {
		_, _ = fmt.Fprintln(stdout, "relay state purged: its mailbox, its push token and its route are gone from the relay")
	}
	// PB-STATE-10: the revoke is the MIDDLE of a four-step recovery, not the end of a
	// job. An owner who stops here has a machine with no device and a handset that
	// still cannot pair, because nothing told them there was another step.
	_, _ = fmt.Fprintln(stdout, "run `swarm remote pair` to pair a device again")

	switch purge {
	case relayPurgeRefused:
		_, _ = fmt.Fprintf(stderr, "remote revoke: the relay REFUSED to purge this device's relay-side state: %v\n", purgeErr)
		// A SUBSTANTIVE refusal from a reachable relay: nothing this machine
		// re-presents changes the answer, so the obligation is resolved NOW as a
		// tombstone -- excluded from the pair gate (round-2 Opus R2-1: the wedge
		// bricked pairing), reason preserved on file (round-2 codex #3: the unlanded
		// purge stays on the record; u37c's Done+Refusal shape). The relay-side state
		// survives, and the operator line says exactly that.
		if resolvePurgeObligation(stateDir, routingID, purgeErr, stderr) {
			_, _ = fmt.Fprintf(stderr, "remote revoke: the handset keeps its relay mailbox, its push wake and "+
				"its route (routing id %s). Nothing will re-present a refused purge; cleaning that state up "+
				"at the relay is now a manual task.\n", routingID)
		} else {
			// The tombstone write failed, so the obligation is STILL PENDING and the
			// next drive will re-present it (and gate pairing). Say that, not the
			// resolved-world sentence (post-commit codex #4).
			_, _ = fmt.Fprintf(stderr, "remote revoke: the handset keeps its relay mailbox, its push wake and "+
				"its route (routing id %s). Recording the refusal failed, so the purge stays PENDING and is "+
				"re-presented on this machine's next relay dial (`swarm remote pair` refuses until it "+
				"settles).\n", routingID)
		}
	case relayPurgePending:
		// The wording distinguishes never-reached from answered-but-clearing (a rate
		// window, a superseded connection) -- both defer, but they are different
		// diagnoses (round-3 codex #5).
		_, _ = fmt.Fprintf(stderr, "remote revoke: the relay did not accept the purge, so its half of this "+
			"revocation is PENDING: %v\n", purgeErr)
		reportDeferredPurge(routingID, obligation == purgeObligationRecorded, stderr)
	case relayPurgeUnprovisioned:
		// Three machines land here (post-commit codex #2). One never had a relay at
		// all -- nothing recordable, genuinely nothing of ours anywhere: the old
		// exit-0 "nothing to purge" truth, kept. One recorded an obligation and could
		// not dial (missing machine.key): exit 1 with the message below. And one OWED
		// an obligation but the recording itself failed: also exit 1 -- the record
		// helper already printed the abandonment -- because exit 0 would claim a
		// relay half that neither ran nor deferred.
		if obligation == purgeObligationNotApplicable {
			return 0
		}
		if obligation == purgeObligationFailed {
			return 1
		}
		_, _ = fmt.Fprintf(stderr, "remote revoke: this machine holds no relay identity (%v), so the relay half "+
			"of this revocation could not run. The handset keeps its relay mailbox, its push wake and its "+
			"route (routing id %s) at the owed relay until a restored identity drives the recorded purge, "+
			"or it is cleaned up there by hand.\n", purgeErr, routingID)
	default:
		// The relay half LANDED (or there was nothing of ours to purge): the
		// obligation recorded above is settled, and -- the relay being provably
		// reachable right now -- so is any OLDER deferral still on file.
		retirePurgeObligation(stateDir, routingID, stderr)
		driveRelayPurgeObligations(stateDir, stderr)
		return 0
	}
	return 1
}

// purgeObligationOutcome is recordPurgeObligation's three-valued answer (post-commit
// codex #2): "no obligation on disk" covers two different worlds -- a machine that
// never owed one, and a machine that owed one and could not write it -- and the
// unprovisioned exit code must not conflate them.
type purgeObligationOutcome int

const (
	purgeObligationNotApplicable purgeObligationOutcome = iota
	purgeObligationRecorded
	purgeObligationFailed
)

// recordPurgeObligation is ADR-007 D9's deferral, built (SH5, bead
// agents-tracker-dtc5; it retires the honest-ceiling note that used to sit in
// performRevoke). It runs BEFORE the destructive local revoke; a Failed outcome means
// the old abandonment is back for this one revoke, and every caller's report says so.
// The obligation carries the relay URL and machine identity it is owed under (reviews
// D2, codex #2): after a relay cutover or an identity change the purge must not
// "land" elsewhere and read as success.
func recordPurgeObligation(stateDir, routingID string, stderr io.Writer) purgeObligationOutcome {
	if routingID == "" || stateDir == "" {
		return purgeObligationNotApplicable
	}
	cfg, found, err := relaycfg.Load(stateDir)
	if err != nil || !found || cfg.RelayURL == "" {
		// Not relay-provisioned: there is no relay-side state to owe a purge of.
		return purgeObligationNotApplicable
	}
	machineRID := ""
	if id, err := machineid.Load(filepath.Join(stateDir, "remote", remoteIdentityFile)); err == nil {
		machineRID = string(relay.RoutingID(id.RelayAuthPublic()))
	}
	store, err := relaypurge.Open(relaypurge.StorePath(stateDir))
	if err == nil {
		err = store.Record(routingID, cfg.RelayURL, machineRID)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "remote revoke: recording the deferred purge FAILED (%v); if the relay "+
			"half of this revocation does not land below, nothing will retry it.\n", err)
		return purgeObligationFailed
	}
	return purgeObligationRecorded
}

// resolvePurgeObligation tombstones the obligation for routingID after a substantive
// relay refusal: Pending no longer gates on it, the reason stays on file.
func resolvePurgeObligation(stateDir, routingID string, reason error, stderr io.Writer) bool {
	if routingID == "" || stateDir == "" {
		return true
	}
	store, err := relaypurge.Open(relaypurge.StorePath(stateDir))
	if err == nil {
		err = store.Resolve(routingID, fmt.Sprintf("%v", reason))
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "remote revoke: recording the refusal failed: %v\n", err)
		return false
	}
	return true
}

// retirePurgeObligation settles the obligation for routingID after an acknowledged
// (or moot) relay purge. Best-effort: a failure leaves a stale obligation the next
// drive's live-pairing or not-authorized handling resolves, and is still reported.
func retirePurgeObligation(stateDir, routingID string, stderr io.Writer) {
	if routingID == "" || stateDir == "" {
		return
	}
	store, err := relaypurge.Open(relaypurge.StorePath(stateDir))
	if err == nil {
		err = store.Retire(routingID)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "remote revoke: retiring the settled purge obligation failed: %v\n", err)
	}
}

// reportDeferredPurge is the one honest sentence the PENDING arm owes: what the
// handset keeps, and what will retry. The two drive sites that exist are named --
// `swarm remote pair` (which refuses to proceed while a purge is owed) and a later
// `swarm remote revoke` that reaches the relay. A supervised gateway start is NOT
// claimed: after a full revoke the unit is booted out and never starts (round-2
// review R2-3), and a paired machine's drive never dials.
func reportDeferredPurge(routingID string, obligated bool, stderr io.Writer) {
	if obligated {
		_, _ = fmt.Fprintf(stderr, "remote revoke: until that purge lands the handset keeps its relay mailbox, "+
			"its push wake and its route (routing id %s). The purge is recorded durably and is driven on this "+
			"machine's next relay dial: `swarm remote pair` drives it (and refuses to proceed until it "+
			"lands), and so does a later `swarm remote revoke` that reaches the relay.\n", routingID)
		return
	}
	_, _ = fmt.Fprintf(stderr, "remote revoke: until that purge lands the handset keeps its relay mailbox, its "+
		"push wake and its route (routing id %s). No deferral is recorded for it, and this verb cannot "+
		"re-address the device: the local record naming that routing id is already gone.\n", routingID)
}

// driveRelayPurgeObligations drives every deferred relay purge (ADR-007 D9, SH5)
// through the ONE shared machine-side driver -- relaypurge.DriveMachineObligations
// carries every ruling (live-pairing guard, paired-machine-never-dials, relay
// mismatch, not-provisioned, answered-vs-transient) so every drive site, present and
// future (bead agents-tracker-x1en), shares one classification. Returns how many purges are STILL owed;
// runRemotePair refuses to proceed while that is nonzero.
func driveRelayPurgeObligations(stateDir string, stderr io.Writer) (pendingLeft int) {
	return relaypurge.DriveMachineObligations(stateDir, func(format string, args ...any) {
		_, _ = fmt.Fprintf(stderr, "remote: "+format+".\n", args...)
	})
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
// running. A missing record is reported rather than guessed at -- callers do nothing
// at all rather than address the relay with something invented. A registry READ
// ERROR is distinct from absence (round-3 codex #1): collapsing it into "not found"
// let a corrupt registry turn a revoke into a false exit-0 success with no purge
// attempted and no obligation recorded.
func deviceRecord(stateDir, deviceID string) (device.Record, bool) {
	rec, ok, err := deviceRecordErr(stateDir, deviceID)
	return rec, ok && err == nil
}

func deviceRecordErr(stateDir, deviceID string) (device.Record, bool, error) {
	if stateDir == "" {
		return device.Record{}, false, nil
	}
	reg, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		return device.Record{}, false, err
	}
	rec, ok := reg.Get(deviceID)
	return rec, ok, nil
}

// deviceRoutingID is the relay mailbox address the machine acts on for a device --
// DERIVED from the device's relay-auth public key, never read from rec.RoutingID: the
// stored field is copied verbatim from the handset's enrollment payload and the
// gateway's C5 re-audit already calls it "self-reported (unverifiable)", deriving its
// own route the same way as here. A purge or a live-pairing guard keyed on a
// self-reported value can be steered by the value's author (SH5 review, codex #5);
// relay.RoutingID(RelayAuthPub) is what the relay itself keys the mailbox by.
func deviceRoutingID(stateDir, deviceID string) string {
	rec, ok := deviceRecord(stateDir, deviceID)
	if !ok || len(rec.RelayAuthPub) == 0 {
		return ""
	}
	return string(relay.RoutingID(ed25519.PublicKey(rec.RelayAuthPub)))
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
// The SH5 deferred-purge drive preserves this: relaypurge.DriveMachineObligations
// refuses to dial while any device is registered (paired-machine-never-dials), so no
// drive path opens a second connection under a live gateway's identity.
//
// The connection is short-lived on purpose. The relay supersedes an older connection for
// the same routing id, so a long-lived CLI client would sever a gateway that came up
// underneath it.
func withMachineRelay(stateDir string, fn func(context.Context, *relay.Client) error) error {
	if stateDir == "" {
		return errRelayNotProvisioned
	}
	cfg, found, err := relaycfg.Load(stateDir)
	if err != nil {
		return err
	}
	idPath := filepath.Join(stateDir, "remote", remoteIdentityFile)
	if !found || cfg.RelayURL == "" || !statFileExists(idPath) {
		return errRelayNotProvisioned
	}
	// The same transport policy the gateway sidecar dials under, resolved from the same
	// relay.json: this connection carries the same machine relay-auth key in the same
	// auth_init frame, and it is the connection that carries a REVOCATION (ADR-007
	// B34/B37). A malformed pin stops it here rather than mid-verb.
	sec, err := cfg.Security()
	if err != nil {
		return err
	}
	id, err := machineid.Load(idPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), remoteRelayOpTimeout)
	defer cancel()
	// NO ClientAuth.Peer, AND THAT IS A DECISION RATHER THAN AN OMISSION (ADR-007 B49).
	//
	// Peer asks the relay "has this counterparty revoked me?", and the answer is a refused
	// handshake. The machine must never ask, because it is the only party that can perform a
	// recovery: `swarm remote pair` and `swarm remote revoke` both run over this connection,
	// and a machine refused here cannot re-pair, which is precisely the mutual assured
	// destruction B49 measured -- a stolen handset's revoke removed the machine's relay
	// identity for good.
	//
	// Nothing is lost by not asking. No legitimate flow revokes a machine at the relay: this
	// CLI is the only production caller of the verb, and the phone's own RevokeThisDevice
	// rides the sealed command plane to the gateway instead. A ban standing against this
	// machine is therefore an attacker's, and the owner sitting at this terminal does not
	// need the relay to tell them their machine is theirs.
	cl, err := relay.DialSecure(ctx, cfg.RelayURL, relay.ClientAuth{
		RelayAuthPub: id.RelayAuthPublic(),
		Sign:         func(challenge []byte) ([]byte, error) { return id.RelayAuthSign(challenge), nil },
	}, sec)
	if err != nil {
		return err
	}
	defer func() { _ = cl.Close() }()
	return fn(ctx, cl)
}

// remoteRelayOpTimeout bounds the relay round trips these verbs make. The owner is
// sitting at a terminal and the local half of the work is already durable, so a relay
// that is down must cost them a reported delay and not a hang.
const remoteRelayOpTimeout = 10 * time.Second

// relayPurgeVerdict is what the relay half of a revocation actually did. ADR-007 D9
// blesses a DEFERRED purge for a machine that is offline at revoke time, so "done" and
// "not done yet" are both legitimate states -- and only one of them means the handset is
// locked out now, which is why the operator is told which one they got.
type relayPurgeVerdict int

const (
	// relayPurgeNone: this machine holds no relay-side state for that device -- no relay
	// provisioned, no routing id, or a relay that says it has no such pairing -- so there
	// was nothing to purge and nothing to report.
	relayPurgeNone relayPurgeVerdict = iota
	// relayPurgeDone: the relay acknowledged the de-authorization and the purge.
	relayPurgeDone
	// relayPurgeRefused: the relay answered, and the answer was a refusal.
	relayPurgeRefused
	// relayPurgePending: the relay was never reached or never answered, so the purge is
	// deferred and the handset still holds everything it held.
	relayPurgePending
	// relayPurgeUnprovisioned: this machine holds no relay identity, so it could not
	// even DIAL. Distinct from relayPurgeNone (round-3 codex #2): with an obligation
	// on file the machine WAS provisioned at revoke time and the owed relay still
	// holds the device's state -- exit 0 with a silent retire would be a false claim.
	relayPurgeUnprovisioned
)

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
//
// IT REPORTS ITS OUTCOME RATHER THAN SWALLOWING IT (ADR-007 B120 F3): the caller's exit
// code is a claim about this call, so this call has to answer whether the relay agreed.
func purgeRelayState(stateDir, routingID string) (relayPurgeVerdict, error) {
	if routingID == "" {
		return relayPurgeNone, nil
	}
	presented := false
	err := withMachineRelay(stateDir, func(ctx context.Context, cl *relay.Client) error {
		// presented marks that the DIAL (auth included) succeeded and the purge op
		// itself went out: an ANSWER received before this point -- an auth_init
		// refusal, a revoked machine registration -- is not the relay refusing the
		// PURGE, and must not read as one (round-3 codex #4).
		presented = true
		return cl.DeviceRevoke(ctx, routingID)
	})
	switch {
	case err == nil:
		return relayPurgeDone, nil
	case errors.Is(err, errRelayNotProvisioned):
		return relayPurgeUnprovisioned, err
	case presented && errors.Is(err, relay.ErrNotAuthorized):
		return relayPurgeNone, nil
	case errors.Is(err, relay.ErrQuotaExceeded), errors.Is(err, relay.ErrDuplicateConnection):
		// Answers that clear BY THEMSELVES -- a rate window lapses, a superseding
		// connection ends -- so treating them as refusals abandoned a purge a
		// minute's patience delivers (SH5 review). The gateway's own outage
		// classifier draws the same line (cmd/swarm-remote).
		return relayPurgePending, err
	case presented && errors.Is(err, relay.ErrRelayAnswered):
		// The relay ANSWERED THE PURGE, and the answer was no. Substantive is an
		// ALLOWLIST anchored on the one place answers are decoded
		// (relay.ErrRelayAnswered, SH5 round-3 F1) AND on the purge having been
		// presented -- a reply that failed to DECODE, or an answer to the handshake
		// rather than to device_revoke, falls to pending, never to a permanent
		// refusal.
		return relayPurgeRefused, err
	default:
		return relayPurgePending, err
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
		_, _ = fmt.Fprintf(stderr, "remote pair: the device is paired, but the machine could not open its "+
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
		_, _ = fmt.Fprintf(stderr, "remote revoke: the device is revoked, but the machine still holds "+
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
		_, _ = fmt.Fprint(stderr, remoteRegrantUsage)
		return 2
	}
	deviceID := args[0]

	client, err := dialClient([]string{protocol.CapPairing})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "remote regrant: %v\n", err)
		return 1
	}
	defer func() { _ = client.Close() }()

	if err := client.RegrantDevice(deviceID); err != nil {
		_, _ = fmt.Fprintf(stderr, "remote regrant: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "re-granted device %s\n", deviceID)
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
		_, _ = fmt.Fprintf(stderr, "remote regrant: the grant was re-issued, but its gateway was not "+
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

	// SH5 (ADR-007 D9): pairing IS the machine's next relay connection, so any purge an
	// offline-at-revoke run deferred is driven first -- the ban lands before the new
	// ceremony, and authorizeAtRelay lifts it for the device being granted now. A purge
	// still owed after the drive REFUSES the pairing (review codex #4): enrolling a
	// replacement while the revoked route lives would invert that ordering, and the
	// replacement's own live routing id would then shield the stale mailbox forever.
	if left := driveRelayPurgeObligations(remoteStateDir(), stderr); left > 0 {
		_, _ = fmt.Fprintf(stderr, "remote pair: %d deferred relay purge(s) from an earlier revoke are still "+
			"owed and could not be driven now (see above). Pairing would grant new authority before the old "+
			"grant is provably gone, so it is refused; re-run once the relay is reachable (a rate-limited "+
			"answer clears within a minute).\n", left)
		return 1
	}

	client, err := dialClient([]string{protocol.CapPairing})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "remote pair: %v\n", err)
		return 1
	}
	defer func() { _ = client.Close() }()

	sess, err := client.StartPairing(protocol.PairStartReq{Capability: *capability})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "remote pair: %v\n", err)
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
	_, _ = fmt.Fprintf(stdout, "rendezvous: %s\n", sess.RendezvousID)
	if sess.ExpiresAt != nil {
		_, _ = fmt.Fprintf(stdout, "expires: %s\n", sess.ExpiresAt.Format(timeFormat))
	}
	// The relay address, SPELLED (agents-tracker-3fkm): the phone's first-run prompt asks
	// for it once, and it otherwise lives only inside the payload and the symbol -- places a
	// person typing a ten-character code cannot read it from. Skipped when the daemon's QR
	// is not a decodable payload (an older daemon, a scripted test host): a line this verb
	// cannot vouch for is worse than no line.
	if qp, err := pairing.DecodeQR(sess.QR); err == nil {
		_, _ = fmt.Fprintf(stdout, "relay: %s\n", qp.RelayURL)
	}

	// The PNG spelling of the same symbol (F3, ADR-007 B141). BEST-EFFORT: a failure to
	// write an auxiliary artifact must not cost the pairing -- the terminal symbol and the
	// codes still print. The remove runs on EVERY exit of this verb: the file carries the
	// pairing secret and must not outlive the ceremony on disk.
	pngPath := ""
	if stateDir := resolveStateDir(); stateDir != "" {
		remoteDir := filepath.Join(stateDir, "remote")
		if err := os.MkdirAll(remoteDir, 0o700); err == nil {
			if p, err := writePairingPNG(sess.QR, remoteDir, sess.RendezvousID); err == nil {
				pngPath = p
				defer func() { _ = os.Remove(p) }()
			} else {
				_, _ = fmt.Fprintf(stderr, "remote pair: QR image not written (%v); the terminal symbol and codes still work\n", err)
			}
		}
	}
	printPairingQR(stdout, sess.ShortCode, pngPath, sess.QR)

	// Block until the phone reaches the SAS gate. A terminal result arriving FIRST (a
	// rendezvous/TTL failure or a dropped session, before any gate) unblocks here fail
	// closed rather than hanging.
	//
	// AND THE EXPIRY IS A DEADLINE, NOT A DECORATION (ADR-007 B46). This wait used to have
	// none of its own: the command printed `expires:` and then waited past it forever, so a
	// QR the relay had already dropped stayed on screen as though it still worked. It does
	// not -- the rendezvous slot is purged and its id burned at the relay -- and a phone
	// scanning after that point cannot reach this machine at all. Stopping when the printed
	// window closes is what makes the printed window true.
	var expired <-chan time.Time
	if sess.ExpiresAt != nil {
		timer := time.NewTimer(time.Until(*sess.ExpiresAt))
		defer timer.Stop()
		expired = timer.C
	}
	var pending protocol.PairingPending
	select {
	case pending = <-sess.Pending():
	case <-sess.Result():
		_, _ = fmt.Fprintln(stdout) // terminate printPairingQR's last, deliberately unterminated row
		_, _ = fmt.Fprintln(stderr, "remote pair: pairing ended before the device connected")
		return 1
	case <-expired:
		_, _ = fmt.Fprintln(stdout) // terminate printPairingQR's last, deliberately unterminated row
		_, _ = fmt.Fprintln(stderr, "remote pair: the pairing window closed before the device connected; "+
			"the code above is dead, so run `swarm remote pair` again for a fresh one")
		return 1
	}

	// The scan is over: the phone has connected, so the symbol may now be displaced. This
	// newline is the one printPairingQR left off its last row — spending it here rather
	// than there is what lets the symbol have the whole viewport (see printPairingQR).
	_, _ = fmt.Fprintln(stdout)

	// The independent second gate (ADR D3): the operator verifies the SAS emoji against
	// the phone's screen and allows or denies at the desktop.
	_, _ = fmt.Fprintf(stdout, "Device: %s\n", pending.DeviceName)
	// sonnet#4: echo the capability tier being granted so the operator sees the authority
	// they are about to hand this device (default "full") before allowing -- the SAS proves
	// WHICH phone, this line proves WHAT it may do.
	_, _ = fmt.Fprintf(stdout, "Capability to grant: %s\n", *capability)
	_, _ = fmt.Fprintf(stdout, "Verify these emoji match your phone: %s\n", strings.Join(pending.SAS, " "))
	_, _ = fmt.Fprint(stdout, "Allow this device? [y/N]: ")

	allow := readYesNo(stdin)
	if err := sess.Confirm(allow); err != nil {
		_, _ = fmt.Fprintf(stderr, "remote pair: %v\n", err)
		return 1
	}

	// The single terminal outcome: a real pair_result, or a fail-closed non-paired result
	// on a dropped session / Close.
	res := <-sess.Result()
	if !res.Paired {
		// The operator's own answer is authoritative: if they denied, this is a decline
		// whatever the daemon managed to attribute, and a decline is the SAS gate doing its
		// job rather than a malfunction.
		cause := res.Failure
		if !allow {
			cause = protocol.PairFailDeclined
		}
		reportPairFailure(cause, stdout, stderr)
		return 1
	}
	name := res.Name
	if name == "" {
		name = res.DeviceID
	}
	_, _ = fmt.Fprintf(stdout, "paired %s\n", name)

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

// pairFailureLines is what the operator is told for each cause (ADR-007 B71(1)). Every
// line names WHAT happened and WHAT to do next, because the audience is someone standing
// at a handset during a closed test with a phone that did not pair.
//
// The table is keyed on the protocol constants and renders NOTHING off the wire: the
// pairing path parses attacker-influenced bytes, and protocol normalises any cause it
// does not recognise to PairFailInternal, so an unknown code lands on the last row rather
// than reaching a terminal.
var pairFailureLines = map[protocol.PairFailure]string{
	protocol.PairFailConfirmTimeout: "remote pair: nobody answered the confirmation prompt in time, so the pairing " +
		"failed closed; nothing was paired. Run `swarm remote pair` again.",
	protocol.PairFailWindowClosed: "remote pair: the pairing window closed before the handshake finished; the code " +
		"is dead. Run `swarm remote pair` again for a fresh one.",
	protocol.PairFailSessionClosed: "remote pair: the pairing session ended before the device finished; nothing " +
		"was paired. Run `swarm remote pair` again.",
	protocol.PairFailConnectionLost: "remote pair: lost the connection to the daemon mid-pairing; nothing was " +
		"paired. Check the daemon is still running, then pair again.",
	protocol.PairFailRateLimited: "remote pair: too many pairing attempts, so this one was refused; nothing was " +
		"paired. Wait a little and run `swarm remote pair` again.",
	protocol.PairFailCodeSpent: "remote pair: that pairing code had already been used once and each one is " +
		"single-use; nothing was paired. Run `swarm remote pair` again for a fresh code.",
	protocol.PairFailHeadless: "remote pair: this machine refuses to pair without a local console; the " +
		"confirmation has to happen at the machine itself.",
	protocol.PairFailNoConsent: "remote pair: the phone never released its relay-route consent, so the pairing " +
		"was abandoned and nothing was paired. Retry the scan from the phone.",
	// The ONE cause where the two ends can disagree about what happened, so it is the one
	// line that must contradict the handset out loud. The device pins on the acceptance it
	// received; this machine deliberately claims nothing when that acceptance is never
	// acknowledged (PB-PAIR-4), so the owner may be reading "paired" on a phone while
	// standing at a terminal that enrolled no device. Naming the phone is the point: an
	// owner who is not told will go hunting for a revoke, and there is nothing to revoke.
	protocol.PairFailAcceptUnacknowledged: "remote pair: the phone never confirmed it received the " +
		"acceptance, so this machine paired NOTHING -- even if the phone now shows it is paired. " +
		"Nothing needs revoking and the slot is free. Run `swarm remote pair` again.",
	protocol.PairFailInternal: "remote pair: pairing failed and the daemon did not report a cause; nothing was " +
		"paired. Check the daemon log for the underlying error, then run `swarm remote pair` again.",
}

// reportPairFailure prints the terminal outcome for a pairing that enrolled nothing.
//
// A DECLINE goes to stdout with the rest of the ceremony and carries no failure
// vocabulary: the operator answering "no" at the SAS gate is the gate working, which is
// the whole reason the gate exists. Everything else is a diagnostic and goes to stderr.
func reportPairFailure(cause protocol.PairFailure, stdout, stderr io.Writer) {
	if cause == protocol.PairFailDeclined {
		_, _ = fmt.Fprintln(stdout, "pairing declined")
		return
	}
	line, ok := pairFailureLines[cause]
	if !ok {
		line = pairFailureLines[protocol.PairFailInternal]
	}
	_, _ = fmt.Fprintln(stderr, line)
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
		_, _ = fmt.Fprintf(stderr, "remote %s: the gateway was not started: %v\n", verb, err)
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
		_, _ = fmt.Fprintf(stderr, "remote %s: nothing is serving the remote socket at %s, so the gateway "+
			"cannot reach the daemon and will be restarted until it can. The running daemon chose "+
			"its listener before this machine was provisioned for remote -- run `swarm daemon "+
			"restart` to pick it up.\n", verb, sock)
	}
	sup, err := newGatewaySupervisor(stateDir)
	if err != nil {
		warn(err)
		return
	}
	restampGatewayUnit(verb, stateDir, sup, stderr)
	switch err := sup.Ensure(); {
	case err == nil:
	case errors.Is(err, supervise.ErrNotInstalled):
		_, _ = fmt.Fprintf(stderr, "remote %s: this machine has no gateway supervision unit, so "+
			"the paired device will receive nothing. Run `swarm remote init` to install one.\n", verb)
	default:
		warn(err)
	}
}

// restampGatewayUnit repairs a unit that names a program which is no longer there, BEFORE the
// supervisor is asked to start it.
//
// THE OUTAGE IT ENDS (agents-tracker-nx44.4, 2026-08-09). A unit stamped by an older release
// named that release's staged gateway; `brew upgrade` deleted the directory; launchd went on
// exec'ing the path, the job exited EX_CONFIG (78) on every restart until the label sat in the
// penalty box, and `swarm remote pair` kickstarted that same stale label and reported success.
// resolveGatewayBinary now prefers the version-stable link, which fixes the NEXT install and
// does nothing for the machines already carrying a versioned unit -- and no command re-stamped
// one: init installs, pair only ensures, and pairing is refused while a device is enrolled.
//
// IT IS THE PATH THAT IS CHECKED, not the file's age or its contents: a unit is a claim about
// a program, and the only claim this can settle is whether that program is still there to run.
// A unit naming a healthy gateway is LEFT ALONE, and that restraint is the point -- Ensure
// never restarts a running gateway, and a bootout on every pair would drop the connection of
// the phone being paired.
//
// AND THE RELOAD IS PART OF IT. launchd holds the plist it was bootstrapped with (bootstrap on
// a loaded label is a no-op, which is why supervisor.go ignores its error), so a fresh file
// alone changes nothing about the running job. Stop-then-Ensure is the bootout/bootstrap pair
// by hand, expressed through the seam the CLI already has.
//
// Every failure is a warning, for ensureGatewayRunning's reason: the enrollment is durable and
// already committed, and the operator is told rather than handed an exit status.
func restampGatewayUnit(verb, stateDir string, sup supervise.Supervisor, stderr io.Writer) {
	exe, err := supervise.InstalledExec(stateDir)
	switch {
	case errors.Is(err, supervise.ErrNotInstalled):
		// No unit to check; the caller's own ErrNotInstalled hint covers that machine.
		return
	case err != nil:
		// StampedExec FAILS LOUDLY BY CONTRACT, and this was the one place that swallowed it
		// (agents-tracker-2pnu F6). Its KDoc names this exact case -- "the day this parse stops
		// matching the templates it would silently re-stamp and reload every unit on every
		// pair, which is the opposite of what a check exists for" -- and the collapsed
		// `err != nil` above made the opposite mistake instead: a unit that is right there and
		// does not parse (a hand-edit, a truncated write, a template this build no longer
		// matches) was indistinguishable from a healthy one, so the check said nothing.
		//
		// IT REPORTS AND DOES NOT RE-STAMP. The claim this repairs is "the stamped program is
		// gone", and a file that cannot be read makes no claim to check -- booting out a job on
		// a read failure would drop the connection of the phone being paired, which is the
		// restraint the healthy-unit case already keeps.
		_, _ = fmt.Fprintf(stderr, "remote %s: this machine's gateway supervision unit could not be "+
			"read, so whether it still names a program that exists is unknown -- if the gateway "+
			"is not reaching your phone, run `swarm remote init` to re-stamp it: %v\n", verb, err)
		return
	case isExecutableFile(exe):
		// The unit names a program that is right there.
		return
	}
	_, _ = fmt.Fprintf(stderr, "remote %s: the gateway supervision unit names %s, which is not an "+
		"executable file any more -- an upgrade that moved it leaves the supervisor exec'ing a "+
		"path that is gone, which is a gateway that never starts. Re-stamping the unit and "+
		"reloading it.\n", verb, exe)
	if !installGatewayUnit(stateDir, stderr) {
		return
	}
	if err := sup.Stop(); err != nil && !errors.Is(err, supervise.ErrNotInstalled) {
		_, _ = fmt.Fprintf(stderr, "remote %s: the re-stamped unit was written, but the old job was not "+
			"stopped, so the supervisor may still be holding the unit it loaded first: %v\n", verb, err)
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
		_, _ = fmt.Fprintf(stderr, "remote revoke: the device is revoked, but its gateway was not "+
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
// resolveStateDir mirrors every other remote verb's resolution: the env override, then the
// platform default. Empty means no writable home; the caller treats that as "skip the
// artifact", never as a failed pairing.
func resolveStateDir() string {
	if dir := os.Getenv(daemon.EnvStateDir); dir != "" {
		return dir
	}
	dir, err := persist.DefaultDir()
	if err != nil {
		return ""
	}
	return dir
}

func printPairingQR(stdout io.Writer, shortCode, pngPath, payload string) {
	cols, rows := terminalBox()
	// The short code leads on BOTH paths (ADR-007 B140): it is the one spelling a human can
	// carry to the phone by reading it, which is what the owner asked for after the
	// 133-character payload ("not possibly written by a human", agents-tracker-tr0n). Empty
	// from a daemon that predates it, and then this line does not print -- a prompt with
	// nothing to type is worse than the old output.
	if shortCode != "" {
		_, _ = fmt.Fprintf(stdout, "Type this code on your phone to pair: %s\n", shortCode)
	}
	// The image is the PROMISED scan target (F3): the terminal symbol depends on font
	// metrics this product does not control. ABOVE the symbol, like everything else -- a
	// row printed after it scrolls its finder patterns off a 24-row screen.
	if pngPath != "" {
		_, _ = fmt.Fprintf(stdout, "Or scan the QR image at: %s\n", pngPath)
	}
	if r, err := renderPairingQR(payload, cols, rows); err == nil {
		// The payload stays available for manual entry (PB-PAIR-2), WRAPPED to the terminal
		// width — a line long enough to reflow would displace the symbol — and printed
		// ABOVE it, where it costs the symbol no rows.
		_, _ = fmt.Fprintln(stdout, "Or paste this full code:")
		for _, line := range chunkLines(payload, cols) {
			_, _ = fmt.Fprintln(stdout, line)
		}
		_, _ = fmt.Fprintln(stdout, "Scan this QR on your phone to pair:")
		_, _ = fmt.Fprint(stdout, r.Text)
		return
	}
	_, _ = fmt.Fprintln(stdout, qrFallbackReason(payload, cols, rows))
	_, _ = fmt.Fprintln(stdout, "Or paste this full code:")
	// UNWRAPPED here: there is no symbol above to protect, and manual entry wants one
	// unbroken token to read or copy.
	_, _ = fmt.Fprintln(stdout, payload)
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
			_, _ = fmt.Fprintf(stderr, "remote status: %v\n", err)
			return 1
		}
	}

	// 1. Configuration presence (machine identity + relay), both under <stateDir>/remote/.
	remoteDir := filepath.Join(stateDir, "remote")
	hasIdentity := statFileExists(filepath.Join(remoteDir, remoteIdentityFile))
	hasRelay := statFileExists(filepath.Join(remoteDir, remoteRelayFile))
	switch {
	case hasIdentity && hasRelay:
		_, _ = fmt.Fprintln(stdout, "configuration: initialized (identity + relay)")
	case hasIdentity:
		_, _ = fmt.Fprintln(stdout, "configuration: initialized (identity; no relay configured)")
	default:
		_, _ = fmt.Fprintln(stdout, "configuration: not initialized (run swarm remote init)")
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
		_, _ = fmt.Fprintln(stdout, "remote control: OFF (manual override)")
	case listErr != nil:
		_, _ = fmt.Fprintln(stdout, "remote control: unknown (daemon unreachable)")
	case len(devices) > 0:
		_, _ = fmt.Fprintln(stdout, "remote control: ON (device-derived)")
	default:
		_, _ = fmt.Fprintln(stdout, "remote control: OFF (device-derived; no devices paired)")
	}

	// The deferred-purge ledger reads LOCAL state, so it prints before the roster's
	// daemon-unreachable early return (post-commit codex #3): owed purges and refusal
	// tombstones must not disappear exactly when the daemon is down.
	reportRelayPurgeState(stateDir, stdout)

	// Roster.
	if listErr != nil {
		_, _ = fmt.Fprintf(stdout, "paired devices: unavailable (%v)\n", listErr)
		return 0
	}
	_, _ = fmt.Fprintf(stdout, "paired devices (%d):\n", len(devices))
	for _, d := range devices {
		_, _ = fmt.Fprintf(stdout, "  %s  %s\n", d.DeviceID, d.Name)
	}
	return 0
}

// reportRelayPurgeState surfaces the deferred-purge ledger (SH5): purges still owed,
// and refused-purge tombstones -- relay-side device state a revoke could NOT remove,
// which the owner would otherwise learn about only from a stderr line that scrolled
// away at revoke time. Best-effort: status is a read that must never fail the verb.
func reportRelayPurgeState(stateDir string, stdout io.Writer) {
	store, err := relaypurge.Open(relaypurge.StorePath(stateDir))
	if err != nil {
		_, _ = fmt.Fprintf(stdout, "deferred relay purges: unavailable (%v)\n", err)
		return
	}
	pending, perr := store.Pending()
	resolved, rerr := store.Resolved()
	if perr != nil || rerr != nil || (len(pending) == 0 && len(resolved) == 0) {
		return
	}
	for _, ob := range pending {
		_, _ = fmt.Fprintf(stdout, "deferred relay purge OWED: routing id %s at %s (recorded %s); "+
			"driven on the next relay dial\n",
			ob.RoutingID, ob.RelayURL, ob.RecordedAt.Format(timeFormat))
	}
	for _, ob := range resolved {
		_, _ = fmt.Fprintf(stdout, "relay purge REFUSED: routing id %s at %s -- %s; that relay still "+
			"holds the device's mailbox, push wake and route (manual cleanup)\n",
			ob.RoutingID, ob.RelayURL, ob.Refusal)
	}
}

// statusListDevices dials the owner daemon (CapPairing, like runRemoteDevices) and
// returns the paired-device roster. Any dial or list failure is returned so status can
// report the roster as unavailable rather than crash.
func statusListDevices() ([]protocol.DeviceView, error) {
	client, err := dialClient([]string{protocol.CapPairing})
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()
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
