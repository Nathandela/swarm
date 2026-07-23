package protocol

// FAILING-FIRST protocol test for the A5 cross-model review finding R2 (slice R2):
// the remote tier must FAIL CLOSED at CONSTRUCTION when a mandatory fail-closed guard is
// absent. take_control's single-use (OperationClaimer) and the global kill switch
// (KillSwitch) were consulted with `if impl, ok := cc.srv.d.(X); ok && !allowed` — a
// MISSING backend read as "allowed" (fail-OPEN), so a misassembled remote server (e.g. a
// DaemonAPI adapter that forwards DeviceAuthenticator but drops OperationClaimer/KillSwitch)
// would silently grant authorized-but-replayable-and-unkillable control. DeviceAuthenticator
// already fails closed (requireRemoteAuthz); these must too.
//
// The fix enforces the guard once at construction: ServeRemoteWithID must return an ERROR
// (and never start the listener) when the backend does not implement BOTH OperationClaimer
// and KillSwitch. This is the cleanest correct form — it covers every remote mutating op's
// kill-switch dependency, not just take_control.
//
// RED today: ServeRemoteWithID performs no such check, so it constructs a server on a
// guard-missing backend and returns a nil error — the assertion below fails until the
// construction guard lands.

import "testing"

// The fixtures below implement DeviceAuthenticator but OMIT exactly one mandatory remote-tier
// guard. Each embeds the DaemonAPI INTERFACE (not the concrete *stubDaemon), so no optional
// interface promotes from the backend; only the interfaces named are added back, making the
// omission exact — this is precisely the "adapter that forwards some optional interfaces but
// not others" misassembly R2 warns about.

// authNoClaimer implements DaemonAPI + DeviceAuthenticator + KillSwitch but NOT
// OperationClaimer: the remote server could authorize take_control yet not make it single-use.
type authNoClaimer struct{ DaemonAPI }

func (authNoClaimer) AuthorizeCommand(DeviceCommandAuth) error { return nil }
func (authNoClaimer) RemoteControlEnabled() bool               { return true }

// authNoKillSwitch implements DaemonAPI + DeviceAuthenticator + OperationClaimer but NOT
// KillSwitch: the remote server could grant control it has no global switch to halt.
type authNoKillSwitch struct{ DaemonAPI }

func (authNoKillSwitch) AuthorizeCommand(DeviceCommandAuth) error    { return nil }
func (authNoKillSwitch) ClaimOperation(_, _, _ string) (bool, error) { return false, nil }

// TestProtocol_RemoteTierRefusesConstructionWithoutGuards pins the fail-closed construction
// guard (R2): a remote-tier Server built on a backend missing either OperationClaimer or
// KillSwitch must NOT be created — ServeRemoteWithID returns an error and starts no listener.
func TestProtocol_RemoteTierRefusesConstructionWithoutGuards(t *testing.T) {
	cases := []struct {
		name    string
		backend DaemonAPI
	}{
		{"missing OperationClaimer", authNoClaimer{DaemonAPI: newStubDaemon()}},
		{"missing KillSwitch", authNoKillSwitch{DaemonAPI: newStubDaemon()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sock := tmpSock(t)
			srv, err := ServeRemoteWithID(tc.backend, sock, "ep")
			if err == nil {
				if srv != nil {
					_ = srv.Close()
				}
				t.Fatalf("ServeRemoteWithID constructed a remote-tier server on a backend %s; "+
					"want a fail-closed error (a remote server must not serve control it cannot "+
					"make single-use or cannot kill)", tc.name)
			}
			if srv != nil {
				_ = srv.Close()
				t.Fatalf("ServeRemoteWithID returned a non-nil server alongside its fail-closed error; want a nil server (no listener started)")
			}
		})
	}
}

// TestProtocol_RemoteTierConstructsWithAllGuards is the positive control: a backend that
// implements every mandatory remote-tier guard (the full stubDaemon: DeviceAuthenticator +
// KillSwitch + OperationClaimer) constructs successfully, so the fail-closed guard rejects
// only the misassembled cases above.
func TestProtocol_RemoteTierConstructsWithAllGuards(t *testing.T) {
	sock := tmpSock(t)
	srv, err := ServeRemoteWithID(newStubDaemon(), sock, "ep")
	if err != nil {
		t.Fatalf("ServeRemoteWithID on a fully-guarded backend = %v; want success", err)
	}
	_ = srv.Close()
}
