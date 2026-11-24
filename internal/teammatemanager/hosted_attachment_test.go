package teammatemanager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/discord"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/voice"
	"github.com/Nathandela/swarm/internal/workermail"
)

func runHostedAttachment(t *testing.T, ctx context.Context, r *Runtime, thread discord.TeammateDiscussion, reader discord.TeammateHistoryReader, project, state, fixture, cli string, wait func(string, time.Duration, func() bool), outputs *[]discord.TeammatePublication, roster func() []protocol.SessionView) {
	t.Helper()
	var f struct{ Manifest, Document, Pixel string }
	raw, err := os.ReadFile(filepath.Join(fixture, "expected.json"))
	if err != nil || json.Unmarshal(raw, &f) != nil || !filepath.IsAbs(f.Manifest) || len(f.Document) != 16 || len(f.Pixel) != 6 {
		t.Fatal("generate a private fixture with TestPersonalAttachmentOnlyHandoff or TestGuildAttachmentIngressUsesVisibleForwardAndReply")
	}
	body := "Delegate to a single worker with swarm spawn --cli " + cli + " in " + project + ". Only the worker may open the attached document and image. Ask it to read the document's complete marker and decode the RGB of the PNG top-left pixel as six lowercase hex digits. It must send a complete note with both results from the same child; publish both results from that note. Inspect only the manifest yourself; never open the files or provide a guess."
	twoWay := os.Getenv("SWARM_TEAMMATE_TEST_TWO_WAY") == "1"
	if twoWay {
		body += " Ask the worker to copy the input PNG bytes to screenshot.png in its working directory and write a summary.csv with header marker,pixel and one data row containing the two values. Use a final newline. Send one swarm teammate message --kind complete --text with both values and --file screenshot.png --file summary.csv. Once that command succeeds, overwrite the worker's local summary.csv with the exact text replaced to prove the note's files are already pinned. Use swarm_read_note to get the file IDs from that completion, then use publish with those file_ids, the values in the caption, and final=true."
		if thread.Route.GuildID != "" {
			body += " Address the final reply to the verified participant 401, message 301, using recipient_id and recipient_message_id."
		}
	}
	authority := discord.AuthorityOwnerDirect
	if thread.Route.GuildID != "" {
		authority = discord.AuthorityExternal
	}
	a := discord.TeammateAdmission{ExpectReply: true, Discussion: thread, OperationID: "input-301", Revision: "rev-301", Text: body, AttachmentManifest: f.Manifest, Route: discord.Route{GuildID: thread.Route.GuildID, ChannelID: thread.Route.ChannelID, MessageID: "301"}, Author: discord.Actor{ID: "401", Name: "fixture"}, InvokerID: "401", Authority: authority}
	for attempt := 1; ; attempt++ {
		err = r.Admit(ctx, a, reader)
		if err == nil {
			break
		}
		if attempt >= 3 || !errors.Is(err, voice.ErrDiscussionActivationUnavailable) || !errors.Is(err, voice.ErrControllerNativeUnavailable) {
			t.Fatal(err)
		}
		t.Logf("source input-301 retained while %s starts: %v", cli, err)
		time.Sleep(time.Second)
	}
	wait("same worker reported document marker and decoded image pixel", 4*time.Minute, func() bool {
		for _, p := range *outputs {
			if !strings.HasPrefix(p.AdmissionID, "worker-message-") || p.JobID == "" || p.AttemptID == "" || !strings.Contains(p.Body, f.Document) || !strings.Contains(strings.ToLower(p.Body), f.Pixel) {
				continue
			}
			if twoWay && len(p.Files) != 2 {
				continue
			}
			st, err := r.Status(thread)
			if err != nil {
				return false
			}
			for _, n := range st.Notes {
				if n.Kind != "complete" || n.JobID != p.JobID || n.AttemptID != p.AttemptID {
					continue
				}
				for _, row := range roster() {
					if row.ID == n.Session && row.SpawnedFrom != "" {
						stored, err := workermail.Lookup(state, workermail.Binding{Discussion: row.WorkerDiscussion, Session: n.Session, Instance: n.Instance, Attempt: row.WorkerAttempt}, n.ID)
						if err != nil || !strings.Contains(stored.Text, f.Document) || !strings.Contains(strings.ToLower(stored.Text), f.Pixel) {
							continue
						}
						if twoWay {
							mutated, err := os.ReadFile(filepath.Join(row.Cwd, "summary.csv"))
							if err != nil || strings.TrimSpace(string(mutated)) != "replaced" {
								continue
							}
							verifyHostedPublishedFiles(t, ctx, r, thread, p, f.Manifest, f.Document, f.Pixel)
						}
						t.Logf("attachment worker %s instance %s attempt %s note %s publication %s", n.Session, n.Instance, n.AttemptID, n.ID, p.ID)
						return true
					}
				}
			}
		}
		return false
	})
}

func verifyHostedPublishedFiles(t *testing.T, ctx context.Context, r *Runtime, discussion discord.TeammateDiscussion, p discord.TeammatePublication, manifest, document, pixel string) {
	t.Helper()
	var entries []struct{ File, ContentType string }
	raw, err := os.ReadFile(manifest)
	if err != nil || json.Unmarshal(raw, &entries) != nil {
		t.Fatal("cannot read the isolated connector manifest", err)
	}
	expected := map[string][]byte{"summary.csv": []byte("marker,pixel\n" + document + "," + pixel + "\n")}
	for _, entry := range entries {
		if entry.ContentType == "image/png" {
			expected["screenshot.png"], err = os.ReadFile(filepath.Join(filepath.Dir(manifest), entry.File))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	recipient := ""
	if discussion.Route.GuildID != "" {
		recipient = "401"
	}
	hash := schema.PublicationContentHash(p.Body, p.Files, recipient)
	if p.RecipientID != recipient || p.BodyHash != "" && p.BodyHash != hash {
		t.Fatalf("wrong publication identity or verified recipient %q", p.RecipientID)
	}
	p.BodyHash = hash
	if err := r.ValidatePublication(ctx, discussion, p); err != nil {
		t.Fatalf("exported publication receipt did not validate: %v", err)
	}
	contents, err := r.ReadPublicationFiles(ctx, discussion, p)
	if err != nil || len(contents) != len(expected) || len(expected) != 2 {
		t.Fatalf("publication file receipt failed: %v (files=%d)", err, len(contents))
	}
	for i, f := range p.Files {
		want, ok := expected[f.Name]
		if !ok || !bytes.Equal(contents[i], want) || f.Size != int64(len(want)) || f.SHA256 != fmt.Sprintf("%x", sha256.Sum256(want)) {
			t.Fatalf("file %s did not match its receipt or immutable source", f.Name)
		}
		delete(expected, f.Name)
	}
	t.Logf("file receipt %s hash %s recipient %q reconfirmed original PNG and CSV after worker source edit", p.ID, p.BodyHash, p.RecipientID)
}
