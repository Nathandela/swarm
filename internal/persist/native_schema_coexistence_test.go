package persist

import (
	"testing"

	"github.com/Nathandela/swarm/internal/status"
)

func TestHermesAndClaudeFixtureSchemaCoexist(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hermes := Meta{ID: "hermes", AgentType: "hermes", HermesNative: true, CLIIdentity: &CLIIdentity{Version: "0.22.5"}}
	fixture := Meta{SchemaVersion: ClaudeChatSchemaVersion, ID: "fixture", AgentType: "fake", Cwd: t.TempDir(), RequiredRuntime: RequiredRuntimeClaudeChat}
	for _, meta := range []Meta{hermes, fixture} {
		if err := store.Save(meta); err != nil {
			t.Fatal(err)
		}
		loaded, err := store.Load(meta.ID)
		if err != nil || loaded.HermesNative != meta.HermesNative || loaded.RequiredRuntime != meta.RequiredRuntime {
			t.Fatalf("runtime changed: %+v, %v", loaded, err)
		}
	}
	for _, meta := range []Meta{
		{SchemaVersion: ClaudeChatSchemaVersion, ID: "mixed-schema", AgentType: "hermes", HermesNative: true, CLIIdentity: &CLIIdentity{Version: "0.22.5"}},
		{SchemaVersion: ClaudeChatSchemaVersion, ID: "mixed-fixture", AgentType: "fake", Cwd: t.TempDir(), RequiredRuntime: RequiredRuntimeClaudeChat, HermesNative: true},
	} {
		if err := store.Save(meta); err == nil {
			t.Fatalf("accepted mixed runtime: %+v", meta)
		}
	}
}

func TestHermesUnknownWriterTupleRemainsRetained(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	meta := Meta{ID: "hermes-unknown", AgentType: "hermes", HermesNative: true, CLIIdentity: &CLIIdentity{Version: "0.22.5"}, ShimPID: 123, Status: status.Status{Process: status.ProcessLost, NativeWorkUnknown: true}}
	if err := store.Save(meta); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(meta.ID)
	if err != nil || !got.HermesNative || !got.Status.NativeWorkUnknown || got.ShimPID != 123 || got.ShimStartTime != 0 {
		t.Fatalf("unknown Hermes writer was rejected or fabricated: %+v, %v", got, err)
	}
}
