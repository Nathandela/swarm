package protocol

import (
	"reflect"
	"strings"
	"testing"
)

// E6.8 — every applicable message carries an endpoint id + a namespaced
// (endpoint-scoped) session id, for V2 multi-daemon forward-compat (F-1). A
// message addressed to a FOREIGN endpoint or a session in a foreign namespace is
// rejected. E6.8b/F-2 — no message references a UDS-specific construct.

func TestNamespacing_IDRoundTrip(t *testing.T) {
	ep, local := "ep-abc", "sess-123"
	ns := NamespacedID(ep, local)
	gotEp, gotLocal, ok := ParseID(ns)
	if !ok || gotEp != ep || gotLocal != local {
		t.Fatalf("ParseID(NamespacedID(%q,%q))=%q -> (%q,%q,%v), want (%q,%q,true)", ep, local, ns, gotEp, gotLocal, ok, ep, local)
	}
	for _, bad := range []string{"", "noslash", "/local", "ep/"} {
		if _, _, ok := ParseID(bad); ok {
			t.Errorf("ParseID(%q) ok=true, want false", bad)
		}
	}
}

// TestNamespacing_ForeignEndpointRejected asserts the server rejects a control
// message whose endpoint_id is not the one it assigned this connection.
func TestNamespacing_ForeignEndpointRejected(t *testing.T) {
	stub := oneRunningSession()
	sock := serveStub(t, stub)
	r := rawDial(t, sock)
	hello := r.hello(Version, nil)

	// Kill carrying a foreign endpoint id (not this connection's).
	foreign := hello.EndpointID + "-not-me"
	r.writeControl(Control{Op: OpKill, EndpointID: foreign, SessionID: NamespacedID(foreign, "sess1")})
	resp := r.readControl()
	if resp.Op != OpError {
		t.Fatalf("kill with foreign endpoint id: reply op = %q, want %q", resp.Op, OpError)
	}
	if len(stub.killedIDs()) != 0 {
		t.Fatalf("foreign-endpoint kill was forwarded to the daemon: %v", stub.killedIDs())
	}
}

// TestNamespacing_ForeignSessionNamespaceRejected asserts a session id whose
// endpoint prefix belongs to a different endpoint is rejected even when the
// message's endpoint_id header is correct.
func TestNamespacing_ForeignSessionNamespaceRejected(t *testing.T) {
	stub := oneRunningSession()
	sock := serveStub(t, stub)
	r := rawDial(t, sock)
	hello := r.hello(Version, nil)

	r.writeControl(Control{
		Op:         OpKill,
		EndpointID: hello.EndpointID,
		SessionID:  NamespacedID("some-other-endpoint", "sess1"),
	})
	resp := r.readControl()
	if resp.Op != OpError {
		t.Fatalf("kill with foreign session namespace: reply op = %q, want %q", resp.Op, OpError)
	}
	if len(stub.killedIDs()) != 0 {
		t.Fatalf("foreign-namespace kill was forwarded: %v", stub.killedIDs())
	}
}

// TestNamespacing_EveryViewCarriesEndpointAndNamespacedID re-asserts the F-1
// stamping on the list path (the row type the TUI consumes).
func TestNamespacing_EveryViewCarriesEndpointAndNamespacedID(t *testing.T) {
	stub := oneRunningSession()
	sock := serveStub(t, stub)
	c := dialClient(t, sock, nil)

	views, err := c.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, v := range views {
		if v.EndpointID != c.EndpointID() {
			t.Errorf("view %q missing/foreign endpoint id %q", v.ID, v.EndpointID)
		}
		if ep, _, ok := ParseID(v.ID); !ok || ep != c.EndpointID() {
			t.Errorf("view id %q is not namespaced under this endpoint", v.ID)
		}
	}
}

// TestF2_NoTransportSpecificFieldsInMessages asserts F-2 by construction: no
// field name or json tag of any wire message references a UDS/transport-specific
// construct (fd passing, socket path, local-only address, peer creds). A remote
// transport can therefore reuse these schemas unchanged.
func TestF2_NoTransportSpecificFieldsInMessages(t *testing.T) {
	forbidden := []string{"socket", "sockaddr", "unixaddr", "peercred", "unixconn"}
	types := []reflect.Type{
		reflect.TypeOf(Control{}),
		reflect.TypeOf(SessionView{}),
		reflect.TypeOf(LaunchReq{}),
	}
	for _, ty := range types {
		for i := 0; i < ty.NumField(); i++ {
			f := ty.Field(i)
			name := strings.ToLower(f.Name)
			tag := strings.ToLower(strings.Split(f.Tag.Get("json"), ",")[0])
			for _, bad := range forbidden {
				if strings.Contains(name, bad) || strings.Contains(tag, bad) {
					t.Errorf("%s.%s (json %q) references transport-specific construct %q — violates F-2", ty.Name(), f.Name, tag, bad)
				}
			}
			// A bare "fd" field (file-descriptor passing) is also transport-specific.
			if name == "fd" || strings.HasSuffix(name, "fd") || tag == "fd" || strings.HasSuffix(tag, "_fd") {
				t.Errorf("%s.%s (json %q) looks like fd-passing — violates F-2", ty.Name(), f.Name, tag)
			}
		}
	}
}
