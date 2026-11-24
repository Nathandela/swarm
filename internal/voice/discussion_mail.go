package voice

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/workermail"
)

var errSupersededWorkerMail = errors.New("worker mailbox: superseded attempt")

func (c *Controller) ingestWorkerMail(ctx context.Context, st State) (bool, error) {
	if !filepath.IsAbs(c.managerSettingsDir) {
		return false, nil
	}
	notes, err := workermail.Pending(c.managerSettingsDir, c.binding.ConversationID)
	if err != nil {
		return false, err
	}
	for _, n := range notes {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		ep, _, ok := protocol.ParseID(n.Session)
		managerEndpoint, _, _ := protocol.ParseID(c.binding.SessionID)
		if !ok || ep != managerEndpoint || n.Discussion != c.binding.ConversationID {
			return false, ErrStale
		}
		worker := WorkerAssociation{VMChild: true, Mailbox: true, ConversationID: n.Discussion, Parent: n.Parent, Session: n.Session, SessionInstance: n.Instance, Project: n.Project, AttemptID: n.Attempt, JobID: "vm-child-" + hashValue([]string{n.Discussion, n.Session})}
		linkVMWorkerInput(st, &worker, n.ParentInputHash)
		meta := workerNoteMetadata{Files: n.Files, Session: n.Session, Instance: n.Instance, Agent: n.Agent, Text: n.Text, NoteID: n.ID, Sequence: n.Sequence, Created: &n.Created, Kind: n.Kind}
		if _, err = c.queueWorkerNote(worker, workerMessageInput{ID: n.ID, Text: n.Text, Kind: n.Kind}, meta); err != nil {
			if errors.Is(err, errSupersededWorkerMail) {
				if err = workermail.Reject(c.managerSettingsDir, n); err != nil {
					return false, err
				}
				continue
			}
			return false, err
		}
		if err = workermail.Acknowledge(c.managerSettingsDir, n, ""); err != nil {
			return false, err
		}
	}
	for _, wake := range st.Wakes {
		settleFiles := wake.State == "settled" && !wake.MailboxFilesSettled
		exportReply := wake.WorkerReply != "" && !wake.WorkerReplyExported
		if !settleFiles && !exportReply {
			continue
		}
		event := st.Events[key(c.binding.ConversationID, wake.EventID)]
		var meta workerNoteMetadata
		if !event.WorkerMessage || json.Unmarshal(event.Metadata, &meta) != nil || meta.NoteID == "" {
			continue
		}
		worker := st.Workers[key(c.binding.ConversationID, key(meta.Session, meta.Instance))]
		if !worker.supervisorMailbox() {
			continue
		}
		if exportReply {
			if err = c.exportWorkerAnswer(worker, meta, wake, wake.WorkerReply); err != nil {
				return false, err
			}
		}
		if settleFiles && len(meta.Files) > 0 {
			binding := workermail.Binding{Discussion: worker.ConversationID, Parent: worker.Parent, Session: worker.Session, Instance: worker.SessionInstance, Attempt: worker.AttemptID}
			var unpublished []string
			for _, p := range st.Publications {
				if p.ConversationID == worker.ConversationID && p.Admission == wake.Admission && p.DiscussionFiles != "" && !p.Exported {
					unpublished = append(unpublished, p.ID)
				}
			}
			if err = workermail.SettleFiles(c.managerSettingsDir, binding, meta.NoteID, unpublished); err != nil && !errors.Is(err, workermail.ErrAbsent) {
				return false, err
			}
			if err = c.store.update(func(current *State) error {
				if err := checkBinding(current, c.binding); err != nil {
					return err
				}
				k := key(c.binding.ConversationID, wake.ID)
				cur := current.Wakes[k]
				if cur.ID == wake.ID && cur.State == "settled" && !cur.MailboxFilesSettled {
					cur.MailboxFilesSettled = true
					putRecord(current, "wakes", &current.Wakes, k, cur)
				}
				return nil
			}); err != nil {
				return false, err
			}
		}
	}
	return len(notes) > 0, nil
}
func (c *Controller) exportWorkerAnswer(w WorkerAssociation, meta workerNoteMetadata, wake Wake, answer string) error {
	n := workermail.Note{Binding: workermail.Binding{Discussion: w.ConversationID, Session: w.Session, Instance: w.SessionInstance, Attempt: w.AttemptID}, ID: meta.NoteID, Sequence: meta.Sequence}
	if err := workermail.Acknowledge(c.managerSettingsDir, n, answer); err != nil {
		return err
	}
	return c.store.update(func(st *State) error {
		if err := checkBinding(st, c.binding); err != nil {
			return err
		}
		k := key(c.binding.ConversationID, wake.ID)
		current := st.Wakes[k]
		if current.WorkerReply != answer {
			return ErrConflict
		}
		if !current.WorkerReplyExported {
			current.WorkerReplyExported = true
			putRecord(st, "wakes", &st.Wakes, k, current)
		}
		return nil
	})
}
