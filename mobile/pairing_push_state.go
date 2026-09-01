package swarmmobile

// recoverPairingPushOwnership completes the idempotent second leg of the phone's
// cross-file pairing transaction. The ownership phase lives in the SAME durable state
// write as the machine pin (phonecore.MutateAndOwnStagedPushBinding), so its presence is
// unambiguous after process death: this exact staged address must be retained, never
// revoked. CompleteOwnedStagedPushBinding persists the push-store disposition before it
// clears the write-ahead phase, making a crash between those writes safe to replay.
func (a *App) recoverPairingPushOwnership() error {
	addr, found, err := a.core.PairingPushOwnership()
	if err != nil || !found {
		return err
	}
	return a.core.CompleteOwnedStagedPushBinding(addr)
}
