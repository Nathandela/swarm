package claudeinput

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeControlInstallIsolation(t *testing.T) {
	if fmt.Sprintf("%x", sha256.Sum256([]byte(guardSource))) != "3be77704d7b2882768ec42af438e1f93f358d64d33f018240a4040efdfa46f76" {
		t.Fatal("legacy qualified guard source changed")
	}
	legacy := shortDir(t)
	if _, err := Install(legacy); err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(filepath.Join(legacy, DescriptorFile))
	raw, err := os.ReadFile(filepath.Join(GuardPath(legacy), "hooks/input.js"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != strings.ReplaceAll(guardSource, "__CONFIG_PATH__", string(config)) {
		t.Fatal("legacy installed guard changed")
	}
	for _, sender := range []bool{false, true} {
		dir := shortDir(t)
		if _, err := InstallNativeControl(dir, sender); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(GuardPath(dir), "hooks/input.js")); err != nil {
			t.Fatal(err)
		}
		types, err := os.ReadFile(filepath.Join(GuardPath(dir), "types/index.d.ts"))
		if err != nil || string(types) != "export type NativeControl = never;\n" {
			t.Fatal("control artifact needs native-supported type export", err)
		}
		_, err = os.Stat(filepath.Join(dir, "claude-input-plugin"))
		if !sender && !os.IsNotExist(err) {
			t.Error("observer-only installation created sender artifacts")
		}
		if sender && err != nil {
			t.Fatal(err)
		}
	}
}

func TestNativeControlDescriptorIntent(t *testing.T) {
	b, err := StartNativeControl(shortDir(t), "session", "2.1.310", 123, 456, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if b.Descriptor().RequiredRuntime != "claude-native-control" || b.Descriptor().SenderEnabled {
		t.Error("observation intent not immutable in descriptor")
	}
	c := NewClient(b.Descriptor())
	worker := Worker{NativeSessionID: "native", Generation: "guard", Version: "2.1.310"}
	heartbeat(t, b, worker, "", 0, 0)
	if _, err := c.Exchange(context.Background(), worker, "", "", ""); err == nil {
		t.Error("observer-only broker accepted sender exchange")
	}
	if _, err := c.Ready(context.Background()); err == nil {
		t.Error("observer-only broker advertised input readiness")
	}
	if _, err := c.Submit(context.Background(), worker, "op", "text"); err == nil {
		t.Error("observer-only broker accepted input")
	}
	if _, err := c.Operation(context.Background(), worker, "op"); err == nil {
		t.Error("observer-only broker exposed input operation")
	}
	for _, version := range []string{"2.1.296", "2.1.311"} {
		other, err := StartNativeControl(shortDir(t), "session", version, 123, 456, false)
		if err == nil {
			_ = other.Close()
			t.Errorf("observation accepted input-only qualified version %s", version)
		}
	}
}
