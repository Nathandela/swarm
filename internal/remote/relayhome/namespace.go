// Package relayhome owns the canonical inputs to a relay-v2 machine home.
package relayhome

import "errors"

// ValidateNamespace accepts the exact bounded ASCII token shared by operator config,
// authenticated pairing payloads, durable phone state and relay-v2 authentication.
func ValidateNamespace(namespace string) error {
	if len(namespace) < 1 || len(namespace) > 64 || namespace[0] < 'a' || namespace[0] > 'z' {
		return errors.New("operator_namespace must match [a-z][a-z0-9-]{0,63}")
	}
	for i := 1; i < len(namespace); i++ {
		c := namespace[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return errors.New("operator_namespace must match [a-z][a-z0-9-]{0,63}")
		}
	}
	return nil
}
