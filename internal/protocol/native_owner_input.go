package protocol

// OwnerSessionCapabilitySource returns only a retained, nonblocking owner roster
// snapshot. A cold/expired snapshot is unavailable while bounded refresh runs.
type OwnerSessionCapabilitySource interface {
	OwnerSessionCapabilities(local string, shimPID int, shimStart int64) (SessionCapabilities, bool)
}
