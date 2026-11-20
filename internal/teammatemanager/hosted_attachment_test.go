package teammatemanager

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/discord"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/workermail"
)

func runHostedAttachment(t *testing.T, ctx context.Context, r *Runtime, thread discord.TeammateDiscussion, reader discord.TeammateHistoryReader, project, state, fixture string, wait func(string, time.Duration, func() bool), outputs *[]discord.TeammatePublication, roster func() []protocol.SessionView) {
	t.Helper()
	var f struct{ Manifest, Document, Pixel string }
	raw, err := os.ReadFile(filepath.Join(fixture, "expected.json"))
	if err != nil || json.Unmarshal(raw, &f) != nil || !filepath.IsAbs(f.Manifest) || len(f.Document) != 16 || len(f.Pixel) != 6 {
		t.Fatal("run TestPersonalAttachmentOnlyHandoff with a private attachment fixture directory first")
	}
	body := "Delegate to a single Codex worker in " + project + ". Only the worker may open the attached document and image. Ask it to read the document's complete marker and decode the RGB of the PNG top-left pixel as six lowercase hex digits. It must send a complete note with both results from the same child; publish both results from that note. Inspect only the manifest yourself; never open the files or provide a guess."
	a := discord.TeammateAdmission{Discussion: thread, OperationID: "input-301", Revision: "rev-301", Text: body, AttachmentManifest: f.Manifest, Route: discord.Route{ChannelID: thread.Route.ChannelID, MessageID: "301"}, Author: discord.Actor{ID: "401", Name: "fixture"}, InvokerID: "401", Authority: discord.AuthorityOwnerDirect}
	if err = r.Admit(ctx, a, reader); err != nil {
		t.Fatal(err)
	}
	wait("same worker reported document marker and decoded image pixel", 4*time.Minute, func() bool {
		for _, p := range *outputs {
			if !strings.HasPrefix(p.AdmissionID, "worker-message-") || p.JobID == "" || p.AttemptID == "" || !strings.Contains(p.Body, f.Document) || !strings.Contains(strings.ToLower(p.Body), f.Pixel) {
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
						t.Logf("attachment worker %s instance %s attempt %s note %s publication %s", n.Session, n.Instance, n.AttemptID, n.ID, p.ID)
						return true
					}
				}
			}
		}
		return false
	})
}
