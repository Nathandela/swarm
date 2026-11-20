package discord

import (
	"testing"
)

func TestTeammateSupervisorBindingsSurviveReopenAndReplayedOpeners(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	old := teammateBinding{TeammateDiscussion: TeammateDiscussion{InstallationID: "install", ID: "old", Route: Route{GuildID: "1", ChannelID: "2"}}}
	fresh := teammateBinding{TeammateDiscussion: TeammateDiscussion{InstallationID: "install", ID: "new", Route: Route{GuildID: "1", ChannelID: "3"}, SupervisorSocket: "/home/daemon.sock"}}
	for _, b := range []teammateBinding{old, fresh} {
		if err := db.teammateBind(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, b := range []teammateBinding{old, fresh} {
		got, err := db.teammateBindingByID("install", b.ID)
		if err != nil || got.SupervisorSocket != b.SupervisorSocket {
			t.Fatal("lost route after restart", err)
		}
		replayed := b
		replayed.SupervisorSocket = "/different.sock"
		if err := db.teammateBind(replayed); err != nil {
			t.Fatal(err)
		}
		retained, err := db.teammateBindingByID("install", b.ID)
		if err != nil || retained.SupervisorSocket != b.SupervisorSocket {
			t.Fatal("replayed opener moved a discussion to another supervisor", err)
		}
	}
}
