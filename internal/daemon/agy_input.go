package daemon

import (
	"errors"
	"github.com/Nathandela/swarm/internal/agyinput"
)

func (d *Daemon) NativeInputEnabled() bool {
	return d.cfg.NativeInputEnabled != nil && d.cfg.NativeInputEnabled()
}

func (d *Daemon) SessionAgyInput(id string) (agyinput.Descriptor, error) {
	meta, ok := d.Get(id)
	if !ok || meta.AgentType != "agy" || meta.CLIIdentity == nil {
		return agyinput.Descriptor{}, errors.New("agy native input requires registration from its own native tool context or an explicit refresh")
	}
	desc, err := agyinput.ReadDescriptor(d.sessionDir(id))
	if err != nil || desc.SessionID != id || desc.ShimPID != meta.ShimPID || desc.ShimStartTime != meta.ShimStartTime || desc.Executable != meta.CLIIdentity.Path || desc.Fingerprint != meta.CLIIdentity.Fingerprint || desc.Version != meta.CLIIdentity.Version {
		return agyinput.Descriptor{}, errors.New("agy native input is not proven for this worker; explicitly refresh it")
	}
	return desc, nil
}
