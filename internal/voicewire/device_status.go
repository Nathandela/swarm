package voicewire

import (
	"encoding/json"
	"errors"
)

var errDeviceStatusPage = errors.New("voice: invalid signed-device status page")

const MaxDeviceStatusPage = 16

// DeviceStatusPage carries one signed request's poll result. A reset contains
// exactly one durable snapshot at Head. Other pages replay strictly after the requested cursor.
type DeviceStatusPage struct {
	Version int           `json:"version"`
	Head    Cursor        `json:"head"`
	Reset   bool          `json:"reset"`
	Events  []ServerEvent `json:"events"`
}

// DecodeDeviceStatusPage reuses the frame decoder for every event before the client advances.
func DecodeDeviceStatusPage(raw json.RawMessage, session, place string, prior Cursor, base DeviceStatus) (DeviceStatus, Cursor, bool, error) {
	type pageHeader struct {
		Version int               `json:"version"`
		Head    Cursor            `json:"head"`
		Reset   bool              `json:"reset"`
		Events  []json.RawMessage `json:"events"`
	}
	var page pageHeader
	if err := strictDecode(raw, &page); err != nil {
		return DeviceStatus{}, Cursor{}, false, err
	}
	invalid := func() (DeviceStatus, Cursor, bool, error) {
		return DeviceStatus{}, Cursor{}, false, errDeviceStatusPage
	}
	if page.Version != ProtocolVersion || page.Head.Validate() != nil || len(page.Events) > MaxDeviceStatusPage {
		return invalid()
	}
	if page.Reset {
		if len(page.Events) != 1 || (prior.StreamID == page.Head.StreamID && page.Head.Sequence <= prior.Sequence) {
			return invalid()
		}
	} else if prior.Validate() != nil || page.Head.StreamID != prior.StreamID || page.Head.Sequence < prior.Sequence {
		return invalid()
	}
	for _, rawEvent := range page.Events {
		event, err := DecodeServerEvent(rawEvent)
		if err != nil || event.SessionID != session || event.Cursor.StreamID != page.Head.StreamID || event.Cursor.Sequence > page.Head.Sequence ||
			event.MediaEpoch != 0 || event.Text != "" || event.State != "" || event.UtteranceID != "" {
			return invalid()
		}
		if page.Reset {
			if event.Type != "snapshot" || event.Cursor != page.Head {
				return invalid()
			}
		} else if (event.Type != "status" && event.Type != "admission") || event.Cursor.Sequence != prior.Sequence+1 {
			return invalid()
		}
		var next DeviceStatus
		if err := strictDecode(event.Data, &next); err != nil || next.Generation == 0 || next.PlaceID != place || next.ConversationID == "" || (next.Provider != "claude" && next.Provider != "codex") {
			return invalid()
		}
		if prior.StreamID != "" {
			if prior.StreamID == event.Cursor.StreamID {
				if next.Generation != base.Generation || next.ConversationID != base.ConversationID || next.Provider != base.Provider {
					return invalid()
				}
			} else if !page.Reset || next.Generation <= base.Generation {
				return invalid()
			}
		}
		base = next
		prior = event.Cursor
	}
	if len(page.Events) == 0 && prior != page.Head {
		return invalid()
	}
	return base, prior, prior == page.Head, nil
}
