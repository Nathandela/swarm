package relay

// The R2 relay operator secret (playbook 6.5): "generated high-entropy relay
// operator secret/instance identity for diagnostic/admin authority; it is not a
// substitute for Web-PKI server authentication." Generation and 0600
// persistence live here; the `swarm relay doctor` capability that CONSUMES it
// is a separate R2 slice and simply reads the same file back.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// operatorSecretBytes is the raw entropy of a generated secret: 32 bytes (256
// bits), hex-encoded to 64 characters.
const operatorSecretBytes = 32

// WithOperatorSecret installs the relay operator secret (playbook 6.5) on a
// Server at construction, the same seam WithPushSink uses for the push
// transport. A nil/empty secret leaves diagnostic/admin authority disabled --
// consumed by the `swarm relay doctor` capability (a separate R2 slice), never
// by this file, and never logged by anything that touches it.
func WithOperatorSecret(secret []byte) Option {
	return func(s *Server) { s.operatorSecret = secret }
}

// EnsureOperatorSecret returns the relay operator secret at path, generating
// and persisting a fresh high-entropy one at 0600 if the file does not yet
// exist. An existing file's contents are reused unchanged -- this is idempotent
// across restarts, not a rotation mechanism.
//
// THE RETURNED SECRET MUST NEVER BE LOGGED. Every caller in this tree checks
// only the error and, when it needs the value, reads the file back rather than
// holding the secret in a log-adjacent variable.
func EnsureOperatorSecret(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		// The generation path below writes 0600; a secret restored from a backup or copied
		// by hand under a permissive umask must not be silently accepted world-readable.
		if fi, statErr := os.Stat(path); statErr == nil && fi.Mode().Perm()&0o077 != 0 {
			return "", fmt.Errorf("relay: operator secret file %s is mode %04o; refusing anything wider than 0600", path, fi.Mode().Perm())
		}
		secret := strings.TrimSpace(string(b))
		if secret == "" {
			return "", fmt.Errorf("relay: operator secret file %s exists but is empty", path)
		}
		return secret, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("relay: read operator secret file: %w", err)
	}

	raw := make([]byte, operatorSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("relay: generate operator secret: %w", err)
	}
	secret := hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("relay: persist operator secret: %w", err)
	}
	return secret, nil
}
