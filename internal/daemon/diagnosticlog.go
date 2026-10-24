package daemon

import "github.com/Nathandela/swarm/internal/diagnosticlog"

func (d *Daemon) startLogMaintenance() error {
	if d.cfg.LogPath == "" {
		return nil
	}
	f, err := diagnosticlog.Open(d.cfg.LogPath)
	if err != nil {
		return err
	}
	stop, err := diagnosticlog.Start(d.cfg.LogPath, f)
	if err != nil {
		_ = f.Close()
		return err
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		<-d.stopCh
		stop()
		_ = f.Close()
	}()
	return nil
}
