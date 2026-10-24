package loginworker

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/processcontain"
)

func TestDrainNativeWritersRetriesUncertainInventory(t *testing.T) {
	calls := 0
	err := drainNativeWritersWithInventory("/synthetic/native", func(homes, sessions []string) ([]processcontain.Identity, error) {
		if len(homes) != 1 || homes[0] != "/synthetic/native" || len(sessions) != 0 {
			t.Fatal("inventory lost the isolated native home")
		}
		calls++
		if calls < 3 {
			return nil, fmt.Errorf("transient census: %w", processcontain.ErrUnavailable)
		}
		return nil, nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("uncertain inventory did not reach a positive empty scan: calls=%d err=%v", calls, err)
	}
}

func TestDrainNativeWritersBoundsUncertainInventory(t *testing.T) {
	calls := 0
	started := time.Now()
	err := drainNativeWritersWithInventory("/synthetic/native", func([]string, []string) ([]processcontain.Identity, error) {
		calls++
		return nil, processcontain.ErrUnavailable
	})
	elapsed := time.Since(started)
	if !errors.Is(err, ErrUnavailable) || calls < 2 || elapsed < 3*time.Second || elapsed > 5*time.Second {
		t.Fatalf("uncertain inventory escaped the drain budget: calls=%d elapsed=%s err=%v", calls, elapsed, err)
	}
}

func TestDrainNativeWritersRejectsNontransientInventoryError(t *testing.T) {
	for _, scanErr := range []error{processcontain.ErrUnsupported, errors.New("inventory refused")} {
		t.Run(scanErr.Error(), func(t *testing.T) {
			calls := 0
			err := drainNativeWritersWithInventory("/synthetic/native", func([]string, []string) ([]processcontain.Identity, error) {
				calls++
				return nil, scanErr
			})
			if !errors.Is(err, ErrUnavailable) || calls != 1 {
				t.Fatalf("nontransient inventory error was retried: calls=%d err=%v", calls, err)
			}
		})
	}
}
