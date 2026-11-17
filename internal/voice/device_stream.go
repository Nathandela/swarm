package voice

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/Nathandela/swarm/internal/voicewire"
)

// Each authenticated device has a separate stream namespace. Only a compact
// public status projection is retained; the admission ledger remains authoritative.
type deviceStatusStreams struct {
	mu      sync.Mutex
	devices map[string]*deviceStatusStream
}

type deviceStatusStream struct {
	replay *VoiceStream
	latest voicewire.DeviceStatus
}

func (c *managerControl) deviceStatusPage(device, place string, body []byte) (voicewire.DeviceStatusPage, error) {
	var page voicewire.DeviceStatusPage
	msg, err := voicewire.DecodeClientMessage(body)
	session, scoped := strings.CutPrefix(msg.SessionID, device+".")
	if err != nil || !scoped || !deviceHex(session, 16) || msg.MediaEpoch != 0 ||
		(msg.Type != voicewire.TypeHandshake && msg.Type != voicewire.TypeResume) {
		return page, ErrInvalid
	}
	if msg.After != nil && (msg.After.Sequence == ^uint64(0) || msg.Sequence != msg.After.Sequence+1) {
		return page, ErrInvalid
	}
	streams := &c.statusStreams
	streams.mu.Lock()
	defer streams.mu.Unlock()
	if streams.devices == nil {
		streams.devices = make(map[string]*deviceStatusStream)
	}
	stream := streams.devices[device]
	if stream == nil {
		if len(streams.devices) >= maxDeviceRecords {
			return page, ErrCapacity
		}
		replay, err := NewVoiceStream()
		if err != nil {
			return page, err
		}
		stream = &deviceStatusStream{replay: replay, latest: voicewire.DeviceStatus{PlaceID: place}}
		streams.devices[device] = stream
	}
	if err := c.syncDeviceStatusesLocked(); err != nil {
		return page, err
	}
	var after VoiceCursor
	if msg.After != nil {
		after = VoiceCursor(*msg.After)
	}
	events, gap, current := stream.replay.Replay(after)
	page.Version = voicewire.ProtocolVersion
	page.Head = voicewire.Cursor(current)
	page.Reset = gap
	if gap {
		data, err := json.Marshal(stream.latest)
		if err != nil {
			return page, err
		}
		page.Events = []voicewire.ServerEvent{{Type: "snapshot", SessionID: msg.SessionID, Cursor: page.Head, Data: data}}
		return page, nil
	}
	for _, event := range events[:min(len(events), voicewire.MaxDeviceStatusPage)] {
		page.Events = append(page.Events, voicewire.ServerEvent{Type: event.Kind, Cursor: voicewire.Cursor(event.Cursor), SessionID: msg.SessionID, Data: event.Payload})
	}
	return page, nil
}

func (c *managerControl) syncDeviceStatuses() error {
	c.statusStreams.mu.Lock()
	defer c.statusStreams.mu.Unlock()
	return c.syncDeviceStatusesLocked()
}

func (c *managerControl) syncDeviceStatusesLocked() error {
	if len(c.statusStreams.devices) == 0 {
		return nil
	}
	c.capture.mu.Lock()
	activeCapture := c.capture.activeLocked()
	c.capture.mu.Unlock()
	st, err := c.store.readView()
	if err != nil {
		return err
	}
	if err := checkBinding(&st, c.binding); err != nil {
		return err
	}
	conv := st.Conversations[c.binding.ConversationID]
	latest := make(map[string]voicewire.DeviceAdmission, len(c.statusStreams.devices))
	for _, u := range st.Utterances {
		if u.ConversationID != c.binding.ConversationID || u.Admission == 0 {
			continue
		}
		device, ok := strings.CutPrefix(u.DeviceID, "device:")
		device = strings.TrimSuffix(device, ":ptt")
		if !ok || c.statusStreams.devices[device] == nil || latest[device].Sequence >= u.Admission {
			continue
		}
		if a, ok := st.Admissions[admissionKey(u.ConversationID, u.Admission)]; ok {
			latest[device] = voicewire.DeviceAdmission{Sequence: a.Sequence, Phase: a.Phase}
		}
	}
	for id, stream := range c.statusStreams.devices {
		receipt := latest[id]
		status := voicewire.DeviceStatus{
			PlaceID: stream.latest.PlaceID, ConversationID: c.binding.ConversationID, Provider: c.binding.Provider,
			Generation: c.binding.Generation, Ready: !conv.CustodyLost && c.controller != nil,
			SchedulerPaused: conv.SchedulerPaused, CustodyLost: conv.CustodyLost,
			AdmissionSequence: receipt.Sequence, AdmissionPhase: receipt.Phase,
		}
		switch {
		case activeCapture.ID == "":
			status.Capture = "idle"
		case activeCapture.Source == "device:"+id+":ptt":
			status.Capture = "owned"
		default:
			status.Capture = "busy"
		}
		if receipt.Sequence != 0 && status.AdmissionPhase == "" {
			status.AdmissionPhase = "queued"
		}
		if status == stream.latest {
			continue
		}
		raw, err := json.Marshal(status)
		if err != nil {
			return err
		}
		kind := "status"
		if stream.latest.Generation != 0 && (status.AdmissionSequence != stream.latest.AdmissionSequence || status.AdmissionPhase != stream.latest.AdmissionPhase) {
			kind = "admission"
		}
		if _, err = stream.replay.Append(kind, raw); err != nil {
			return err
		}
		stream.latest = status
	}
	return nil
}
