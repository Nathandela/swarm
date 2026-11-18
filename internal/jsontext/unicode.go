// Package jsontext validates Unicode integrity before JSON decoding can repair it.
package jsontext

import (
	"errors"
	"strconv"
	"unicode/utf8"
)

// ValidateUnicode rejects invalid UTF-8 and unpaired UTF-16 escapes in JSON strings.
// It leaves JSON syntax, duplicate keys and application schemas to the decoder.
func ValidateUnicode(data []byte) error {
	if !utf8.Valid(data) {
		return errors.New("JSON is not valid UTF-8")
	}
	return validateUnicodeEscapes(data)
}

func validateUnicodeEscapes(data []byte) error {
	inString := false
	for i := 0; i < len(data); i++ {
		if !inString {
			if data[i] == '"' {
				inString = true
			}
			continue
		}
		switch data[i] {
		case '"':
			inString = false
		case '\\':
			if i+1 >= len(data) {
				continue
			}
			if data[i+1] != 'u' {
				i++
				continue
			}
			unit, end, err := unicodeEscapeUnit(data, i)
			if err != nil {
				return err
			}
			if unit >= 0xD800 && unit <= 0xDBFF {
				low, lowEnd, err := unicodeEscapeUnit(data, end+1)
				if err != nil || low < 0xDC00 || low > 0xDFFF {
					return errors.New("JSON contains an unpaired UTF-16 high surrogate")
				}
				i = lowEnd
			} else if unit >= 0xDC00 && unit <= 0xDFFF {
				return errors.New("JSON contains an unpaired UTF-16 low surrogate")
			} else {
				i = end
			}
		}
	}
	return nil
}

func unicodeEscapeUnit(data []byte, slash int) (uint16, int, error) {
	if slash+6 > len(data) || data[slash] != '\\' || data[slash+1] != 'u' {
		return 0, slash, errors.New("invalid JSON Unicode escape")
	}
	unit, err := strconv.ParseUint(string(data[slash+2:slash+6]), 16, 16)
	if err != nil {
		return 0, slash, errors.New("invalid JSON Unicode escape")
	}
	return uint16(unit), slash + 5, nil
}
