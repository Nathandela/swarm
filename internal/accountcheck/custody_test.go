package accountcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
)

func TestWritersStoppedForBindingRefusesUnknownRetainedCustody(t *testing.T) {
	_, cfg, _ := checkFixture(t, "", ModeAuthStatus)
	if !WritersStoppedForBinding(cfg.StateRoot, cfg.Binding) {
		t.Fatal("genuinely absent inventory refused")
	}
	if _, err := os.Stat(filepath.Join(cfg.StateRoot, "accounts", "checks")); !os.IsNotExist(err) {
		t.Fatal("custody read created a check inventory", err)
	}
	root, err := openChecks(cfg.StateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	generation := strings.Repeat("d", 32)
	if err := root.Mkdir(generation, 0o700); err != nil {
		t.Fatal(err)
	}
	files, err := openGeneration(root, generation)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = files.Close() }()
	binding := cfg.Binding
	binding.ConfigurationGeneration++
	ref := Ref{SchemaVersion: SchemaVersion, Generation: generation, Worker: processcontain.Identity{PID: 1<<30 + 3, StartTime: 1}, Binding: binding, Mode: ModeAuthStatus}
	if err := writeJSON(files, WorkerFile, ref); err != nil {
		t.Fatal(err)
	}
	if WritersStoppedForBinding(cfg.StateRoot, cfg.Binding) {
		t.Fatal("PID absence without exact proof permitted transfer")
	}
	proof := Stopped{SchemaVersion: SchemaVersion, Ref: ref, WritersStopped: true}
	proof.Ref.Generation = strings.Repeat("e", 32)
	if err := writeJSON(files, StoppedFile, proof); err != nil {
		t.Fatal(err)
	}
	if WritersStoppedForBinding(cfg.StateRoot, cfg.Binding) {
		t.Fatal("foreign proof permitted transfer")
	}
	proof.Ref = ref
	if err := writeJSON(files, StoppedFile, proof); err != nil {
		t.Fatal(err)
	}
	if !WritersStoppedForBinding(cfg.StateRoot, cfg.Binding) {
		t.Fatal("exact contained dead check refused")
	}
	start, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	ref.Worker = processcontain.Identity{PID: os.Getpid(), StartTime: start}
	proof.Ref = ref
	if err := writeJSON(files, WorkerFile, ref); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(files, StoppedFile, proof); err != nil {
		t.Fatal(err)
	}
	if WritersStoppedForBinding(cfg.StateRoot, cfg.Binding) {
		t.Fatal("live check owner permitted transfer")
	}
}

func TestWritersStoppedForBindingRefusesUnsafeInventory(t *testing.T) {
	_, cfg, _ := checkFixture(t, "", ModeAuthStatus)
	path := filepath.Join(cfg.StateRoot, "accounts", "checks")
	if err := os.Symlink(t.TempDir(), path); err != nil {
		t.Fatal(err)
	}
	if WritersStoppedForBinding(cfg.StateRoot, cfg.Binding) {
		t.Fatal("symlink inventory treated as absent")
	}
}
