// Package pushreg owns the registration transcripts shared by the phone and push gateway.
package pushreg

import (
	"crypto/sha256"
	"errors"
	"unicode/utf8"
)

// CanonicalRegistration returns the RFC 8785/JCS representation of the closed
// registration schema after replacing attestation.token with the empty string. The schema
// contains only objects and strings, so RFC 8785's number serialization rules do not enter
// this protocol. Its fixed ASCII member names are emitted in UTF-16 code-unit order.
func CanonicalRegistration(installationPublicKey, fcmToken string) ([]byte, error) {
	if !utf8.ValidString(installationPublicKey) || !utf8.ValidString(fcmToken) {
		return nil, errors.New("pushreg: registration contains invalid UTF-8")
	}
	out := make([]byte, 0, len(installationPublicKey)+len(fcmToken)+112)
	out = append(out, `{"attestation":{"kind":"play_integrity","token":""},"fcm_token":`...)
	out = appendJCSString(out, fcmToken)
	out = append(out, `,"installation_public_key":`...)
	out = appendJCSString(out, installationPublicKey)
	out = append(out, '}')
	return out, nil
}

// RequestHash is SHA-256 of CanonicalRegistration.
func RequestHash(installationPublicKey, fcmToken string) ([32]byte, error) {
	canonical, err := CanonicalRegistration(installationPublicKey, fcmToken)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(canonical), nil
}

func appendJCSString(dst []byte, value string) []byte {
	const hex = "0123456789abcdef"
	dst = append(dst, '"')
	for _, r := range value {
		switch r {
		case '"', '\\':
			dst = append(dst, '\\', byte(r))
		case '\b':
			dst = append(dst, `\b`...)
		case '\t':
			dst = append(dst, `\t`...)
		case '\n':
			dst = append(dst, `\n`...)
		case '\f':
			dst = append(dst, `\f`...)
		case '\r':
			dst = append(dst, `\r`...)
		default:
			if r < 0x20 {
				dst = append(dst, '\\', 'u', '0', '0', hex[byte(r)>>4], hex[byte(r)&0x0f])
			} else {
				dst = utf8.AppendRune(dst, r)
			}
		}
	}
	return append(dst, '"')
}
