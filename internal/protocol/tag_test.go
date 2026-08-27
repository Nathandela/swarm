package protocol

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func TestSetTagForwardsSanitizedTag(t *testing.T) {
	stub, c := renameStub(t)
	id := NamespacedID(c.EndpointID(), "sess1")
	if err := c.SetTag(id, "front\x00end\n"); err != nil {
		t.Fatalf("SetTag: %v", err)
	}
	if got := stub.tags(); len(got) != 1 || got[0] != (tagCall{id: "sess1", tag: "frontend"}) {
		t.Fatalf("SetTag forwards = %+v", got)
	}
}

func TestStampViewCarriesTag(t *testing.T) {
	v := stampView("ep", persist.Meta{ID: "s1", Tag: "backend"}, status.GroupWorking, false, false, time.Time{})
	if v.Tag != "backend" {
		t.Fatalf("SessionView.Tag = %q, want backend", v.Tag)
	}
}
