package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestVoiceSSHRejectsArbitraryHostArgumentsBeforeConnection(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"--host=-oProxyCommand=unexpected"},
		{"--host", "alias;extra"},
		{"--host", "user@alias"},
		{"--host", "alias", "extra"},
	} {
		var stderr bytes.Buffer
		if code := dispatchVoiceSSH(args, io.Discard, &stderr); code != misuseExit || !strings.Contains(stderr.String(), "OpenSSH host alias") {
			t.Errorf("%q: exit %d, output %s", args, code, &stderr)
		}
	}
}
