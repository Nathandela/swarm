package protocol

import (
	"errors"
	"slices"
	"testing"
)

type contextGuardSettingsStub struct {
	*stubDaemon
	settings ContextGuardSettings
	getCalls int
	setCalls int
}

func newContextGuardSettingsStub() *contextGuardSettingsStub {
	return &contextGuardSettingsStub{
		stubDaemon: newStubDaemon(),
		settings:   ContextGuardSettings{SchemaVersion: 1, AutoCompact: ContextGuardAutoCompact{ThresholdPercent: 80}},
	}
}

func (s *contextGuardSettingsStub) ContextGuardSettings() (ContextGuardSettings, error) {
	s.getCalls++
	return s.settings, nil
}

func (s *contextGuardSettingsStub) SetContextGuardSettings(expected uint64, compact ContextGuardAutoCompact) (ContextGuardSettings, error) {
	s.setCalls++
	if expected != s.settings.Revision {
		return ContextGuardSettings{}, ErrContextGuardSettingsStaleRevision
	}
	if compact != s.settings.AutoCompact {
		s.settings.AutoCompact = compact
		s.settings.Revision++
	}
	return s.settings, nil
}

func TestContextGuardSettings_OwnerProtocolAndCapability(t *testing.T) {
	stub := newContextGuardSettingsStub()
	c := dialClient(t, serveContextGuardAPI(t, stub), []string{CapContextGuardSettings})
	got, err := c.ContextGuardSettings()
	if err != nil || got.Revision != 0 || got.AutoCompact.ThresholdPercent != 80 {
		t.Fatalf("ContextGuardSettings = %#v, %v", got, err)
	}
	updated, err := c.SetContextGuardSettings(got.Revision, ContextGuardAutoCompact{Enabled: true, ThresholdPercent: 95})
	if err != nil || updated.Revision != 1 || !updated.AutoCompact.Enabled {
		t.Fatalf("SetContextGuardSettings = %#v, %v", updated, err)
	}
	if _, err := c.SetContextGuardSettings(0, updated.AutoCompact); !errors.Is(err, ErrContextGuardSettingsStaleRevision) {
		t.Fatalf("stale SetContextGuardSettings error = %v, want stale_revision", err)
	}
}

func TestContextGuardSettings_GatesBeforeBodyOrBackend(t *testing.T) {
	t.Run("remote is refused first", func(t *testing.T) {
		stub := newContextGuardSettingsStub()
		rc := rawDial(t, serveRemoteAPI(t, stub))
		rep := rc.hello(Version, []string{CapContextGuardSettings})
		if slices.Contains(rep.Capabilities, CapContextGuardSettings) {
			t.Fatalf("remote hello advertised owner-only %q: %v", CapContextGuardSettings, rep.Capabilities)
		}
		rc.writeControl(Control{Op: OpContextGuardSet, EndpointID: rep.EndpointID})
		got := rc.readControl()
		if got.Op != OpError || got.ErrorCode != CodeNotAuthorized || stub.setCalls != 0 {
			t.Fatalf("remote reply=%#v setCalls=%d; want not_authorized before body/backend", got, stub.setCalls)
		}
	})
	t.Run("capability required", func(t *testing.T) {
		stub := newContextGuardSettingsStub()
		rc := rawDial(t, serveContextGuardAPI(t, stub))
		rep := rc.hello(Version, nil)
		rc.writeControl(Control{Op: OpContextGuardGet, EndpointID: rep.EndpointID})
		got := rc.readControl()
		if got.Op != OpError || got.ErrorCode != CodeCapabilityRefused || stub.getCalls != 0 {
			t.Fatalf("unnegotiated reply=%#v getCalls=%d; want capability_refused before backend", got, stub.getCalls)
		}
	})
	t.Run("unsupported backend", func(t *testing.T) {
		rc := rawDial(t, serveContextGuardAPI(t, newStubDaemon()))
		rep := rc.hello(Version, []string{CapContextGuardSettings})
		rc.writeControl(Control{Op: OpContextGuardGet, EndpointID: rep.EndpointID})
		got := rc.readControl()
		if got.Op != OpError || got.ErrorCode != CodeUnavailable {
			t.Fatalf("unsupported reply=%#v; want unavailable", got)
		}
	})
	t.Run("invalid threshold is refused before backend", func(t *testing.T) {
		for _, threshold := range []int{39, 96} {
			stub := newContextGuardSettingsStub()
			rc := rawDial(t, serveContextGuardAPI(t, stub))
			rep := rc.hello(Version, []string{CapContextGuardSettings})
			body := ContextGuardSettingsSetReq{ExpectedRevision: 0, AutoCompact: ContextGuardAutoCompact{ThresholdPercent: threshold}}
			rc.writeControl(Control{Op: OpContextGuardSet, EndpointID: rep.EndpointID, ContextGuardSet: &body})
			got := rc.readControl()
			if got.Op != OpError || got.ErrorCode != CodeInvalidField || stub.setCalls != 0 {
				t.Fatalf("threshold %d reply=%#v setCalls=%d; want invalid_field before backend", threshold, got, stub.setCalls)
			}
		}
	})
}

func TestContextGuardSettings_OldClientRemainsCompatible(t *testing.T) {
	stub := newContextGuardSettingsStub()
	rc := rawDial(t, serveContextGuardAPI(t, stub))
	rep := rc.hello(Version, nil)
	if rcaps := rep.Capabilities; len(rcaps) != 0 {
		t.Fatalf("old client negotiated unexpected capabilities %v", rcaps)
	}
	rc.writeControl(Control{Op: OpList, EndpointID: rep.EndpointID})
	if got := rc.readControl(); got.Op != OpList {
		t.Fatalf("old-client list reply = %#v", got)
	}
}

func serveContextGuardAPI(t *testing.T, d DaemonAPI) string {
	t.Helper()
	sock := tmpSock(t)
	srv, err := Serve(d, sock)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}
