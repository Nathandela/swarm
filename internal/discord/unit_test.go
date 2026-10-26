package discord

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func TestRenderUnits(t *testing.T) {
	for _, osname := range []string{"darwin", "linux"} {
		unit, err := RenderUnit(osname, "/path with space/swarm", "/state & space", "/logs/output.log")
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range []string{"discord", "run", "--state-dir"} {
			if !strings.Contains(unit, s) {
				t.Fatalf("missing %s", s)
			}
		}
		if strings.Contains(unit, "token") || strings.Contains(unit, "/bin/sh") {
			t.Fatal(unit)
		}
		if osname == "darwin" {
			d := xml.NewDecoder(strings.NewReader(unit))
			for {
				_, e := d.Token()
				if e == io.EOF {
					break
				}
				if e != nil {
					t.Fatal(e)
				}
			}
			if !strings.Contains(unit, "ThrottleInterval") {
				t.Fatal(unit)
			}
		} else if !strings.Contains(unit, "RestartSec=") || !strings.Contains(unit, `"/path with space/swarm"`) {
			t.Fatal(unit)
		}
	}
}
func TestUnitRejectsInjectionAndUnsupported(t *testing.T) {
	for _, osname := range []string{"darwin", "linux", "windows"} {
		if _, err := RenderUnit(osname, "/swarm\nExecStart=evil", "/state", "/log"); err == nil {
			t.Fatalf("accepted %s", osname)
		}
	}
	if _, err := UnitPath("windows", "/home/test"); err == nil {
		t.Fatal("accepted windows")
	}
	p, err := UnitPath("darwin", "/home/test")
	if err != nil || p != "/home/test/Library/LaunchAgents/com.swarm.discord.plist" {
		t.Fatalf("%s %v", p, err)
	}
}
