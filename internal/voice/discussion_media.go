package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/workermail"
)

func discordUserID(id string) bool {
	n, err := strconv.ParseUint(id, 10, 64)
	return err == nil && n != 0 && strconv.FormatUint(n, 10) == id
}

func (c *Controller) verifyPublicationRecipient(ctx context.Context, st State, id, messageID string) error {
	if id == "" && messageID == "" {
		return nil
	}
	if c.discussion == nil || c.discussion.GuildID == "" || !discordUserID(id) {
		return ErrInvalid
	}
	if messageID != "" && c.discussion.VerifyRecipient != nil {
		return c.discussion.VerifyRecipient(ctx, id, messageID)
	}
	for _, a := range st.Admissions {
		if a.ConversationID == c.binding.ConversationID && a.Origin != nil &&
			a.Origin.ActorID == id && c.discussion.validateOrigin(*a.Origin) {
			return nil
		}
	}
	return fmt.Errorf("%w: use the verified Discord participant ID and their message ID in this discussion", ErrNotFound)
}

func publicationWorkerNote(st State, conversation string, admission uint64) (workerNoteMetadata, workermail.Binding, error) {
	var note workerNoteMetadata
	a := st.Admissions[admissionKey(conversation, admission)]
	wakeID, ok := strings.CutPrefix(a.SourceID, "wake:")
	wake := st.Wakes[key(conversation, wakeID)]
	event := st.Events[key(conversation, wake.EventID)]
	if !ok || wake.Admission != admission || !event.WorkerMessage || json.Unmarshal(event.Metadata, &note) != nil ||
		note.Kind != "complete" || note.NoteID == "" || len(note.Files) == 0 {
		return note, workermail.Binding{}, fmt.Errorf("%w: use file IDs from the completion note for this turn", ErrNotFound)
	}
	w := st.Workers[key(conversation, key(note.Session, note.Instance))]
	if !w.supervisorMailbox() || w.JobID != event.JobID || w.AttemptID != event.AttemptID ||
		w.Session != note.Session || w.SessionInstance != note.Instance || w.ConversationID != conversation {
		return note, workermail.Binding{}, ErrStale
	}
	return note, workermail.Binding{Discussion: conversation, Parent: w.Parent, Session: w.Session, Instance: w.SessionInstance, Attempt: w.AttemptID}, nil
}

func selectPublicationFiles(note workerNoteMetadata, ids []string) ([]schema.PublicationFile, error) {
	files := make([]schema.PublicationFile, 0, len(ids))
	for _, id := range ids {
		i := slices.IndexFunc(note.Files, func(f schema.PublicationFile) bool { return f.ID == id })
		if i < 0 || slices.ContainsFunc(files, func(f schema.PublicationFile) bool { return f.ID == id }) {
			return nil, fmt.Errorf("%w: file ID is missing or repeated in this worker note", ErrInvalid)
		}
		files = append(files, note.Files[i])
	}
	return files, nil
}

func (d *DiscussionController) ReadPublicationFiles(ctx context.Context, output DiscussionOutput) ([][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	st, err := d.store.readView()
	if err != nil {
		return nil, err
	}
	p, ok := st.Publications[key(d.cfg.ConversationID, output.ID)]
	if !ok || p.DiscussionFiles != output.FileManifest || output.FileManifest == "" {
		return nil, ErrStale
	}
	note, binding, err := publicationWorkerNote(st, d.cfg.ConversationID, p.Admission)
	if err != nil {
		return nil, err
	}
	files := output.FileMetadata()
	if len(files) == 0 {
		return nil, ErrCorrupt
	}
	contents := make([][]byte, len(files))
	for i, f := range files {
		contents[i], err = workermail.ReadFile(d.scope.ManagerSettingsDir, binding, note.NoteID, f.ID)
		if err != nil {
			return nil, fmt.Errorf("could not read staged worker file %q: %w", f.Name, err)
		}
	}
	return contents, nil
}

func (o DiscussionOutput) FileMetadata() []schema.PublicationFile {
	var files []schema.PublicationFile
	if o.FileManifest != "" {
		_ = json.Unmarshal([]byte(o.FileManifest), &files)
	}
	return files
}

func publicationOutput(p Publication) DiscussionOutput {
	return DiscussionOutput{DiscussionOutputSource: p.DiscussionSource, ID: p.ID, Text: p.Text, Sequence: p.OutputSequence, FileManifest: p.DiscussionFiles, RecipientID: p.RecipientID}
}

func questionOutput(r Request, body string) DiscussionOutput {
	return DiscussionOutput{DiscussionOutputSource: r.DiscussionSource, ID: r.ID, Text: body, Sequence: r.OutputSequence, QuestionID: r.ID, QuestionHash: r.Hash, RecipientID: r.RecipientID}
}

func (d *DiscussionController) ReleasePublicationFiles(ctx context.Context, output DiscussionOutput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	st, err := d.store.readView()
	if err != nil {
		return err
	}
	p, ok := st.Publications[key(d.cfg.ConversationID, output.ID)]
	if !ok || p.DiscussionFiles != output.FileManifest || output.FileManifest == "" {
		return ErrStale
	}
	note, binding, err := publicationWorkerNote(st, d.cfg.ConversationID, p.Admission)
	if err != nil {
		return err
	}
	return workermail.ReleaseFiles(d.scope.ManagerSettingsDir, binding, note.NoteID, p.ID)
}

func (c *Controller) publishWorkerFiles(permit SpeechPermit, p Publication, ids []string) (Publication, error) {
	if c.discussion == nil || !c.discussion.VMProjectAccess {
		return Publication{}, ErrExpired
	}
	c.metadata.Lock()
	defer c.metadata.Unlock()
	st, err := c.store.readView()
	if err != nil {
		return Publication{}, err
	}
	note, binding, err := publicationWorkerNote(st, c.binding.ConversationID, p.Admission)
	if err != nil {
		return Publication{}, err
	}
	files, err := selectPublicationFiles(note, ids)
	if err != nil {
		return Publication{}, err
	}
	raw, err := json.Marshal(files)
	if err != nil {
		return Publication{}, err
	}
	p.DiscussionFiles = string(raw)
	recordKey := key(c.binding.ConversationID, p.ID)
	if st.Publications[recordKey].ID != "" {
		return c.store.PublishSpeech(c.binding, permit, p)
	}
	if err := workermail.PinFiles(c.managerSettingsDir, binding, note.NoteID, p.ID, ids); err != nil {
		return Publication{}, err
	}
	published, err := c.store.PublishSpeech(c.binding, permit, p)
	if err != nil {
		// A lost write acknowledgement must keep the lease if its publication exists.
		if current, readErr := c.store.readView(); readErr == nil && current.Publications[recordKey].ID == "" {
			err = errors.Join(err, workermail.ReleaseFiles(c.managerSettingsDir, binding, note.NoteID, p.ID))
		}
	}
	return published, err
}
