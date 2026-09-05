package swarmmobile

// MachinePresence is retained for bind-surface compatibility. Relay-v2 has no
// presence RPC, so the phone never invents reachability from relay connectivity;
// MachineFreshness is the authenticated liveness signal.
func (a *App) MachinePresence() (p *MachinePresence, err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return nil, err
	}
	return &MachinePresence{State: "unknown"}, nil
}
