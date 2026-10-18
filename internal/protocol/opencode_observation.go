package protocol

import "github.com/Nathandela/swarm/internal/protocol/schema"

const CapOpenCodeObservation = "opencode-observation"

type OpenCodeObservationView = schema.OpenCodeObservationView

type OpenCodeObservationBackend interface {
	OpenCodeObservation(sessionID string) (OpenCodeObservationView, bool)
}
