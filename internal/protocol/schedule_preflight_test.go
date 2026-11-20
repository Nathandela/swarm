package protocol

import (
	"slices"
	"strings"
	"testing"
)

type preflightStub struct{ *scheduleRunStub }

func (preflightStub) ScheduleWorkspaceConflict(path string) string { return path + "/active" }
func (preflightStub) ScheduleRouteAvailability(agent string) string {
	return agent + " needs configuration"
}

func TestSchedulePreflightProtocolNegotiation(t *testing.T) {
	b := preflightStub{&scheduleRunStub{stubDaemon: newStubDaemon()}}
	sock := serveOwnerAPI(t, b)
	caps := []string{CapSchedule, CapScheduleHostTime, CapScheduleWorkspace, CapScheduleRoute}
	client := dialClient(t, sock, caps)
	for _, cap := range caps {
		if !slices.Contains(client.Capabilities(), cap) {
			t.Fatalf("missing owner capability %s", cap)
		}
	}
	if got, err := client.ScheduleWorkspaceConflict("/fixture"); err != nil || got != "/fixture/active" {
		t.Fatalf("workspace %q %v", got, err)
	}
	if _, err := client.ScheduleWorkspaceConflict("relative"); err == nil {
		t.Fatal("relative workspace accepted")
	}
	if got, err := client.ScheduleRouteAvailability("agy"); err != nil || got != "agy needs configuration" {
		t.Fatalf("route %q %v", got, err)
	}
	if _, err := client.ScheduleRouteAvailability("unknown"); err == nil {
		t.Fatal("unknown route accepted")
	}
	legacy := dialClient(t, sock, []string{CapSchedule})
	if _, err := legacy.ScheduleWorkspaceConflict("/fixture"); err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("legacy workspace: %v", err)
	}
	if _, err := legacy.ScheduleRouteAvailability("agy"); err == nil {
		t.Fatal("legacy agent preflight accepted")
	}
	for _, srv := range []*Server{{d: b, remoteTier: true}, {d: b, completionTier: true}} {
		for _, cap := range []string{CapScheduleHostTime, CapScheduleWorkspace, CapScheduleRoute} {
			if slices.Contains(srv.supportedCaps(), cap) {
				t.Fatalf("non-owner advertised %s", cap)
			}
		}
	}
}
