package supervise

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLinuxUpgradeCleansPayloadBeforeRestart(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failure    string
		restart    int
		wantStatus int
	}{
		{name: "success one", restart: 0, wantStatus: 0},
		{name: "success two", restart: 2, wantStatus: 2},
		{name: "success three", restart: 3, wantStatus: 3},
		{name: "download failure", failure: "download", restart: 2, wantStatus: 2},
		{name: "checksum failure", failure: "checksum", restart: 3, wantStatus: 3},
		{name: "install failure", failure: "install", restart: 1, wantStatus: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			binDir := filepath.Join(root, "bin")
			if err := os.Mkdir(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			pathDir := filepath.Join(root, "commands")
			if err := os.Mkdir(pathDir, 0o755); err != nil {
				t.Fatal(err)
			}
			writeExecutable(t, filepath.Join(binDir, "swarm"), `#!/bin/sh
if [ "$1" = version ]; then echo 'swarm 0.1.0'; exit 0; fi
if [ "$1" = daemon ] && [ "$2" = restart ] && [ "$3" = --unattended ]; then
  : > "$UPGRADE_TEST_ROOT/restarted"
  exit "$UPGRADE_TEST_RESTART"
fi
exit 90
`)
			writeExecutable(t, filepath.Join(pathDir, "curl"), `#!/bin/sh
case "$*" in
  *api.github.com*) printf '{\n  "tag_name": "v0.2.0"\n}\n' ;;
  *)
    case "$UPGRADE_TEST_FAILURE:$*" in
      download:*swarm_0.2.0_linux_*) exit 22 ;;
    esac
    while [ "$#" -gt 0 ]; do
      if [ "$1" = -o ]; then shift; : > "$1"; exit 0; fi
      shift
    done
    exit 23
    ;;
esac
`)
			writeExecutable(t, filepath.Join(pathDir, "mktemp"), `#!/bin/sh
mkdir "$UPGRADE_TEST_ROOT/payload"
echo "$UPGRADE_TEST_ROOT/payload"
`)
			writeExecutable(t, filepath.Join(pathDir, "sha256sum"), `#!/bin/sh
printf 'checksum\n' >> "$UPGRADE_TEST_ROOT/trace"
[ "$UPGRADE_TEST_FAILURE" != checksum ]
`)
			writeExecutable(t, filepath.Join(pathDir, "tar"), `#!/bin/sh
printf 'extract\n' >> "$UPGRADE_TEST_ROOT/trace"
while [ "$#" -gt 0 ]; do
  if [ "$1" = -C ]; then shift; dest=$1; break; fi
  shift
done
cat > "$dest/swarm" <<'EOF'
#!/bin/sh
if [ "$1" = version ]; then echo 'swarm 0.2.0'; exit 0; fi
if [ "$1" = daemon ] && [ "$2" = restart ] && [ "$3" = --unattended ]; then
  : > "$UPGRADE_TEST_ROOT/restarted"
  exit "$UPGRADE_TEST_RESTART"
fi
exit 90
EOF
chmod +x "$dest/swarm"
`)
			writeExecutable(t, filepath.Join(pathDir, "install"), `#!/bin/sh
printf 'install\n' >> "$UPGRADE_TEST_ROOT/trace"
[ "$UPGRADE_TEST_FAILURE" != install ] || exit 1
cp "$3" "$4"
`)

			script := filepath.Join("..", "..", "..", "packaging", "systemd", "swarm-upgrade.sh")
			cmd := exec.Command("/bin/sh", script)
			cmd.Env = append(os.Environ(),
				"PATH="+pathDir+":"+os.Getenv("PATH"),
				"SWARM_BIN="+filepath.Join(binDir, "swarm"),
				"SWARM_REPO=fixture/swarm",
				"UPGRADE_TEST_ROOT="+root,
				"UPGRADE_TEST_FAILURE="+tc.failure,
				"UPGRADE_TEST_RESTART="+strconv.Itoa(tc.restart),
			)
			output, err := cmd.CombinedOutput()
			status := 0
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("run updater: %v\n%s", err, output)
				}
				status = exitErr.ExitCode()
			}
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d; output: %s", status, tc.wantStatus, output)
			}
			if _, err := os.Stat(filepath.Join(root, "restarted")); err != nil {
				t.Fatalf("restart was not attempted: %v; output: %s", err, output)
			}
			if _, err := os.Stat(filepath.Join(root, "payload")); !os.IsNotExist(err) {
				t.Fatalf("owned payload remains after updater exit (stat err %v); output: %s", err, output)
			}
			version, err := exec.Command(filepath.Join(binDir, "swarm"), "version").Output()
			if err != nil {
				t.Fatalf("read installed fixture version: %v", err)
			}
			wantVersion := "0.1.0"
			if tc.failure == "" {
				wantVersion = "0.2.0"
			}
			if gotVersion := strings.Fields(string(version)); len(gotVersion) != 2 || gotVersion[1] != wantVersion {
				t.Fatalf("installed fixture version = %q, want %s", strings.TrimSpace(string(version)), wantVersion)
			}
			trace, err := os.ReadFile(filepath.Join(root, "trace"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatalf("read updater trace: %v", err)
			}
			wantTrace := map[string]string{
				"":         "checksum\nextract\ninstall\n",
				"download": "",
				"checksum": "checksum\n",
				"install":  "checksum\nextract\ninstall\n",
			}[tc.failure]
			if string(trace) != wantTrace {
				t.Fatalf("fetch trace = %q, want %q", trace, wantTrace)
			}
		})
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}
