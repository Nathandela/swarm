/*
Record grouping and assignment headers adapted from dotenvy 0.15.7:
https://static.crates.io/crates/dotenvy/dotenvy-0.15.7.crate
SHA-256: 1aaf95b3e5c8f23aa320147307562d361db0ae0d51242340f558153b4eb2439b

The MIT License (MIT)

# Copyright (c) 2014 Santiago Lapresta and contributors

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
*/
package logins

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

func (s *Store) validateCodexDotenv() error {
	raw, err := s.readOptional("provider/codex/native/.env")
	if err != nil {
		return err
	}
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return fmt.Errorf("%w: managed Codex .env contains invalid text; repair that file", ErrIneligible)
	}
	const (
		complete = iota
		escape
		strongOpen
		strongEscape
		weakOpen
		weakEscape
		whiteSpace
	)
	// Match dotenvy 0.15.7 record grouping; values cannot create assignment keys.
	data := string(raw)
	state, recordStart, scanStart, line, recordLine := complete, 0, 0, 1, 1
	for scanStart < len(data) {
		end := len(data)
		if i := strings.IndexByte(data[scanStart:], '\n'); i >= 0 {
			end = scanStart + i + 1
		}
		recordEnd, done := end, false
		if scanStart == recordStart && strings.HasPrefix(strings.TrimLeftFunc(data[recordStart:end], unicode.IsSpace), "#") {
			recordEnd, done = recordStart, true
		} else {
			for i, c := range data[scanStart:end] {
				switch state {
				case whiteSpace:
					switch c {
					case '#':
						recordEnd, done = scanStart+i, true
					case '\\':
						state = escape
					case '"':
						state = weakOpen
					case '\'':
						state = strongOpen
					default:
						state = complete
					}
				case complete:
					switch {
					case unicode.IsSpace(c) && c != '\n' && c != '\r':
						state = whiteSpace
					case c == '\\':
						state = escape
					case c == '"':
						state = weakOpen
					case c == '\'':
						state = strongOpen
					}
				case escape:
					state = complete
				case weakOpen:
					switch c {
					case '\\':
						state = weakEscape
					case '"':
						state = complete
					}
				case weakEscape:
					state = weakOpen
				case strongOpen:
					switch c {
					case '\\':
						state = strongEscape
					case '\'':
						state = complete
					}
				case strongEscape:
					state = strongOpen
				}
				if done {
					break
				}
			}
			done = done || state == complete
		}
		if done {
			key, ok := codexDotenvKey(data[recordStart:recordEnd])
			if !ok {
				return fmt.Errorf("%w: managed Codex .env has an invalid assignment near line %d; repair that assignment", ErrIneligible, recordLine)
			}
			switch key {
			case "OPENAI_FEDERATION_RULE_ID", "OPENAI_IDENTITY_TOKEN_FILE", "OPENAI_WORKLOAD_IDENTITY_CONTEXT", "OPENAI_API_KEY", "OPENAI_ORGANIZATION", "OPENAI_PROJECT":
				return fmt.Errorf("%w: managed Codex .env sets auth selector %s; remove that assignment", ErrIneligible, key)
			}
			recordStart, recordLine, state = end, line+1, complete
		}
		scanStart, line = end, line+1
	}
	if recordStart != len(data) {
		return fmt.Errorf("%w: managed Codex .env has an unfinished quoted record near line %d; repair that assignment", ErrIneligible, recordLine)
	}
	return nil
}

func codexDotenvKey(record string) (string, bool) {
	record = strings.TrimSpace(record)
	if record == "" || strings.HasPrefix(record, "#") {
		return "", true
	}
	for prefix := 0; prefix < 2; prefix++ {
		c := record[0]
		if c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return "", false
		}
		i := 1
		for i < len(record) && ((record[i] >= 'a' && record[i] <= 'z') || (record[i] >= 'A' && record[i] <= 'Z') || (record[i] >= '0' && record[i] <= '9') || record[i] == '_' || record[i] == '.') {
			i++
		}
		key := record[:i]
		record = strings.TrimLeftFunc(record[i:], unicode.IsSpace)
		if prefix == 0 && key == "export" && !strings.HasPrefix(record, "=") && record != "" {
			continue
		}
		return key, strings.HasPrefix(record, "=")
	}
	return "", false
}
