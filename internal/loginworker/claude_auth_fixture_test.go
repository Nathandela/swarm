package loginworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/Nathandela/swarm/internal/logins"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
)

// This transport is compiled only into tests. Product binaries neither read
// this fixture nor accept proxy/CA configuration through the helper protocol.
type claudeAuthFixtureNetwork struct {
	Proxy  string `json:"proxy"`
	CA     string `json:"ca"`
	SHA256 string `json:"sha256"`
}

func claudeAuthFixtureConfig(path string) (ClaudeAuthConfig, error) {
	var c ClaudeAuthConfig
	if _, err := safeAbsolute(filepath.Dir(path), true, true); err != nil {
		return c, err
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return c, err
	}
	defer func() { _ = root.Close() }()
	raw, err := readPrivate(root, filepath.Base(path), 8192)
	if err != nil {
		return c, err
	}
	if json.Unmarshal(raw, &c) != nil {
		return c, ErrInvalid
	}
	canonical, _ := json.Marshal(c)
	if !bytes.Equal(raw, canonical) {
		return c, ErrInvalid
	}
	return c, nil
}
func claudeAuthFixtureTransport(state string) error {
	if _, err := safeAbsolute(state, true, true); err != nil {
		return err
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	raw, err := readPrivate(root, "claude-auth-fixture-network.json", 4096)
	if err != nil {
		return err
	}
	var network claudeAuthFixtureNetwork
	if json.Unmarshal(raw, &network) != nil {
		return ErrInvalid
	}
	canonical, _ := json.Marshal(network)
	if !bytes.Equal(raw, canonical) {
		return ErrInvalid
	}
	proxy, err := url.Parse(network.Proxy)
	if err != nil || proxy.Scheme != "http" || proxy.Hostname() != "127.0.0.1" || proxy.User != nil || proxy.Path != "" || proxy.RawQuery != "" || proxy.Fragment != "" {
		return ErrUnsafe
	}
	port, err := strconv.Atoi(proxy.Port())
	if err != nil || port < 1 || port > 65535 {
		return ErrUnsafe
	}
	if network.CA != filepath.Join(state, "fixture-ca.pem") {
		return ErrUnsafe
	}
	certificate, err := readPrivate(root, "fixture-ca.pem", 16384)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(certificate)
	if hex.EncodeToString(digest[:]) != network.SHA256 {
		return ErrUnsafe
	}
	original := claudeAuthNativeEnvironment
	claudeAuthNativeEnvironment = func(a logins.ClaudeAuthAttempt, home, tmp string) []string {
		env := original(a, home, tmp)
		for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
			env = append(env, key+"="+network.Proxy)
		}
		return append(env, "NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost", "NODE_EXTRA_CA_CERTS="+network.CA)
	}
	return nil
}
func dispatchClaudeAuthFixture(args []string) (bool, int) {
	if len(args) != 4 || args[1] != "internal" {
		return false, 0
	}
	switch args[2] {
	case "login-claude-check":
		if RunClaudeAuthConfinementCheck(args[3]) != nil {
			return true, 1
		}
		return true, 0
	case "login-claude-auth":
		c, err := claudeAuthFixtureConfig(args[3])
		if err != nil {
			return true, 1
		}
		if claudeAuthFixtureTransport(c.StateRoot) != nil {
			return true, 1
		}
		if RunClaudeAuthConfig(context.Background(), args[3]) != nil {
			return true, 1
		}
		return true, 0
	case "claude-auth-fixture-recover":
		c, err := claudeAuthFixtureConfig(args[3])
		if err != nil {
			return true, 1
		}
		result, err := RecoverClaudeAuth(context.Background(), c)
		if err != nil {
			return true, 1
		}
		raw, _ := json.Marshal(result)
		_, _ = os.Stdout.Write(raw)
		return true, 0
	}
	return false, 0
}
