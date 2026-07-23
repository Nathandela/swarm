package skeleton

// Failing-first test for R-GW.8: the daemon assembly stands up the dedicated
// remote-tier socket (the amendment D.0-A1 remote.sock the gateway dials), distinct
// from the owner-trusted main UDS, when a RemoteSocketPath is configured. Every
// connection on it is remote-tier, so a mutating op is authorized against the pinned
// device registry (R-POL.9) before any action -- and with no paired devices it is
// refused, fail-closed. The main socket is unaffected (owner tier, no device auth).
//
// RED is undefined-only: Config has no RemoteSocketPath field yet.

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// assembleWithRemote stands up the full assembly with a configured remote.sock and
// returns the daemon plus the remote socket path.
func assembleWithRemote(t *testing.T) (*Daemon, string) {
	t.Helper()
	buildBinaries(t)
	dir, err := os.MkdirTemp("/tmp", "swsk-rgw")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	rsock := filepath.Join(dir, "r.sock")
	// Fix-pack 1b harness: remote launches are now confined to machine-configured cwd roots
	// and FAIL CLOSED when none are set (R-POL.3/.7). Seed a remote-policy.json (loaded on
	// start) allowing the resolved test temp root, so a remote launch whose cwd is a
	// t.TempDir() (which lives under os.TempDir()) is permitted. Without this, fail-closed
	// would refuse the remote launch in TestRemoteLaunchE2E. This only ALLOWS the legitimate
	// test working directory; it weakens no security assertion (the roots guard itself is
	// tested in launch_roots_test.go / remote_policy_test.go, and the E2E's content-hash
	// tamper assertion is unaffected). It is a no-op before enforcement lands (Serve ignores
	// the file) and permits the launch after.
	tmpRoot, terr := filepath.EvalSymlinks(os.TempDir())
	if terr != nil {
		tmpRoot = filepath.Clean(os.TempDir())
	}
	if werr := writeRemoteLaunchPolicy(dir, []string{tmpRoot}); werr != nil {
		t.Fatalf("seed remote launch policy: %v", werr)
	}
	sk, err := Serve(Config{
		StateDir:           dir,
		SocketPath:         filepath.Join(dir, "d.sock"),
		LockPath:           filepath.Join(dir, "d.lock"),
		LogPath:            filepath.Join(dir, "d.log"),
		ShimBinary:         swarmBin,
		MaxSessions:        16,
		PollInterval:       50 * time.Millisecond,
		StalenessThreshold: 2 * time.Second,
		FakeAgentBin:       fakeAgentBin,
		RemoteSocketPath:   rsock,
	})
	if err != nil {
		t.Fatalf("Serve with RemoteSocketPath: %v", err)
	}
	t.Cleanup(func() { _ = sk.Close() })
	return sk, rsock
}

func TestRGW_RemoteSocketIsRemoteTierAndFailClosed(t *testing.T) {
	sk, rsock := assembleWithRemote(t)

	// The remote socket exists and accepts connections.
	if fi, err := os.Stat(rsock); err != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("remote.sock not a socket at %s: err=%v", rsock, err)
	}

	rc := dialRemote(t, rsock)
	sid := rc.endpointID + "/sess1"

	// A mutating op MISSING the device fields is invalid_field (structural precondition).
	rc.write(protocol.Control{
		Op: protocol.OpKill, EndpointID: rc.endpointID, SessionID: sid,
		OperationID: "op-1",
	})
	got := rc.read(5 * time.Second)
	if got.Op != protocol.OpError || got.ErrorCode != protocol.CodeInvalidField {
		t.Fatalf("remote kill missing device fields = op %q code %q; want error/invalid_field", got.Op, got.ErrorCode)
	}

	// A well-formed mutating op with device fields, but no paired device in the
	// registry, is refused not_authorized (fail-closed: unknown device).
	exp := time.Now().Add(time.Minute)
	rc.write(protocol.Control{
		Op: protocol.OpKill, EndpointID: rc.endpointID, SessionID: sid,
		OperationID: "op-2", DeviceID: "nobody", DeviceSig: "c2ln", ExpiresAt: &exp,
	})
	got = rc.read(5 * time.Second)
	if got.Op != protocol.OpError || got.ErrorCode != protocol.CodeNotAuthorized {
		t.Fatalf("remote kill from an unpaired device = op %q code %q; want error/not_authorized (fail-closed)", got.Op, got.ErrorCode)
	}

	// The MAIN socket is unaffected: a connection there is owner-tier and NOT device-
	// gated (list works without any device auth).
	mc := dialRemoteRaw(t, sk.socketPath)
	mc.write(protocol.Control{Op: protocol.OpList, EndpointID: mc.endpointID})
	if got := mc.read(5 * time.Second); got.Op != protocol.OpList {
		t.Fatalf("main-tier list = op %q; want %q (owner tier must not be device-gated)", got.Op, protocol.OpList)
	}
}

// dialRemoteRaw dials any socket and completes the hello, reusing the rawRemote
// client; it is the main-tier counterpart to dialRemote (same wire, different socket).
func dialRemoteRaw(t *testing.T, sock string) *rawRemote {
	t.Helper()
	conn, err := net.DialTimeout("unix", sock, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", sock, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	r := &rawRemote{t: t, conn: conn}
	r.write(protocol.Control{Op: protocol.OpHello, ProtocolVersion: protocol.Version})
	rep := r.read(5 * time.Second)
	if rep.Op != protocol.OpHello {
		t.Fatalf("hello reply op = %q; want %q", rep.Op, protocol.OpHello)
	}
	r.endpointID = rep.EndpointID
	return r
}
