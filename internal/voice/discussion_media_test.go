package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func TestDiscussionRecipientUsesTheVerifiedActor(t *testing.T) {
	c, s, _, _ := newControllerFixture(t)
	c.discussion = &DiscussionScope{InstallationID: "install", DiscussionID: "discussion", GuildID: "900", ThreadID: "901"}
	origin := schema.MessageOrigin{Kind: "external", InstallationID: "install", DiscussionID: "discussion", GuildID: "900", ChannelID: "901",
		RequestID: "request", MessageID: "310", ActorID: "401", RequesterID: "401", Revision: "first"}
	if _, err := c.submitDiscussion(origin, "check the files", "", ""); err != nil {
		t.Fatal(err)
	}
	st, err := s.readView()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.verifyPublicationRecipient(t.Context(), st, origin.ActorID, ""); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"everyone", "0401", "9002"} {
		if err := c.verifyPublicationRecipient(t.Context(), st, id, ""); err == nil {
			t.Fatalf("allowed an unverified recipient %q", id)
		}
	}
	c.discussion.VerifyRecipient = func(_ context.Context, id, anchor string) error {
		if id == "402" && anchor == "305" {
			return nil
		}
		return ErrExpired
	}
	if err := c.verifyPublicationRecipient(t.Context(), st, "402", "305"); err != nil {
		t.Fatal(err)
	}
	if err := c.verifyPublicationRecipient(t.Context(), st, "401", "305"); !errors.Is(err, ErrExpired) {
		t.Fatalf("explicit mismatched history anchor accepted: %v", err)
	}
	c.discussion.GuildID, c.discussion.DMOwnerID = "", "401"
	if err := c.verifyPublicationRecipient(t.Context(), st, "401", "310"); err == nil {
		t.Fatal("personal reply unexpectedly allowed a guild mention")
	}
}

func TestDiscussionPublicationBindsFileOrderAndMention(t *testing.T) {
	_, s, _, b := newControllerFixture(t)
	first := schema.PublicationFile{ID: "first", Name: "proof.csv", Size: 12}
	second := schema.PublicationFile{ID: "second", Name: "proof.png", Size: 16}
	p := Publication{ID: "publication", Text: "check", DiscussionFiles: `[{"id":"first","name":"proof.csv","size":12},{"id":"second","name":"proof.png","size":16}]`, RecipientID: "401", Exported: true}
	if err := s.update(func(st *State) error {
		putRecord(st, "publications", &st.Publications, key(b.ConversationID, p.ID), p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	d := DiscussionController{store: s, cfg: Config{ConversationID: b.ConversationID}}
	h := schema.PublicationContentHash(p.Text, []schema.PublicationFile{first, second}, "401")
	output := publicationOutput(p)
	if err := d.ValidatePublication(t.Context(), output, h); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal([]schema.PublicationFile{second, first})
	if err != nil {
		t.Fatal(err)
	}
	output.FileManifest = string(raw)
	if err := d.ValidatePublication(t.Context(), output, schema.PublicationContentHash(p.Text, output.FileMetadata(), output.RecipientID)); !errors.Is(err, ErrStale) {
		t.Fatalf("swapped files passed: %v", err)
	}
	output = publicationOutput(p)
	output.RecipientID = "402"
	if err := d.ValidatePublication(t.Context(), output, schema.PublicationContentHash(p.Text, output.FileMetadata(), output.RecipientID)); !errors.Is(err, ErrStale) {
		t.Fatalf("changed recipient passed: %v", err)
	}
}

func TestDiscussionPublicationMediaFenceSurvivesReopen(t *testing.T) {
	dir := privateConfigDir(t)
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	check := func(store *Store, want int) {
		t.Helper()
		if err := store.db.View(func(tx *bolt.Tx) error {
			header, err := readHeader(tx)
			if err == nil && header.Version != want {
				return fmt.Errorf("store scheme %d, expected %d", header.Version, want)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	check(s, storeSchemaVersion)
	_, _, _, b := newControllerFixture(t)
	b, err = s.BindManager(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutRequest(b, Request{ID: "mention", Kind: "manager", RecipientID: "401", Payload: json.RawMessage(`{"question":"Which one?"}`), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	check(s, mediaStoreSchemaVersion)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	check(reopened, mediaStoreSchemaVersion)
}
