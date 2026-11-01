package logins

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
)

func TestCodexAuxiliaryWaitsForOwnedNativePublication(t *testing.T) {
	s, _, lease := ownedGrantFixture(t)
	defer func() { _ = lease.Close() }()
	if err := lease.PrepareAuxiliary("0.163.0"); err != nil {
		t.Fatal(err)
	}
	started, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var finish sync.Once
	defer finish.Do(func() { close(release) })
	s.writeOps.rename = func(old, next string) error {
		if filepath.Base(next) == ".grant-owner" {
			close(entered)
			<-release
		}
		return s.root.Rename(old, next)
	}
	published := make(chan error, 1)
	go func() { published <- lease.RecordNative(processcontain.Identity{PID: os.Getpid(), StartTime: started}) }()
	select {
	case <-entered:
	case err := <-published:
		t.Fatalf("owner publication never reached durable rename: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("owner publication did not reach rename")
	}
	checked := make(chan error, 1)
	go func() { checked <- lease.CheckAuxiliary() }()
	premature := false
	select {
	case err := <-checked:
		premature = true
		t.Errorf("inventory observed an unfinished Store-owned publication: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	finish.Do(func() { close(release) })
	if err := <-published; err != nil {
		t.Fatal(err)
	}
	if !premature {
		if err := <-checked; err != nil {
			t.Fatal(err)
		}
	}
	// Serializing the owner write must not authorize unrelated temporary files.
	if err := os.WriteFile(filepath.Join(lease.Home(), ".login-tmp-00112233445566778899aabbccddeeff"), []byte("unowned"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := lease.CheckAuxiliary(); err == nil {
		t.Fatal("unowned temporary was accepted")
	}
}
