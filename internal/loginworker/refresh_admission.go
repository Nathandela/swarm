package loginworker

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

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
	var failures uint8
	var retryAfter time.Time
	if old, err := readRefreshProgress(f); err == nil {
		if (old.Phase != PhaseReady && old.Phase != PhaseFailed) || !old.WritersStopped || old.Worker.Alive() || old.Native.Alive() {
			return RefreshProgress{}, logins.ErrPending
		}
		raw, err := readPrivate(f.root, refreshConfigFile, 16<<10)
		if errors.Is(err, os.ErrNotExist) {
			return RefreshProgress{}, logins.ErrPending
		}
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
		switch {
		case c.Grant.LoginID == old.Grant.LoginID && c.Grant.Identity == old.Grant.Identity && c.Grant.Owner == old.Grant.Owner && c.Grant.CredentialRevision > old.Grant.CredentialRevision:
			// The caller holds current grant custody; only a committed relogin advances revision.
		case old.Phase == PhaseReady && old.Output != nil && *old.Output == c.Grant:
		case old.Phase == PhaseFailed && old.Grant == c.Grant && retryableRefreshFailure(old.ErrorCode) && old.Failures > 0 && !old.RetryAfter.IsZero() && !time.Now().Before(old.RetryAfter):
			failures, retryAfter = old.Failures, old.RetryAfter
		default:
			return RefreshProgress{}, logins.ErrPending
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return RefreshProgress{}, err
	} else if _, err := f.root.Lstat(refreshConfigFile); err == nil {
		return RefreshProgress{}, logins.ErrPending
	} else if !errors.Is(err, os.ErrNotExist) {
		return RefreshProgress{}, err
	}
	p := RefreshProgress{Version: refreshRecordVersion, Grant: c.Grant, Creator: creator, LaunchNonce: c.LaunchNonce, Generation: c.Generation, Phase: refreshPrepared, Failures: failures, RetryAfter: retryAfter}
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

// These outcomes permit a settled-failure retry, not a claim of transience or revocation.
func retryableRefreshFailure(code string) bool {
	switch code {
	case "deadline_exceeded", "canceled", "native_context_canceled", "refresh_native_start_failed", "refresh_native_exited", "refresh_transport_failed", "refresh_initialize_failed", "refresh_initialized_failed", "refresh_account_read_failed":
		return true
	}
	return false
}

func recordRefreshFailure(p *RefreshProgress, current logins.CodexRefreshRef) {
	// An unqualified completion consumes any prior gate without resetting its budget.
	p.RetryAfter = time.Time{}
	if !p.WritersStopped || current != p.Grant || !retryableRefreshFailure(p.ErrorCode) {
		return
	}
	if p.Failures < 5 {
		p.Failures++
	}
	delay := time.Minute << (p.Failures - 1)
	if delay > 15*time.Minute {
		delay = 15 * time.Minute
	}
	p.RetryAfter = time.Now().Add(delay).UTC()
}
