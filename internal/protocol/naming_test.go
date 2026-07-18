package protocol

// v0.4 P2 — session naming (bd agents-tracker-4e2). Failing-first white-box tests
// for the wire surface: LaunchReq.Name and SessionView.Name carry a user-provided
// label across the control codec, the server re-validates the name (E6.6: strip
// control characters, cap length) without failing the launch, and stampView carries
// the persisted name to clients. Version-skew: a SessionView without a name field
// decodes to an empty Name so the client can fall back to the agent label.

import (
	"strings"
	"testing"
	"unicode"

	"github.com/Nathandela/swarm/internal/persist"
)

// A LaunchReq's Name survives the control codec round-trip.
func TestControl_LaunchNameRoundTrips(t *testing.T) {
	in := Control{Op: OpLaunch, EndpointID: "ep", Launch: &LaunchReq{Agent: "claude", Cwd: "/tmp", Name: "backend-refactor"}}
	b, err := EncodeControl(in)
	if err != nil {
		t.Fatalf("EncodeControl: %v", err)
	}
	got, err := DecodeControl(b)
	if err != nil {
		t.Fatalf("DecodeControl: %v", err)
	}
	if got.Launch == nil || got.Launch.Name != "backend-refactor" {
		t.Fatalf("LaunchReq.Name did not survive the control codec: %+v", got.Launch)
	}
}

// A SessionView's Name survives the control codec round-trip.
func TestControl_SessionViewNameRoundTrips(t *testing.T) {
	in := Control{Op: OpList, EndpointID: "ep", Sessions: []SessionView{{ID: "ep/s1", Agent: "claude", Name: "backend-refactor"}}}
	b, err := EncodeControl(in)
	if err != nil {
		t.Fatalf("EncodeControl: %v", err)
	}
	got, err := DecodeControl(b)
	if err != nil {
		t.Fatalf("DecodeControl: %v", err)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].Name != "backend-refactor" {
		t.Fatalf("SessionView.Name did not survive the control codec: %+v", got.Sessions)
	}
}

// Version skew: a SessionView from an older daemon that predates naming carries no
// "name" key; it must decode to an empty Name (never an error), so the client can
// fall back to the agent label rather than showing a blank identity.
func TestControl_SessionViewWithoutNameDecodesEmpty(t *testing.T) {
	raw := []byte(`{"op":"list","endpoint_id":"ep","sessions":[{"id":"ep/s1","agent":"claude"}]}`)
	got, err := DecodeControl(raw)
	if err != nil {
		t.Fatalf("DecodeControl of a name-less SessionView must not error: %v", err)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].Name != "" {
		t.Fatalf("an absent name must decode as empty; got %+v", got.Sessions)
	}
}

// The server re-validates the name server-side (E6.6/P-6): control characters are
// stripped and the value is capped, but — being a cosmetic label — an over-long or
// control-laden name never fails the launch. The sanitized value reaches the daemon.
func TestRevalidate_LaunchSanitizesName(t *testing.T) {
	stub := newStubDaemon()
	c := dialClient(t, serveStub(t, stub), nil)

	req := validLaunch(t)
	req.Name = "bad\x00\tname" + strings.Repeat("x", 100)
	if _, err := c.Launch(req); err != nil {
		t.Fatalf("a cosmetic name must not fail the launch: %v", err)
	}
	specs := stub.launchSpecs()
	if len(specs) != 1 {
		t.Fatalf("launch was not forwarded exactly once: %d", len(specs))
	}
	got := specs[0].Name
	for _, r := range got {
		if unicode.IsControl(r) {
			t.Fatalf("a control character survived name sanitization: %q", got)
		}
	}
	if n := len([]rune(got)); n > 64 {
		t.Fatalf("name not capped to 64 runes: len=%d (%q)", n, got)
	}
	if !strings.HasPrefix(got, "badname") {
		t.Fatalf("sanitization dropped visible content: %q", got)
	}
}

// A list stamps the persisted Name into each SessionView; a meta without a name
// carries an empty Name (the client falls back to the agent label).
func TestList_StampsSessionName(t *testing.T) {
	stub := newStubDaemon()
	stub.setMetas(
		persist.Meta{ID: "s1", AgentType: "claude", Name: "backend-refactor", Status: runningStatus()},
		persist.Meta{ID: "s2", AgentType: "codex", Status: runningStatus()},
	)
	c := dialClient(t, serveStub(t, stub), nil)

	views, err := c.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byLocal := map[string]SessionView{}
	for _, v := range views {
		_, local, ok := ParseID(v.ID)
		if !ok {
			t.Fatalf("view id %q is not namespaced", v.ID)
		}
		byLocal[local] = v
	}
	if byLocal["s1"].Name != "backend-refactor" {
		t.Fatalf("stampView must carry meta.Name; got %q", byLocal["s1"].Name)
	}
	if byLocal["s2"].Name != "" {
		t.Fatalf("a nameless meta must stamp an empty Name; got %q", byLocal["s2"].Name)
	}
}
