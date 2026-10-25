package tui

import "testing"

func TestMobileReviewHiddenWorkingAnimationStops(t *testing.T) {
	for _, overlay := range []string{"pairing", "confirm"} {
		t.Run(overlay, func(t *testing.T) {
			m := mobileFrameFixture(t, 32, 8)
			if overlay == "pairing" {
				m = send(m, pairPendingMsg{DeviceName: "phone", SAS: []string{"one", "two"}})
			} else {
				m = send(m, keyCtrlX)
			}
			next, cmd := m.Update(workingAnimationMsg{})
			if cmd != nil || next.(rootModel).animatingWorking {
				t.Fatal("hidden working indicators kept their animation timer")
			}
		})
	}
}
