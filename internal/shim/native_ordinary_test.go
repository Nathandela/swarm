package shim

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func init() {
	if len(os.Args) != 3 || os.Args[1] != "fixture-native-ordinary" {
		return
	}
	dir := os.Args[2]
	code, err := Run(Config{NativeProvider: "codex", SessionID: "ordinary-native", SessionDir: dir, SocketPath: filepath.Join(dir, "shim.sock"), Cwd: dir, Argv: []string{"/bin/sh", "-c", "exit 0"}, Env: []string{"HOME=" + dir, "PATH=/bin:/usr/bin"}, Cols: 80, Rows: 24, GraceTimeout: 25 * time.Millisecond})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(90)
	}
	os.Exit(code)
}
func TestOrdinaryNativeProviderLaunchHasNoLoginOrUnsupportedContainmentRegression(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(selfExe(t), "fixture-native-ordinary", dir)
	cmd.Env = []string{}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ordinary native launch: %v: %s", err, out)
	}
	var proof NativeStoppedInfo
	raw, err := os.ReadFile(filepath.Join(dir, NativeStoppedFile))
	if err != nil || json.Unmarshal(raw, &proof) != nil {
		t.Fatal("ordinary provider has no native stop proof")
	}
	if !proof.WritersStopped || proof.Native.Provider != "codex" || proof.Native.Runtime.Epoch != 0 || proof.Native.ShimPID != cmd.Process.Pid {
		t.Fatalf("incorrect ordinary native proof: %+v", proof)
	}
	if proof.DescendantsContained != (runtime.GOOS == "linux") {
		t.Fatal("platform proof claimed unsupported descendant custody")
	}
}
