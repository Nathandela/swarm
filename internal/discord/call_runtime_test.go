package discord

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/Nathandela/swarm/internal/version"
)

func TestCallStatusIdentifiesTheServingConnector(t *testing.T) {
	f := newDispatcherCallFixture(t, false)
	s := f.connector.service
	status, err := s.localStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	r := status.CallRuntime
	if r == nil || r.PID != os.Getpid() || r.Version != version.Version || r.Commit != version.Commit ||
		r.ApplicationID != s.Config.ApplicationID || r.RoomID != s.Config.Calls.RoomID ||
		r.DiscussionID != s.Config.DMChannelID || r.GuildID != s.Config.Calls.GuildID {
		t.Fatalf("unexpected connector runtime %+v", r)
	}
	raw, err := json.Marshal(LocalStatus{})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, present := fields["call_runtime"]; present {
		t.Fatal("offline status claimed a serving connector")
	}
}
