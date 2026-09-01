package phonecore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func testComposerPublication(id string) PendingPublication {
	body := &schema.ComposerSendReq{
		Session:         "m1/s1",
		SessionInstance: "instance-1",
		ExpectedTurn:    "turn-1",
		Text:            "ship it",
	}
	return PendingPublication{
		LogicalID:       "logical-" + id,
		OperationID:     id,
		Kind:            PublicationComposer,
		SessionID:       body.Session,
		SessionInstance: body.SessionInstance,
		ExpectedTurn:    body.ExpectedTurn,
		Text:            body.Text,
		Machine:         "m1",
		EpochID:         7,
		Target:          "rid-m1",
		AuthorityPub:    bytes.Repeat([]byte{0x44}, 32),
		Command: schema.DeviceCommandAuth{
			Action:      schema.ActionComposerSend,
			Machine:     "m1",
			Session:     body.Session,
			OperationID: id,
			ExpiresAt:   time.Unix(1_800_000_000, 0),
		},
		Composer:  body,
		Phase:     PublicationPrepared,
		CreatedAt: time.Unix(1_700_000_000, 0),
	}
}

func publicationCore(t *testing.T) *Core {
	t.Helper()
	c, err := Resume(Config{
		Dir:           t.TempDir(),
		Machine:       "m1",
		WakeSealer:    s14aNewSealer(t),
		ContentSealer: s14aNewSealer(t),
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	st := c.State()
	st.Machine = "m1"
	st.MachineRelayAuthPub = bytes.Repeat([]byte{0x44}, 32)
	st.EpochID = 7
	st.RoutingID = "rid-m1"
	st.Keys = s15EpochKeys()
	if err := c.Save(st); err != nil {
		t.Fatalf("seed publication state: %v", err)
	}
	return c
}

func TestPendingPublications_PrepareIsBoundedAndNeverEvictsUnresolved(t *testing.T) {
	c := publicationCore(t)
	for i := 0; i < MaxPendingPublications; i++ {
		id := "op-" + string(rune('A'+i))
		if err := c.PreparePublication(testComposerPublication(id)); err != nil {
			t.Fatalf("PreparePublication %d: %v", i, err)
		}
	}
	want := c.PendingPublications()
	if err := c.PreparePublication(testComposerPublication("overflow")); !errors.Is(err, ErrPublicationQueueFull) {
		t.Fatalf("overflow error = %v, want ErrPublicationQueueFull", err)
	}
	got := c.PendingPublications()
	if len(got) != len(want) {
		t.Fatalf("overflow changed queue length from %d to %d", len(want), len(got))
	}
	for i := range want {
		if got[i].OperationID != want[i].OperationID {
			t.Fatalf("overflow evicted/reordered index %d: got %q want %q", i, got[i].OperationID, want[i].OperationID)
		}
	}
}

func TestPendingPublications_ProjectionAndStateCopiesOwnEnvelopeBytes(t *testing.T) {
	c := publicationCore(t)
	p := testComposerPublication("op-copy")
	if err := c.PreparePublication(p); err != nil {
		t.Fatalf("PreparePublication: %v", err)
	}
	envelope := []byte("exact sealed envelope")
	if err := c.SealPublication(p.OperationID, 41, envelope); err != nil {
		t.Fatalf("SealPublication: %v", err)
	}

	first := c.PendingPublications()
	if len(first) != 1 || first[0].Phase != PublicationSealed || first[0].Sequence != 41 {
		t.Fatalf("projection = %+v", first)
	}
	first[0].Envelope[0] ^= 0xff
	first[0].Composer.Text = "mutated"

	second := c.PendingPublications()
	if !bytes.Equal(second[0].Envelope, envelope) {
		t.Fatalf("caller mutated core envelope: %q", second[0].Envelope)
	}
	if second[0].Composer.Text != "ship it" {
		t.Fatalf("caller mutated core composer body: %q", second[0].Composer.Text)
	}
}

func TestPendingPublications_ExactPrepareAndSealAreIdempotentButConflictFails(t *testing.T) {
	c := publicationCore(t)
	p := testComposerPublication("op-idempotent")
	if err := c.PreparePublication(p); err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	if err := c.PreparePublication(p); err != nil {
		t.Fatalf("exact repeated prepare: %v", err)
	}
	conflict := testComposerPublication(p.OperationID)
	conflict.Text = "different"
	conflict.Composer.Text = "different"
	if err := c.PreparePublication(conflict); !errors.Is(err, ErrPublicationConflict) {
		t.Fatalf("conflicting prepare = %v, want ErrPublicationConflict", err)
	}
	if err := c.SealPublication(p.OperationID, 9, []byte("same")); err != nil {
		t.Fatalf("first seal: %v", err)
	}
	if err := c.SealPublication(p.OperationID, 9, []byte("same")); err != nil {
		t.Fatalf("exact repeated seal: %v", err)
	}
	if err := c.SealPublication(p.OperationID, 10, []byte("rival")); !errors.Is(err, ErrPublicationConflict) {
		t.Fatalf("conflicting seal = %v, want ErrPublicationConflict", err)
	}
}

func TestPendingPublications_LogicalIDAndRoutingAuthorityAreUniqueFences(t *testing.T) {
	c := publicationCore(t)
	first := testComposerPublication("op-first")
	if err := c.PreparePublication(first); err != nil {
		t.Fatalf("first prepare: %v", err)
	}

	duplicateIntent := testComposerPublication("op-second")
	duplicateIntent.LogicalID = first.LogicalID
	if err := c.PreparePublication(duplicateIntent); !errors.Is(err, ErrPublicationConflict) {
		t.Fatalf("same logical id under another operation = %v, want ErrPublicationConflict", err)
	}

	wrongAuthority := testComposerPublication("op-wrong-authority")
	wrongAuthority.AuthorityPub = bytes.Repeat([]byte{0x55}, 32)
	if err := c.PreparePublication(wrongAuthority); !errors.Is(err, ErrPublicationState) {
		t.Fatalf("wrong routing authority = %v, want ErrPublicationState", err)
	}
}

func TestPendingPublications_RegistrationIdentityChangePurgesRatherThanRetargets(t *testing.T) {
	c := publicationCore(t)
	if err := c.PreparePublication(testComposerPublication("op-old-authority")); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	st := c.State()
	st.MachineRelayAuthPub = bytes.Repeat([]byte{0x66}, 32)
	if err := c.Save(st); err != nil {
		t.Fatalf("replace routing authority: %v", err)
	}
	got := c.PendingPublications()
	if len(got) != 1 || got[0].Phase != PublicationTerminal || got[0].TerminalCode != PublicationAuthorityChanged {
		t.Fatalf("replacement did not quarantine the old publication: %+v", got)
	}
}

func TestPendingPublications_AdmissionIsMonotonicAndIdempotent(t *testing.T) {
	c := publicationCore(t)
	p := testComposerPublication("op-admitted")
	if err := c.PreparePublication(p); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := c.MarkPublicationAdmitted(p.OperationID); !errors.Is(err, ErrPublicationState) {
		t.Fatalf("prepared -> admitted = %v, want ErrPublicationState", err)
	}
	if err := c.SealPublication(p.OperationID, 12, []byte("exact")); err != nil {
		t.Fatalf("seal: %v", err)
	}
	if err := c.MarkPublicationAdmitted(p.OperationID); err != nil {
		t.Fatalf("admit: %v", err)
	}
	if err := c.MarkPublicationAdmitted(p.OperationID); err != nil {
		t.Fatalf("repeat admit: %v", err)
	}
	got := c.PendingPublications()
	if len(got) != 1 || got[0].Phase != PublicationAdmitted || got[0].Sequence != 12 ||
		!bytes.Equal(got[0].Envelope, []byte("exact")) {
		t.Fatalf("admitted projection = %+v", got)
	}
}

func TestPendingPublications_SurviveRestartAndRevokePurgesThem(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	c, err := Resume(Config{Dir: dir, Machine: "m1", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	st := c.State()
	st.Machine, st.EpochID, st.RoutingID, st.Keys = "m1", 7, "rid-m1", s15EpochKeys()
	st.MachineRelayAuthPub = bytes.Repeat([]byte{0x44}, 32)
	if err := c.Save(st); err != nil {
		t.Fatalf("seed: %v", err)
	}
	p := testComposerPublication("op-restart")
	if err := c.PreparePublication(p); err != nil {
		t.Fatalf("PreparePublication: %v", err)
	}
	if err := c.SealPublication(p.OperationID, 77, []byte("ciphertext")); err != nil {
		t.Fatalf("SealPublication: %v", err)
	}

	restarted, err := Resume(Config{Dir: dir, Machine: "m1", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if got := restarted.PendingPublications(); len(got) != 1 || got[0].OperationID != p.OperationID {
		t.Fatalf("restart projection = %+v", got)
	}
	if err := restarted.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}
	again, err := Resume(Config{Dir: dir, Machine: "m1", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("restart after revoke: %v", err)
	}
	if got := again.PendingPublications(); len(got) != 0 {
		t.Fatalf("revoked phone restored pending publications: %+v", got)
	}
}

func TestPendingPublications_V17MigratesToAnEmptyJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFileName)
	if err := os.WriteFile(path, []byte(stateV17Fixture), 0o600); err != nil {
		t.Fatalf("write v17 fixture: %v", err)
	}
	kek := &s14aSealer{kek: stateV4FixtureKEK}
	store, err := OpenStore(path, "m1", kek, kek)
	if err != nil {
		t.Fatalf("OpenStore v17: %v", err)
	}
	if got := store.Load().PendingPublications; len(got) != 0 {
		t.Fatalf("v17 invented pending publications: %+v", got)
	}
}

func TestPendingPublications_DirectStateSaveCannotBypassTheBound(t *testing.T) {
	c := publicationCore(t)
	st := c.State()
	for i := 0; i <= MaxPendingPublications; i++ {
		st.PendingPublications = append(st.PendingPublications, testComposerPublication(
			"over-"+string(rune('A'+i)),
		))
	}
	if err := c.Save(st); !errors.Is(err, ErrPublicationQueueFull) {
		t.Fatalf("oversized State Save = %v, want ErrPublicationQueueFull", err)
	}
	if got := c.PendingPublications(); len(got) != 0 {
		t.Fatalf("failed oversized Save changed the durable journal: %+v", got)
	}
}
