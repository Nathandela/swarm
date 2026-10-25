//go:build linux

package loginworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Nathandela/swarm/internal/persist"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
)

func claudeAuthUnitFixture() map[string]string {
	return map[string]string{"Transient": "yes", "RemainAfterExit": "yes", "LoadState": "loaded", "Type": "exec", "Restart": "no", "NRestarts": "0", "RestartForceExitStatus": "", "KillMode": "control-group", "SendSIGKILL": "yes", "Delegate": "no", "NoNewPrivileges": "yes", "PrivateUsers": "yes", "CapabilityBoundingSet": "", "LimitFSIZE": "2097152", "MemoryMax": "805306368", "TasksMax": "128", "StandardOutput": "null", "StandardError": "null", "ReadOnlyPaths": "/sys/fs/cgroup", "RestrictAddressFamilies": "AF_INET AF_INET6", "RestrictNamespaces": "yes", "SystemCallArchitectures": "native", "SystemCallFilter": "~chroot fsconfig fsmount fsopen fspick mount mount_setattr move_mount open_tree pivot_root umount umount2", "RuntimeMaxUSec": "45s", "TimeoutStopUSec": "2s"}
}
func TestClaudeAuthUnitPropertyQualification(t *testing.T) {
	if !claudeAuthExpectedProperties(claudeAuthUnitFixture()) {
		t.Fatal("qualified249 encoding rejected")
	}
	for key, value := range map[string]string{"PrivateUsers": "no", "RestrictAddressFamilies": "AF_INET AF_UNIX", "SystemCallFilter": "", "RestrictNamespaces": "no", "Delegate": "yes", "Restart": "always", "LoadState": "not-found", "CapabilityBoundingSet": "CAP_SYS_ADMIN", "ReadOnlyPaths": "", "RuntimeMaxUSec": "0", "TimeoutStopUSec": "infinity", "SystemCallArchitectures": ""} {
		unit := claudeAuthUnitFixture()
		unit[key] = value
		if claudeAuthExpectedProperties(unit) {
			t.Fatalf("ineffective property admitted: %s", key)
		}
	}
}
func TestClaudeAuthFreshVerifierRejectsForeignOrActiveInvocation(t *testing.T) {
	child := exec.Command("/bin/sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	start, err := procstart.StartTime(child.Process.Pid)
	if err != nil {
		_ = child.Process.Kill()
		_ = child.Wait()
		t.Fatal(err)
	}
	worker := processcontain.Identity{PID: child.Process.Pid, StartTime: start}
	_ = child.Process.Kill()
	_ = child.Wait()
	managerStart, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	manager := processcontain.Identity{PID: os.Getpid(), StartTime: managerStart}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	mount, err := claudeAuthMountID()
	if errors.Is(err, ErrUnsupported) {
		t.Skip("unified cgroup fixture unavailable")
	}
	if err != nil {
		t.Fatal(err)
	}
	scope := logins.ClaudeAuthScope{Unit: "swarm-claude-auth-00000000000000000000000000000001.service", BootID: strings.TrimSpace(string(boot)), BusID: strings.Repeat("1", 32), ManagerOwner: ":1.1", Manager: manager, MountID: mount, Cgroup: "/swarm-claude-auth-unit-test-never-created.service", Device: 1, Inode: 1, InvocationID: strings.Repeat("2", 32), Worker: worker}
	original := claudeAuthCommand
	defer func() { claudeAuthCommand = original }()
	for _, scenario := range []string{"retired", "exited", "active", "stale", "queued", "missing", "manager-changed"} {
		t.Run(scenario, func(t *testing.T) {
			claudeAuthCommand = func(_ context.Context, program string, args ...string) (string, error) {
				if program == "systemctl" && len(args) == 2 && args[1] == "--version" {
					return "systemd 249 (249.11)", nil
				}
				if program == "busctl" && len(args) > 1 && args[1] == "get-property" {
					return `s "249.11"`, nil
				}
				if program == "busctl" {
					method := args[5]
					switch method {
					case "GetId":
						return `s "` + scope.BusID + `"`, nil
					case "GetNameOwner":
						owner := scope.ManagerOwner
						if scenario == "manager-changed" {
							owner = ":1.9"
						}
						raw, _ := json.Marshal(owner)
						return "s " + string(raw), nil
					case "GetConnectionUnixProcessID":
						return "u " + strconv.Itoa(manager.PID), nil
					}
					t.Fatal("unexpected bus call")
				}
				unit := claudeAuthUnitFixture()
				unit["InvocationID"] = scope.InvocationID
				unit["MainPID"] = "0"
				unit["ActiveState"] = "failed"
				unit["SubState"] = "failed"
				unit["Job"] = "0"
				switch scenario {
				case "exited":
					unit["ActiveState"] = "active"
					unit["SubState"] = "exited"
				case "active":
					unit["ActiveState"] = "active"
				case "stale":
					unit["InvocationID"] = strings.Repeat("3", 32)
				case "queued":
					unit["Job"] = "99"
				case "missing":
					unit["LoadState"] = "not-found"
				}
				var lines []string
				for k, v := range unit {
					lines = append(lines, k+"="+v)
				}
				return strings.Join(lines, "\n"), nil
			}
			err := verifyClaudeAuthScopeRetired(t.Context(), scope)
			if scenario == "exited" {
				if err := stopClaudeAuthRevokedWorker(t.Context(), scope); err != nil {
					t.Fatalf("worker exited during revoke snapshot: %v", err)
				}
			}
			if scenario == "retired" || scenario == "exited" {
				if err != nil {
					t.Fatalf("bound retained retired group rejected: %v", err)
				}
			} else if err == nil {
				t.Fatalf("unqualified %s accepted", scenario)
			}
		})
	}
}

func TestClaudeAuthVerifiedImageSurvivesAtomicInstallationReplacement(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("qualified architecture only")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "native")
	marker := filepath.Join(root, "marker")
	original := []byte("#!/bin/sh\nprintf original > \"$1\"\n")
	if err := os.WriteFile(path, original, 0700); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := persist.CLIFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	c := ClaudeAuthConfig{NativePath: path, NativeVersion: "2.1.310", NativeFingerprint: fingerprint}
	if image, err := openClaudeAuthImage(c); err == nil {
		_ = image.Close()
		t.Fatal("unqualified synthetic image admitted by production pin")
	}
	digest := sha256.Sum256(original)
	image, err := openClaudeAuthImageForDigest(c, hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = image.Close() }()
	replacement := filepath.Join(root, "replacement")
	if err := os.WriteFile(replacement, []byte("#!/bin/sh\nprintf replacement > \"$1\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	cmd := claudeAuthImageCommand(image, path, marker)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(marker)
	if err != nil || string(raw) != "original" {
		t.Fatalf("atomic replacement redirected verified image: %q %v", raw, err)
	}
}

func TestClaudeAuthQualifiedManagerVersionIsFinite(t *testing.T) {
	original := claudeAuthCommand
	defer func() { claudeAuthCommand = original }()
	for _, version := range []string{"systemd 249 (249.11)", "systemd 250 (250.1)", "systemd 248 (248.1)", "unknown"} {
		claudeAuthCommand = func(_ context.Context, program string, _ ...string) (string, error) {
			if program == "busctl" {
				return `s "249.11"`, nil
			}
			return version, nil
		}
		err := ValidateClaudeAuthManager(t.Context())
		if (version == "systemd 249 (249.11)") != (err == nil) {
			t.Fatalf("incorrect manager source qualification: %q %v", version, err)
		}
	}
}

func TestClaudeAuthRunningManagerMustMatchQualifiedSourceVersion(t *testing.T) {
	original := claudeAuthCommand
	defer func() { claudeAuthCommand = original }()
	for _, manager := range []string{"249.11-0ubuntu3.23", "250.1", "unknown"} {
		claudeAuthCommand = func(_ context.Context, program string, args ...string) (string, error) {
			if program == "systemctl" {
				return "systemd 249 (249.11)", nil
			}
			raw, _ := json.Marshal(manager)
			return "s " + string(raw), nil
		}
		err := ValidateClaudeAuthManager(t.Context())
		if (manager == "249.11-0ubuntu3.23") != (err == nil) {
			t.Fatalf("CLI249 qualified a different running manager: %q %v", manager, err)
		}
	}
}
