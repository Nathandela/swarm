package skeleton

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
)

// A required but unavailable backend prevents frontend exec. A zero writer
// record alone could also mean the shim lost the frontend before recording it.
func provedLostManagerStartup(stateRoot string, meta persist.Meta, instance string) bool {
	var launch struct {
		SessionID       string `json:"session_id"`
		SessionDir      string `json:"session_dir"`
		NativeProvider  string `json:"native_provider"`
		BackendRequired bool   `json:"backend_required"`
		BackendProgram  string `json:"backend_program"`
		BackendSocket   string `json:"backend_socket_path"`
	}
	dir := filepath.Join(stateRoot, meta.ID)
	if meta.AgentType != "codex" || readNativeProof(stateRoot, meta.ID, "shim-launch.json", &launch) != nil ||
		launch.SessionID != meta.ID || launch.SessionDir != dir || launch.NativeProvider != meta.AgentType ||
		!launch.BackendRequired || launch.BackendProgram == "" || launch.BackendSocket == "" {
		return false
	}
	var marker historyTornMarker
	if readNativeProof(stateRoot, meta.ID, sessionDegradedFile, &marker) != nil || marker.Version != 1 || marker.SessionInstance != instance {
		return false
	}
	key, prefix := strings.CutPrefix(marker.Generation, "backend-gap/v1/")
	key, suffix := strings.CutSuffix(key, "/"+gapBackendNeverServed)
	decoded, err := hex.DecodeString(key)
	if !prefix || !suffix || err != nil || len(decoded) != 16 {
		return false
	}
	if _, err := os.Lstat(filepath.Join(dir, shim.BackendFile)); !errors.Is(err, os.ErrNotExist) {
		return false
	}
	return verifyNativeWriterExit(stateRoot, meta, false, true) == nil
}
