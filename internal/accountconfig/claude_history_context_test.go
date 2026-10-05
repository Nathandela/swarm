package accountconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeHistoryAliasPreservesOriginalConversationAndPrivateData(t *testing.T) {
	f := fixture(t, "claude")
	lease := func(g string, install func() error) error { return install() }
	context, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease)
	if err != nil {
		t.Fatal(err)
	}
	key := "-synthetic-project"
	source := filepath.Join(f.source, "projects", key)
	put(t, filepath.Join(source, "conversation.jsonl"), "latest native context")
	proof, err := PrepareClaudeHistoryAlias(context, f.candidate, key, false, lease)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidClaudeHistoryAlias(context, proof) {
		t.Fatal("invalid intrinsic history proof")
	}
	if err := RevalidateClaudeHistoryAlias(context, f.candidate, proof); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(f.candidate, "projects", key, "conversation.jsonl"))
	if err != nil || string(raw) != "latest native context" {
		t.Fatal("native history continuity lost")
	}
	if err := os.WriteFile(filepath.Join(f.candidate, "projects", key, "conversation.jsonl"), []byte("native continuation"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(source, "conversation.jsonl"))
	if string(raw) != "native continuation" {
		t.Fatal("native writes did not retain original history authority")
	}
	keyB := "-other-project"
	private := filepath.Join(f.candidate, "projects", keyB)
	put(t, filepath.Join(private, "retained.jsonl"), "existing private conversation")
	if _, err := PrepareClaudeHistoryAlias(context, f.candidate, keyB, true, lease); err == nil {
		t.Fatal("existing destination data overwritten")
	}
	raw, _ = os.ReadFile(filepath.Join(private, "retained.jsonl"))
	if string(raw) != "existing private conversation" {
		t.Fatal("private history lost")
	}
}

func TestClaudeHistoryPreviewDoesNotCreateOwnerSource(t *testing.T) {
	f := fixture(t, "claude")
	lease := func(g string, install func() error) error { return install() }
	context, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareClaudeHistoryAlias(context, f.candidate, "-absent", false, lease); err == nil {
		t.Fatal("absent preview history admitted")
	}
	if _, err := os.Stat(filepath.Join(f.source, "projects")); !os.IsNotExist(err) {
		t.Fatal("preview wrote original history")
	}
	proof, err := PrepareClaudeHistoryAlias(context, f.candidate, "-absent", true, lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := RevalidateClaudeHistoryAlias(context, f.candidate, proof); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(proof.SourcePath, proof.SourcePath+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(proof.SourcePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := RevalidateClaudeHistoryAlias(context, f.candidate, proof); err == nil {
		t.Fatal("replaced original history root admitted")
	}
}
