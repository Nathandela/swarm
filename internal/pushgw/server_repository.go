package pushgw

import (
	"context"
	"time"
)

func (s *Server) getInstallation(ctx context.Context, id string) (installationRecord, bool, error) {
	if s.v2store != nil {
		rec, found, err := s.v2store.p.getInstallation(ctx, id)
		if err == nil && found && installationExpired(rec.LastActiveMs, s.now()) {
			return installationRecord{}, false, nil
		}
		return rec, found, err
	}
	return s.store.getInstallation(id)
}

func (s *Server) getAddress(ctx context.Context, address string) (addressRecord, bool, error) {
	if s.v2store != nil {
		return s.v2store.p.getAddress(ctx, address)
	}
	return s.store.getAddress(address)
}

func (s *Server) claimNonce(ctx context.Context, installationID string, expectedPublicKey []byte, nonce string, now, expiry time.Time) (bool, error) {
	if s.v2store != nil {
		return s.v2store.p.claimNonceAndTouch(ctx, installationID, expectedPublicKey, nonce, now, expiry)
	}
	if !s.nonces.checkAndStore(installationID, nonce, now, expiry) {
		return false, nil
	}
	return true, s.store.touchInstallation(installationID, now.UnixMilli())
}

func (s *Server) allow(ctx context.Context, key string, rate RateLimit, now time.Time) (bool, int, error) {
	if s.v2store != nil {
		return s.v2store.p.allow(ctx, key, rate, now)
	}
	ok, retry := s.limiter.allow(key, rate, now)
	return ok, retry, nil
}

func (s *Server) encryptToken(token string) ([]byte, string, error) {
	if s.v2store != nil {
		return s.v2store.encrypt(token)
	}
	enc, err := s.store.encrypt(token)
	return enc, "local", err
}

func (s *Server) decryptToken(enc []byte, version string) (string, error) {
	if s.v2store != nil {
		return s.v2store.decrypt(enc, version)
	}
	return s.store.decrypt(enc)
}
