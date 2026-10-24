package main

import (
	"path/filepath"
	"testing"
)

func TestLoginWorkerDispatch(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-config.json")
	for _, command := range []string{"login-enroll", "login-refresh"} {
		t.Run(command, func(t *testing.T) {
			if got := runLoginWorker([]string{command, missing}); got != 1 {
				t.Fatalf("missing worker config exit = %d, want 1", got)
			}
		})
	}
	for _, args := range [][]string{nil, {"login-refresh"}, {"unknown", missing}, {"login-refresh", missing, "extra"}} {
		if got := runLoginWorker(args); got != 2 {
			t.Fatalf("invalid worker arguments %v exit = %d, want 2", args, got)
		}
	}
}
