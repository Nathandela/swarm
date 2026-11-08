package voice

import (
	"encoding/xml"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestVoiceUnitPath(t *testing.T) {
	for _, tc := range []struct{ goos, want string }{
		{"darwin", "/home/test/Library/LaunchAgents/com.swarm.voice.plist"},
		{"linux", "/home/test/.config/systemd/user/swarm-voice.service"},
	} {
		got, err := UnitPath(tc.goos, "/home/test")
		if err != nil || got != tc.want {
			t.Errorf("UnitPath(%q) = %q, %v; want %q", tc.goos, got, err, tc.want)
		}
	}
	for _, home := range []string{"relative", "/home/test\nnoise", string([]byte{0xff})} {
		if _, err := UnitPath("darwin", home); err == nil {
			t.Errorf("accepted unsafe home %q", home)
		}
	}
	if _, err := UnitPath("windows", "/home/test"); err == nil {
		t.Fatal("accepted unsupported platform")
	}
}

func TestRenderVoiceLaunchdArguments(t *testing.T) {
	binary, stateDir, logPath := "/Applications/Swarm & Tools/swarm", "/state/Voice & work", "/logs/voice & work.log"
	unit, err := RenderUnit("darwin", binary, stateDir, logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := plistArguments(t, unit), []string{binary, "voice", "serve", "--state-dir", stateDir}; !slices.Equal(got, want) {
		t.Fatalf("ProgramArguments = %#v, want %#v", got, want)
	}
	for _, want := range []string{
		"<key>Label</key><string>com.swarm.voice</string>",
		"<key>StandardOutPath</key><string>/logs/voice &amp; work.log</string>",
		"<key>StandardErrorPath</key><string>/logs/voice &amp; work.log</string>",
		"<key>RunAtLoad</key><true/>",
		"<key>KeepAlive</key><true/>",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("launchd unit missing %q:\n%s", want, unit)
		}
	}
}

func TestRenderVoiceSystemdEscapesAndLiteralExpansion(t *testing.T) {
	homeVar := "$" + "{HOME}"
	binary := "/path with space/" + homeVar + "%"
	stateDir := "/state/" + homeVar + "% \"voice\"\\data"
	logPath := "/logs/voice output % \"today\"\\run.log"
	unit, err := RenderUnit("linux", binary, stateDir, logPath)
	if err != nil {
		t.Fatal(err)
	}
	wantStart := "ExecStart=:\"/path with space/" + homeVar + "%%\" voice serve --state-dir \"/state/" + homeVar + "%% \\\"voice\\\"\\\\data\""
	if !strings.Contains(unit, wantStart) {
		t.Errorf("missing escaped literal command %q:\n%s", wantStart, unit)
	}
	for _, want := range []string{
		"Description=Swarm Voice",
		"StandardOutput=append:/logs/voice output %% \"today\"\\run.log",
		"StandardError=append:/logs/voice output %% \"today\"\\run.log",
		"Restart=always",
		"RestartSec=10",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("systemd unit missing %q:\n%s", want, unit)
		}
	}
}

func TestRenderVoiceSystemdAcceptsKnownSafeExecutablePunctuation(t *testing.T) {
	binary := "/opt/swarm with space/über!@#$%^&()_+=,:;]"
	unit, err := RenderUnit("linux", binary, "/state with spaces/$data % \"quoted\"\\literal", "/logs with spaces/% \"quoted\"\\literal")
	if err != nil {
		t.Fatalf("rejected systemd string_is_safe characters: %v", err)
	}
	want := "ExecStart=:\"/opt/swarm with space/über!@#$%%^&()_+=,:;]\" voice serve --state-dir " +
		"\"/state with spaces/$data %% \\\"quoted\\\"\\\\literal\""
	if !strings.Contains(unit, want) {
		t.Fatalf("unit omitted safe executable path %q:\n%s", want, unit)
	}
}

func TestRenderVoiceRejectsUnsafePathsAndPlatforms(t *testing.T) {
	for _, tc := range []struct{ name, goos, binary, state, log string }{
		{"unsupported platform", "windows", "/swarm", "/state", "/log"},
		{"relative binary", "linux", "swarm", "/state", "/log"},
		{"binary newline", "linux", "/swarm\nExecStart=/bin/sh", "/state", "/log"},
		{"state newline", "darwin", "/swarm", "/state\n<key>RunAtLoad</key>", "/log"},
		{"log newline", "linux", "/swarm", "/state", "/log\nExecStart=evil"},
		{"log trailing space", "linux", "/swarm", "/state", "/log "},
		{"log trailing backslash", "linux", "/swarm", "/state", "/log\\"},
		{"XML FFFE binary", "darwin", "/opt/swarm\ufffe", "/state", "/log"},
		{"XML FFFF state", "darwin", "/swarm", "/state\uffff", "/log"},
		{"XML FFFE log", "darwin", "/swarm", "/state", "/log\ufffe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := RenderUnit(tc.goos, tc.binary, tc.state, tc.log); err == nil {
				t.Fatal("accepted unsafe service unit input")
			}
		})
	}
	for _, binary := range []string{
		"/opt/swarm\"quoted",
		"/opt/swarm'quoted",
		"/opt/swarm\\bin",
		"/opt/swarm*",
		"/opt/swarm?",
		"/opt/swarm[1]",
		"/opt/swarm\x7fdel",
	} {
		t.Run(binary, func(t *testing.T) {
			if _, err := RenderUnit("linux", binary, "/state", "/log"); err == nil {
				t.Fatalf("accepted systemd-unsafe executable path %q", binary)
			}
		})
	}
}

func plistArguments(t *testing.T, unit string) []string {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(unit))
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "key" {
			continue
		}
		var key string
		if err := d.DecodeElement(&key, &start); err != nil {
			t.Fatal(err)
		}
		value, err := d.Token()
		if err != nil {
			t.Fatal(err)
		}
		array, ok := value.(xml.StartElement)
		if key != "ProgramArguments" {
			if ok {
				if err := d.Skip(); err != nil {
					t.Fatal(err)
				}
			}
			continue
		}
		if !ok || array.Name.Local != "array" {
			t.Fatalf("ProgramArguments value = %#v, want array", value)
		}
		var args []string
		for {
			tok, err := d.Token()
			if err != nil {
				t.Fatal(err)
			}
			if end, ok := tok.(xml.EndElement); ok && end.Name.Local == "array" {
				return args
			}
			if item, ok := tok.(xml.StartElement); ok && item.Name.Local == "string" {
				var arg string
				if err := d.DecodeElement(&arg, &item); err != nil {
					t.Fatal(err)
				}
				args = append(args, arg)
			}
		}
	}
	t.Fatal("ProgramArguments not found")
	return nil
}
