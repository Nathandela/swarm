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

// DeviceRegranter is the optional interface a DaemonAPI implements to expose
// device_regrant (PB-KEY-3 / PB-KEY-4, backed by skeleton.coreAPI.RegrantDevice in
// production): mint a fresh sealed grant for a still-registered device under the CURRENT
// machine epoch and converge its record onto it.
//
// It is the ONLY exit from a lost grant. The relay purges mailbox items past its retention
// cap even when never acked, and re-pairing is refused outright while a device is
// registered, so without this a phone that never received its grant -- or slept through a
// rotation -- is recoverable only by physical access to the machine.
type DeviceRegranter interface {
	RegrantDevice(deviceID string) error
}

// DeviceRegistrar is the optional interface a DaemonAPI implements to report whether a
// device is still registered (present in the pinned device registry, backed by
// device.Registry.Get in production). controlGateOpen re-checks it on EVERY remote
// keystroke against the LEASE-ESTABLISHING device recorded on the control session, so a
// revoked device's live lease severs immediately — the per-keystroke, daemon-side defense
// that holds even when a device_revoke was handled by a DIFFERENT Server sharing the same
// backend registry (the production owner/remote split). It is consulted only when the
// backend implements it (skipped when absent, exactly like KillSwitch), so a backend
// without a registry is behavior-unchanged. This is the C1 [UNANIMOUS BLOCKER] fix's
// per-keystroke half; handleDeviceRevoke adds the proactive-release half.
type DeviceRegistrar interface {
	DeviceRegistered(deviceID string) bool
}

// InteractionApprover is the optional interface a DaemonAPI implements to expose approve
// (IS-LIFE-4, backed by skeleton.Daemon.approveInteraction in production): it validates ONE
// arriving approve against the stored ADR-007 D7 binding tuple -- agent instance, content
// hash, daemon-authoritative expiry -- and the decision vocabulary the request actually
// offered, then resolves the approval.
//
// It returns an ErrorCode BESIDE the error rather than only prose, because the refusal is the
// interesting outcome: a stale card and a decision that was never offered are different facts
// with different phone-side remedies, and D10's taxonomy is what carries the difference. An
// empty code with a non-nil error is a refusal the phone can only render, which is the shape
// confirmLease's own refusals already take.
//
// Like DeviceRevoker this is MUTATING, so handleApprove puts it behind requireRemoteAuthz. A
// backend that does not implement it refuses the op outright: replying OK to a decision
// nothing applied would dismiss the card on every surface (IS-LIFE-2) while the CLI stays
// blocked.
type InteractionApprover interface {
	ApproveInteraction(machine, operationID string, req ApproveReq) (ErrorCode, error)
}
