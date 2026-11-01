package loginworker

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/Nathandela/swarm/internal/logins"
)

const refreshRecordVersion = 2
const refreshPrepared = "prepared"
const refreshClaimed = "claimed"

func decodeRefreshRecord(raw []byte, into any) error {
	var header struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(raw, &header) != nil {
		return ErrInvalid
	}
	if header.Version == 0 {
		return logins.ErrPending
	}
	if header.Version != refreshRecordVersion {
		return ErrUnsupported
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(into) != nil || decoder.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	canonical, err := json.Marshal(into)
	if err != nil || !bytes.Equal(canonical, raw) {
		return ErrInvalid
	}
	return nil
}

// prepareCodexRefresh is called with the existing per-login worker lock held.
func prepareCodexRefresh(f *jobFiles, c RefreshConfig, creator ProcessIdentity) (RefreshProgress, error) {
	if c.Version != refreshRecordVersion || !c.Grant.Valid() || c.Grant.LoginID != c.LoginID || creator.PID <= 0 || creator.StartTime <= 0 || !validID(c.LaunchNonce) || c.Generation == 0 {
		return RefreshProgress{}, ErrInvalid
	}
	if old, err := readRefreshProgress(f); err == nil {
		// Failed or uncertain attempts retain authority until retry/recovery is qualified.
		if old.Phase != PhaseReady || !old.WritersStopped || old.Worker.Alive() || old.Native.Alive() || old.Output == nil || *old.Output != c.Grant {
			return RefreshProgress{}, logins.ErrPending
		}
		raw, err := readPrivate(f.root, refreshConfigFile, 16<<10)
		if err != nil {
			return RefreshProgress{}, err
		}
		var previous RefreshConfig
		if err := decodeRefreshRecord(raw, &previous); err != nil {
			return RefreshProgress{}, err
		}
		if previous.Grant != old.Grant || previous.Generation != old.Generation || previous.LaunchNonce != old.LaunchNonce || previous.StateRoot != c.StateRoot || previous.LoginID != c.LoginID {
			return RefreshProgress{}, ErrStale
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return RefreshProgress{}, err
	} else if _, err := f.root.Lstat(refreshConfigFile); err == nil {
		return RefreshProgress{}, logins.ErrPending
	} else if !errors.Is(err, os.ErrNotExist) {
		return RefreshProgress{}, err
	}
	p := RefreshProgress{Version: refreshRecordVersion, Grant: c.Grant, Creator: creator, LaunchNonce: c.LaunchNonce, Generation: c.Generation, Phase: refreshPrepared}
	// The incompatible progress fence precedes config and fork, including crashes.
	if err := f.write(refreshProgressFile, p); err != nil {
		return p, err
	}
	if err := f.write(refreshConfigFile, c); err != nil {
		return p, err
	}
	return p, nil
}

func acquireRefreshGrant(s *logins.Store, c RefreshConfig) (*logins.GrantLease, error) {
	lease, err := s.AcquireCodexGrant(c.LoginID)
	if err != nil {
		return nil, err
	}
	current, err := lease.RefreshRef()
	if err != nil || current != c.Grant {
		_ = lease.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrStale
	}
	return lease, nil
}
