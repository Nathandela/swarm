package skeleton

import (
	"maps"

	"github.com/Nathandela/swarm/internal/persist"
)

// Records read from the journal are immutable until their replacement is
// visible. Mutable incident budgets and leases must not alias that old record.
func cloneAccountRotation(rec accountRotationRecord) accountRotationRecord {
	rec.Incident.TriedAccounts = maps.Clone(rec.Incident.TriedAccounts)
	if rec.Destination != nil {
		value := *rec.Destination
		rec.Destination = &value
	}
	if rec.Trial != nil {
		value := *rec.Trial
		rec.Trial = &value
	}
	if rec.Manifest != nil {
		value := *rec.Manifest
		value.Files = append([]accountHistoryFile(nil), value.Files...)
		value.PreviousFiles = append([]accountHistoryFile(nil), value.PreviousFiles...)
		rec.Manifest = &value
	}
	if rec.ExpectedCLIIdentity != nil {
		value := *rec.ExpectedCLIIdentity
		rec.ExpectedCLIIdentity = &value
	}
	return rec
}

func cloneAccountHistoryOwnership(ownership accountHistoryOwnership) accountHistoryOwnership {
	ownership.ProfileFiles = maps.Clone(ownership.ProfileFiles)
	for key, files := range ownership.ProfileFiles {
		ownership.ProfileFiles[key] = append([]accountHistoryFile(nil), files...)
	}
	return ownership
}

// A canonical launch result can belong to the owner. Only immutable launch
// custody gives recovery authority to adopt, release, or stop that process.
func ownedAccountCandidate(rec accountRotationRecord, candidate persist.Meta) bool {
	return candidate.ID != "" && candidate.ID != rec.SourceID &&
		candidate.AgentType == rec.SourceBinding.Provider && candidate.ResumedFrom == rec.SourceID &&
		rec.Destination != nil && candidate.AccountBinding != nil && *candidate.AccountBinding == *rec.Destination &&
		candidate.InputEmbargo == rec.Incident.ID && candidate.ConversationID == rec.ConversationID &&
		validCLIIdentity(candidate.CLIIdentity) && rec.ExpectedCLIIdentity != nil && *candidate.CLIIdentity == *rec.ExpectedCLIIdentity
}
