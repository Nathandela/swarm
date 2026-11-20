package voice

import (
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func TestPersonalDiscussionRequiresMatchingOwnerOrigin(t *testing.T) {
	scope := DiscussionScope{InstallationID: "place", DiscussionID: "discussion", DMOwnerID: "owner", ThreadID: "dm"}
	base := schema.MessageOrigin{Kind: "owner_direct", ActorID: "owner", RequesterID: "owner", InstallationID: "place", DiscussionID: "discussion", ChannelID: "dm", MessageID: "m", RequestID: "r", Revision: "1"}
	if !scope.validateOrigin(base) {
		t.Fatal("pinned owner rejected")
	}
	for _, change := range []func(*schema.MessageOrigin){
		func(o *schema.MessageOrigin) { o.ActorID = "guest"; o.RequesterID = "guest" },
		func(o *schema.MessageOrigin) { o.RequesterID = "guest" },
		func(o *schema.MessageOrigin) { o.Kind = "external" },
		func(o *schema.MessageOrigin) { o.GuildID = "guild" },
		func(o *schema.MessageOrigin) { o.ChannelID = "another-dm" },
	} {
		o := base
		change(&o)
		if scope.validateOrigin(o) {
			t.Fatalf("accepted altered origin %+v", o)
		}
	}
}
