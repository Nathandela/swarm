package daemon

import "errors"

type bootstrapLaunchNoStart struct{ operationID string }

func (*bootstrapLaunchNoStart) Error() string { return "bootstrap_launch_not_started" }

// BootstrapLaunchNotStarted certifies only a causally received pre-start refusal
// on the ONE direct call authorized by a fresh durable bootstrap claim.
func BootstrapLaunchNotStarted(operationID string) error {
	return &bootstrapLaunchNoStart{operationID: operationID}
}
func BootstrapLaunchDidNotStart(err error, operationID string) bool {
	var refusal *bootstrapLaunchNoStart
	return operationID != "" && errors.As(err, &refusal) && refusal.operationID == operationID
}
