// Package managerproof binds native manager observations without model-claimed authority.
package managerproof

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	"github.com/Nathandela/swarm/internal/jsontext"
)

const MaxJSONBytes = 64 << 10

var ErrInvalid = errors.New("manager proof: invalid input")

// CanonicalJSON preserves numeric precision, sorts object keys and rejects repair.
func CanonicalJSON(raw []byte) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > MaxJSONBytes || jsontext.ValidateUnicode(raw) != nil {
		return nil, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := jsonValue(decoder, 0)
	if err != nil {
		return nil, ErrInvalid
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, ErrInvalid
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, ErrInvalid
	}
	return canonical, nil
}
func ArgumentsHash(raw []byte) (string, error) {
	canonical, err := CanonicalJSON(raw)
	if err != nil || len(canonical) == 0 || canonical[0] != '{' {
		return "", ErrInvalid
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}
func jsonValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, ErrInvalid
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, ErrInvalid
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := map[string]any{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, ErrInvalid
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, ErrInvalid
			}
			if _, duplicate := object[key]; duplicate {
				return nil, ErrInvalid
			}
			value, err := jsonValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return nil, ErrInvalid
		}
		return object, nil
	case '[':
		array := []any{}
		for decoder.More() {
			value, err := jsonValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return nil, ErrInvalid
		}
		return array, nil
	default:
		return nil, ErrInvalid
	}
}
