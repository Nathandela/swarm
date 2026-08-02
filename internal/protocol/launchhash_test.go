package protocol

// Failing-first tests for LaunchContentHash (R-POL.9 launch content-binding): a launch
// command's signature must bind the launch spec so a compromised gateway cannot alter
// the agent/cwd/options/prompt of a validly-signed launch. The hash is computed
// identically by the phone-core (signer) and the daemon (verifier); a mismatch makes
// the signature fail. RED is undefined-only (LaunchContentHash does not exist yet).

import (
	"bytes"
	"testing"
)

func baseReq() *LaunchReq {
	return &LaunchReq{
		Agent:         "claude",
		Cwd:           "/work/repo",
		Options:       map[string]string{"model": "opus", "profile": "default"},
		Env:           []string{"IGNORED=1"},
		Cols:          80,
		Rows:          24,
		InitialPrompt: "hello",
		Worktree:      true,
	}
}

// TestLaunchContentHash_DeterministicAndSized: the hash is a stable 32 bytes for equal
// specs and is independent of Options map iteration order and of the ignored/cosmetic
// fields (Env, Cols, Rows).
func TestLaunchContentHash_DeterministicAndSized(t *testing.T) {
	h1 := LaunchContentHash(baseReq())
	if len(h1) != 32 {
		t.Fatalf("hash len = %d, want 32", len(h1))
	}
	// Recompute with Options built in a different insertion order + different ignored
	// fields; the hash must be identical.
	r2 := &LaunchReq{
		Agent:         "claude",
		Cwd:           "/work/repo",
		Options:       map[string]string{"profile": "default", "model": "opus"},
		Env:           []string{"DIFFERENT=2", "MORE=3"},
		Cols:          200,
		Rows:          50,
		InitialPrompt: "hello",
		Worktree:      true,
	}
	if h2 := LaunchContentHash(r2); !bytes.Equal(h1, h2) {
		t.Fatalf("hash not order/ignored-field independent:\n %x\n %x", h1, h2)
	}
}

// TestLaunchContentHash_SensitiveToSecurityFields: changing any bound field (agent,
// cwd, an option value, initial prompt, worktree) changes the hash — otherwise a
// gateway could swap that field under a valid signature.
func TestLaunchContentHash_SensitiveToSecurityFields(t *testing.T) {
	base := LaunchContentHash(baseReq())
	muts := map[string]func(*LaunchReq){
		"agent":    func(r *LaunchReq) { r.Agent = "evil" },
		"cwd":      func(r *LaunchReq) { r.Cwd = "/etc" },
		"opt_val":  func(r *LaunchReq) { r.Options["model"] = "sonnet" },
		"opt_add":  func(r *LaunchReq) { r.Options["extra"] = "x" },
		"prompt":   func(r *LaunchReq) { r.InitialPrompt = "rm -rf" },
		"worktree": func(r *LaunchReq) { r.Worktree = false },
	}
	for name, mut := range muts {
		r := baseReq()
		mut(r)
		if bytes.Equal(base, LaunchContentHash(r)) {
			t.Errorf("mutating %s did not change the content hash (unbound field)", name)
		}
	}
}
