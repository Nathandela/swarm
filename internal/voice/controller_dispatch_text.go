package voice

import (
	"encoding/json"
	"strings"

	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/protocol"
)

func (c *Controller) managerDispatchText(st State, a Admission, nonce string) (string, error) {
	input, err := c.admissionText(st, a)
	if err != nil {
		return "", err
	}
	var prefix string
	if c.managerSettingsDir != "" {
		prompt, err := LoadManagerStartingPrompt(c.managerSettingsDir)
		if err != nil {
			return "", err
		}
		if prompt != "" {
			prefix = "Current owner settings (supersede earlier settings)\n" + prompt + "\n\nCurrent input\n"
		}
	}
	if a.RecoveryOnly {
		prefix = recoveryInputNotice + prefix
	}
	if c.discussion == nil {
		return prefix + input, nil
	}

	provider := c.binding.Provider
	handoff := st.Conversations[c.binding.ConversationID].NeedsHandoff
	if note, mailbox := discussionDispatchNote(st, a); note.NoteID != "" {
		// Reserve the whole envelope before choosing an inline note or preview.
		// A still-prepared admission can become recovery-only before it runs.
		priorFinal := discussionPriorFinalNotice(st, a, "", provider, nonce)
		if provider == "claude" {
			prefix = escapeManagerEnvelopeMarker(prefix)
		}
		budget := managerDispatchLimit(provider) - len(prefix) - len(priorFinal)
		if !a.RecoveryOnly {
			budget -= len(recoveryInputNotice)
		}
		if handoff {
			budget -= len("\n\n" + discussionHandoffGuidance)
		}
		raw, _ := json.Marshal(note)
		input, err = workerNoteWakeText(mailbox, note, raw, budget)
		if err != nil {
			return "", err
		}
		input += priorFinal
	} else if strings.HasPrefix(a.SourceID, "wake:") {
		input = discussionPriorFinalNotice(st, a, prefix+input, provider, nonce)
		prefix = ""
	}
	text := prefix + input
	if handoff {
		text = discussionHandoff(st, a, text, provider, nonce, c.discussion.VMProjectAccess)
	}
	return text, nil
}

func managerDispatchLimit(provider string) int {
	if provider == "claude" {
		return min(protocol.MaxSendInputText, claudeinput.MaxManagerTextBytes)
	}
	return protocol.MaxSendInputText
}

func managerDispatchFits(provider, nonce, text string) bool {
	if provider == "claude" {
		_, err := claudeinput.ManagerEnvelope(nonce, text)
		return err == nil
	}
	return len(text) <= protocol.MaxSendInputText
}

func discussionDispatchNote(st State, a Admission) (workerNoteMetadata, bool) {
	id, fromWake := strings.CutPrefix(a.SourceID, "wake:")
	if !fromWake {
		return workerNoteMetadata{}, false
	}
	wake := st.Wakes[key(a.ConversationID, id)]
	event := st.Events[key(a.ConversationID, wake.EventID)]
	var note workerNoteMetadata
	if wake.Admission != a.Sequence || event.WakeID != wake.ID || !event.WorkerMessage ||
		json.Unmarshal(event.Metadata, &note) != nil || note.NoteID == "" {
		return workerNoteMetadata{}, false
	}
	worker := st.Workers[key(a.ConversationID, key(note.Session, note.Instance))]
	return note, worker.Mailbox
}
