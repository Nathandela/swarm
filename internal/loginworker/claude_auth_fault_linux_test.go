//go:build linux

package loginworker

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/processcontain"
)

// Changed only in an independent scratch Go overlay, never in product source.
const claudeAuthFaultOverlay = ""

type claudeAuthFaultProof struct {
	Ref         logins.ClaudeAuthRef    `json:"ref"`
	Attempt     int                     `json:"attempt"`
	Phase       string                  `json:"phase"`
	Scope       logins.ClaudeAuthScope  `json:"scope"`
	Native      processcontain.Identity `json:"native"`
	StopProof   string                  `json:"stop_proof"`
	UnitRetired bool                    `json:"unit_retired"`
}

func readClaudeAuthFaultProof(c ClaudeAuthConfig, result logins.ClaudeAuthResult) (claudeAuthFaultProof, error) {
	var proof claudeAuthFaultProof
	file, err := os.Open(filepath.Join(c.StateRoot, "logins", "claude-auth", c.Ref.LoginID+".json"))
	if err != nil {
		return proof, err
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(raw) > 65536 || json.Unmarshal(raw, &proof) != nil {
		return proof, ErrInvalid
	}
	if proof.Ref != result.Ref || proof.Attempt != result.Attempt || proof.Phase != result.Phase || proof.Scope != result.Scope || proof.UnitRetired != result.UnitRetired {
		return proof, ErrInvalid
	}
	return proof, nil
}

type claudeAuthFaultHit struct {
	Ref     logins.ClaudeAuthRef `json:"ref"`
	Attempt int                  `json:"attempt"`
	Point   string               `json:"point"`
}

func readClaudeAuthFaultHit(c ClaudeAuthConfig, attempt int) error {
	file, err := os.Open(filepath.Join(c.StateRoot, "logins", "claude-auth", c.Ref.LoginID, c.Ref.Nonce, "fault-hit.json"))
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	var hit claudeAuthFaultHit
	if err != nil || len(raw) > 4096 || json.Unmarshal(raw, &hit) != nil || hit.Ref != c.Ref || hit.Attempt != attempt || hit.Point != "containment-enospc" {
		return ErrInvalid
	}
	return nil
}
