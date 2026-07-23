package protocol

// DeviceLister is the optional interface a DaemonAPI implements to expose
// device_list (backed by device.Registry.List() in production): the Server
// serves device_list only when the `pairing` capability was negotiated AND the
// backend implements this (mirrors journalBackend()'s cap+type-assert gate).
// device_list is a READ, so it does not touch requireRemoteAuthz.
type DeviceLister interface {
	ListDevices() []DeviceView
}

// PolicyDescriber is the optional interface a DaemonAPI implements to expose
// policy_query (backed by the remote launch policy's configured cwd roots);
// gated the same way on the `policy` capability. policy_query is a READ, so it
// does not touch requireRemoteAuthz.
type PolicyDescriber interface {
	DescribePolicy() PolicyView
}

// DeviceRevoker is the optional interface a DaemonAPI implements to expose
// device_revoke (slice A3.2, backed by device.Registry.Remove in production):
// RevokeDevice removes the TARGET device (Control.TargetDeviceID) from the
// registry. Unlike DeviceLister/PolicyDescriber this is a MUTATING op, so it goes
// through requireRemoteAuthz like kill/delete before RevokeDevice is called.
type DeviceRevoker interface {
	RevokeDevice(deviceID string) (bool, error)
}
