package schema

import "fmt"

// ContextGuardAutoCompact is the daemon-global policy the owner may configure for
// the context guard. ThresholdPercent remains meaningful while disabled so an owner
// can prepare a policy before enabling it.
type ContextGuardAutoCompact struct {
	Enabled          bool `json:"enabled"`
	ThresholdPercent int  `json:"threshold_percent"`
}

// ContextGuardSettings is the versioned daemon-global context-guard settings
// document and the reply body of context_guard_get/context_guard_set.
type ContextGuardSettings struct {
	SchemaVersion int                     `json:"schema_version"`
	Revision      uint64                  `json:"revision"`
	AutoCompact   ContextGuardAutoCompact `json:"auto_compact"`
}

// ContextGuardSettingsSetReq is the compare-and-swap request body. The caller
// supplies the revision it read; a changed revision is refused, never merged.
type ContextGuardSettingsSetReq struct {
	ExpectedRevision uint64                  `json:"expected_revision"`
	AutoCompact      ContextGuardAutoCompact `json:"auto_compact"`
}

// ValidateContextGuardAutoCompact is shared by the protocol's rejection boundary
// and the durable store. Keeping this rule here prevents an invalid owner request
// from reaching one backend but not another.
func ValidateContextGuardAutoCompact(autoCompact ContextGuardAutoCompact) error {
	if autoCompact.ThresholdPercent < 40 || autoCompact.ThresholdPercent > 95 {
		return fmt.Errorf("auto_compact.threshold_percent %d is outside 40..95", autoCompact.ThresholdPercent)
	}
	return nil
}
