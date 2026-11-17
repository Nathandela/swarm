package voicewire

import (
	"encoding/json"
	"testing"
)

func TestDeviceStatusPageRejectsForeignAndNoncontiguousHistory(t *testing.T) {
	base := DeviceStatus{PlaceID: "p", Provider: "claude", Generation: 2, ConversationID: "conv"}
	data, _ := json.Marshal(base)
	prior := Cursor{StreamID: "stream", Sequence: 4}
	next := Cursor{StreamID: "stream", Sequence: 5}
	makePage := func() DeviceStatusPage {
		return DeviceStatusPage{Version: ProtocolVersion, Head: next, Events: []ServerEvent{{Type: "status", Cursor: next, SessionID: "device", Data: data}}}
	}
	raw, _ := json.Marshal(makePage())
	result, cursor, done, err := DecodeDeviceStatusPage(raw, "device", "p", prior, base)
	if err != nil || result != base || cursor != next || !done {
		t.Fatalf("valid page: %+v %v %v %v", result, cursor, done, err)
	}
	for name, change := range map[string]func(*DeviceStatusPage){
		"foreign device":    func(p *DeviceStatusPage) { p.Events[0].SessionID = "other" },
		"different stream":  func(p *DeviceStatusPage) { p.Head.StreamID = "other" },
		"unsupported major": func(p *DeviceStatusPage) { p.Version++ },
		"skipped update":    func(p *DeviceStatusPage) { p.Head.Sequence++; p.Events[0].Cursor = p.Head },
		"unqualified epoch": func(p *DeviceStatusPage) {
			changed := base
			changed.Generation++
			p.Events[0].Data, _ = json.Marshal(changed)
		},
		"raw text":      func(p *DeviceStatusPage) { p.Events[0].Text = "private text" },
		"raw audio":     func(p *DeviceStatusPage) { p.Events[0].Type = TypeAudio },
		"missing delta": func(p *DeviceStatusPage) { p.Events = nil },
	} {
		t.Run(name, func(t *testing.T) {
			p := makePage()
			change(&p)
			raw, _ := json.Marshal(p)
			if _, _, _, err := DecodeDeviceStatusPage(raw, "device", "p", prior, base); err == nil {
				t.Fatal("accepted inconsistent history")
			}
		})
	}
}
