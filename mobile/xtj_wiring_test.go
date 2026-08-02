package swarmmobile_test

// Bead agents-tracker-xtj -- FAILING-FIRST (TDD RED, GG-5) source guards for the two things
// the Machines tab needs and the facade cannot supply today.
//
// WHY SOURCE AND NOT BEHAVIOUR, for the reason S11's wiring guards give: the state machines
// are unit-tested where they live (xtj_presence_test.go), and the way they ship broken is that
// nothing CALLS them. A perfect presence cache the relay loop never feeds is the "requirement
// satisfiable while the defect ships" failure this project has already had. The behavioural
// half needs a paired relay and a machine, which is mobile/conformance's shape.
//
// THE TWO DEFECTS, in the bead's words:
//
//  1. "No bound accessor returns it. The string exists once, in Go (mobile/pairing.go sends
//     DeviceName: "swarm phone"), and no facade verb exposes it. Typing the literal in Kotlin
//     would be a second copy of a Go constant rendered as though the wire carried it --
//     ADR-007 B135's defect class."
//  2. "presence. MachinePane.presence is App.Presence, a BLOCKING relay round-trip."
//
// The device name is NOT a wire fact and this file makes sure the fix does not pretend it is.
// The phone sends it in msg3 and nothing gives it back: pairing.DeviceOutcome carries the SAS,
// the machine's static key and the machine payload, and no device payload; phonecore.State has
// no field for it; the pairing record holds one label from a closed set. Only the machine keeps
// it (internal/remote/enroll/enroll.go:81). So the honest accessor returns the constant this
// side owns -- the WakeNotificationText precedent (pushwake.go:44), whose own doc gives the
// argument: "a second copy in Kotlin is a copy that drifts."
//
// This file contains NO implementation.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestXTJWiring_TheDeviceNameHasExactlyOneSourceInGo is the defect class itself. A verb that
// returned its own copy of the literal would satisfy "a facade verb exposes it" while leaving
// two strings to drift -- the same defect the bead refuses in Kotlin, moved one package down.
func TestXTJWiring_TheDeviceNameHasExactlyOneSourceInGo(t *testing.T) {
	src := loadFacade(t)

	const literal = `"swarm phone"`
	var sites []string
	for _, base := range src.GoFiles {
		b, err := os.ReadFile(filepath.Join(src.Dir, base))
		if err != nil {
			t.Fatalf("read %s: %v", base, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, literal) {
				sites = append(sites, filepath.Base(base)+":"+itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
	}

	if len(sites) != 1 {
		t.Fatalf("the device name literal appears at %d sites, want exactly 1 (the constant declaration):\n\t%s\n\nEvery other site must reference the constant. Two copies of a name the wire never returns is ADR-007 B135's defect class, and moving one of them from Kotlin into Go does not fix it.", len(sites), strings.Join(sites, "\n\t"))
	}
	if !strings.Contains(sites[0], "const ") {
		t.Fatalf("the single device-name literal is not a constant declaration: %s", sites[0])
	}
}

// TestXTJWiring_ThePairingPayloadUsesTheConstantItExposes closes the loop the previous test
// opens: one source is only worth having if the SEND SITE is what reads it. A constant the
// accessor returns while the handshake sends something else is a verb that reports a name the
// machine was never told.
func TestXTJWiring_ThePairingPayloadUsesTheConstantItExposes(t *testing.T) {
	src := loadFacade(t)
	body := s11FuncSource(t, src, "Pairing", "join")
	if !strings.Contains(body, "DeviceName:") {
		t.Fatal("Pairing.join no longer builds a DeviceName; this guard has lost its subject and must be re-aimed rather than deleted")
	}
	if strings.Contains(body, `"swarm phone"`) {
		t.Fatal("Pairing.join still sends the device name as a literal. The accessor and the handshake must read ONE constant, or the verb can report a name the machine was never sent.")
	}
}

// TestXTJWiring_PairedDeviceNameIsGatedOnBeingPaired. An accessor that answered before pairing
// would name a device the machine has never heard of, on a pane whose subject is the machine
// this phone is paired to. ready() is also what PB-BIND-5's reflective robustness test requires
// of every verb: a non-nil error on an unusable receiver.
func TestXTJWiring_PairedDeviceNameIsGatedOnBeingPaired(t *testing.T) {
	src := loadFacade(t)
	body := s11FuncSource(t, src, "App", "PairedDeviceName")
	s11RequireCalls(t, s11FuncLabel("App", "PairedDeviceName"), body, map[string]string{
		"ready": "the name is what this phone told a machine at pairing; before there is a pairing there is nothing to report, and PB-BIND-5 requires a real error on an unusable receiver",
	})
}

// TestXTJWiring_MachinePresenceReadsTheCacheAndNeverTheRelay is the bead's actual requirement.
// A verb that consulted the relay would be App.Presence under another name, and the ledger row
// this fix exists to answer would apply to it word for word.
func TestXTJWiring_MachinePresenceReadsTheCacheAndNeverTheRelay(t *testing.T) {
	src := loadFacade(t)
	body := s11FuncSource(t, src, "App", "MachinePresence")

	for _, banned := range []string{".Presence(", "a.conn()", "roundtrip"} {
		if strings.Contains(body, banned) {
			t.Fatalf("App.MachinePresence calls %s. It must be an O(1) locked read of the cache: this is the verb a screen calls per render, and a relay round-trip here is the defect the bead describes, with a 10 s DefaultCallTimeout behind it on the main thread.", banned)
		}
	}
	s11RequireCalls(t, s11FuncLabel("App", "MachinePresence"), body, map[string]string{
		"read": "the cached reading and its instant come from the presence cache the relay goroutine feeds",
	})
}

// TestXTJWiring_TheRelayLoopDrivesThePresenceCadence is the reachability half. The cache is
// fed from the connection's own lifetime -- started once the link is up, ended when the drain
// returns -- so the poll cannot outlive the client it polls through, and a screen never drives
// a relay op from a draw call.
func TestXTJWiring_TheRelayLoopDrivesThePresenceCadence(t *testing.T) {
	src := loadFacade(t)
	body := s11FuncSource(t, src, "App", "run")
	if !strings.Contains(body, "pollPresence") {
		t.Fatal("App.run never starts the presence poll. A cache nothing feeds reads \"unknown\" forever, which is a Machines tab that renders nothing -- the bug this bead is about, one layer down.")
	}
}

// TestXTJWiring_TheLiveRoundTripSurvivesForTheLeakTest. App.Presence must NOT be quietly
// converted into a cached read. mobile/conformance/pbnet7_leak_test.go calls it to retire a
// USED connection -- "this is the request/reply path, the one that holds the exchange lock and
// now carries a deadline of its own". A cached Presence() leaves that test passing while it
// stops testing anything, which is worse than failing it.
func TestXTJWiring_TheLiveRoundTripSurvivesForTheLeakTest(t *testing.T) {
	src := loadFacade(t)
	body := s11FuncSource(t, src, "App", "Presence")
	if !strings.Contains(body, ".Presence(") {
		t.Fatal("App.Presence no longer performs the relay round-trip. PB-NET-7's connection-leak test uses it as its real exchange; without it that test retires an idle connection and still passes.")
	}
}

// itoa is strconv.Itoa without the import, kept local so this file's import list stays the
// three the guards actually need.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
