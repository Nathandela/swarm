package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/diagnosticlog"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
)

func cleanWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func cleanFixture(t *testing.T) (root, spool string, suffix []byte) {
	t.Helper()
	root = doctorEnv(t)
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	cleanWrite(t, filepath.Join(root, "d.lock"), nil)
	dir := filepath.Join(root, "session-one")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(persist.Meta{ID: "session-one", SchemaVersion: persist.SchemaVersion})
	cleanWrite(t, filepath.Join(dir, "meta.json"), meta)
	spool = filepath.Join(dir, shim.HookSpoolFile)
	s, err := shim.OpenHookSpoolProducer(spool, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Append([]byte(`{"applied":true}`)); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(spool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Append([]byte(`{"retain":true}`)); err != nil {
		t.Fatal(err)
	}
	cert := map[string]any{"incarnation": s.IncarnationID(), "producer": s.ProducerCertificate(), "sequence": 1}
	raw, _ := json.Marshal(cert)
	cleanWrite(t, filepath.Join(dir, "hook.fold"), []byte("1\n"))
	cleanWrite(t, filepath.Join(dir, "hook.fold.cert"), raw)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	all, err := os.ReadFile(spool)
	if err != nil {
		t.Fatal(err)
	}
	suffix = append([]byte(nil), all[len(first):]...)
	// An uncertified torn tail must survive manual cleanup verbatim.
	suffix = append(suffix, 0x01, 0x02, 0x03)
	cleanWrite(t, spool, append(all, 0x01, 0x02, 0x03))
	for _, name := range []string{"history.jsonl", "recovery.receipt", "handoff-input.md", "shim.log"} {
		cleanWrite(t, filepath.Join(dir, name), []byte("preserve "+name))
	}
	return
}

func cleanSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	got := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		value := fmt.Sprintf("%v %d", info.Mode(), info.Size())
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += fmt.Sprintf(" %x", sha256.Sum256(data))
		}
		got[path] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestCleanPreviewPreservesStateAndNeverStartsDaemon(t *testing.T) {
	root, _, _ := cleanFixture(t)
	cleanWrite(t, filepath.Join(root, "d.log"), bytes.Repeat([]byte("x"), diagnosticlog.MaxBytes+1))
	before := cleanSnapshot(t, root)
	var out, errb bytes.Buffer
	if code := dispatch([]string{"clean", "--json"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, &errb)
	}
	var report map[string]any
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report["mode"] != "preview" {
		t.Fatalf("report=%v", report)
	}
	if after := cleanSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("preview changed state")
	}
}

func TestCleanApplyPreservesUncertifiedSuffixAndProtectedFiles(t *testing.T) {
	_, spool, suffix := cleanFixture(t)
	protected := filepath.Join(filepath.Dir(spool), "recovery.receipt")
	before, err := os.ReadFile(protected)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		var out, errb bytes.Buffer
		if code := dispatch([]string{"clean", "--apply", "--json"}, &out, &errb); code != 0 {
			t.Fatalf("exit %d: %s\n%s", code, &errb, &out)
		}
		after, err := os.ReadFile(spool)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(after, suffix) {
			t.Fatalf("suffix changed: %q", after)
		}
	}
	after, err := os.ReadFile(protected)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("recovery receipt changed", err)
	}
}

func TestCleanMissingStateIsNeverCreated(t *testing.T) {
	root := filepath.Join(doctorEnv(t), "absent")
	t.Setenv("SWARM_DAEMON_STATE", root)
	t.Setenv("SWARM_DAEMON_LOG", filepath.Join(root, "daemon.log"))
	t.Setenv("SWARM_DAEMON_LOCK", filepath.Join(root, "daemon.lock"))
	for _, args := range [][]string{{"clean"}, {"clean", "--apply"}, {"clean", "--apply", "extra"}} {
		var out, errb bytes.Buffer
		code := dispatch(args, &out, &errb)
		want := 0
		if len(args) == 3 {
			want = 2
		}
		if code != want {
			t.Fatalf("%v: exit %d want %d: %s", args, code, want, &errb)
		}
		if _, err := os.Lstat(root); !os.IsNotExist(err) {
			t.Fatalf("created absent state: %v", err)
		}
	}
}

func TestCleanRetainsHooksWithoutSingletonCustody(t *testing.T) {
	for _, kind := range []string{"busy", "missing", "symlink", "hardlink", "fifo", "inside-session"} {
		t.Run(kind, func(t *testing.T) {
			root, spool, _ := cleanFixture(t)
			before, err := os.ReadFile(spool)
			if err != nil {
				t.Fatal(err)
			}
			lockPath := filepath.Join(root, "custom.lock")
			cleanWrite(t, lockPath, nil)
			t.Setenv("SWARM_DAEMON_LOCK", lockPath)
			switch kind {
			case "busy":
				f, err := os.Open(lockPath)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = f.Close() })
				if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(lockPath); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(lockPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "d.lock"), lockPath); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(lockPath, lockPath+".alias"); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := os.Remove(lockPath); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(lockPath, 0600); err != nil {
					t.Fatal(err)
				}
			case "inside-session":
				t.Setenv("SWARM_DAEMON_LOCK", spool)
			}
			var out, errb bytes.Buffer
			if code := dispatch([]string{"clean", "--apply", "--json"}, &out, &errb); code != 0 {
				t.Fatalf("exit %d: %s %s", code, &out, &errb)
			}
			after, err := os.ReadFile(spool)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("uncertain singleton changed hook payload", err)
			}
			var report cleanReport
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, item := range report.Items {
				if item.Path == spool && item.Status == "retained" {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing retained outcome: %s", &out)
			}
		})
	}
}

func TestCleanMetadataAndProofRefusals(t *testing.T) {
	for _, kind := range []string{"schema2", "future", "negative", "malformed", "wrong-id", "oversize", "cursor-mismatch", "missing-certificate", "live-producer", "symlink-meta", "hardlink-meta", "fifo-meta"} {
		t.Run(kind, func(t *testing.T) {
			root, spool, _ := cleanFixture(t)
			metaPath := filepath.Join(filepath.Dir(spool), "meta.json")
			switch kind {
			case "schema2", "future", "negative":
				version := 2
				if kind == "future" {
					version = 99
				}
				if kind == "negative" {
					version = -1
				}
				cleanWrite(t, metaPath, []byte(fmt.Sprintf(`{"id":"session-one","schema_version":%d}`, version)))
			case "malformed":
				cleanWrite(t, metaPath, []byte("{"))
			case "wrong-id":
				cleanWrite(t, metaPath, []byte(`{"id":"wrong"}`))
			case "oversize":
				cleanWrite(t, metaPath, bytes.Repeat([]byte(" "), (1<<20)+1))
			case "cursor-mismatch":
				cleanWrite(t, filepath.Join(filepath.Dir(spool), "hook.fold"), []byte("2"))
			case "missing-certificate":
				if err := os.Remove(filepath.Join(filepath.Dir(spool), "hook.fold.cert")); err != nil {
					t.Fatal(err)
				}
			case "live-producer":
				s, err := shim.OpenHookSpoolProducer(spool, 0)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = s.Close() })
			case "symlink-meta":
				if err := os.Rename(metaPath, metaPath+".target"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(metaPath+".target", metaPath); err != nil {
					t.Fatal(err)
				}
			case "hardlink-meta":
				if err := os.Link(metaPath, metaPath+".alias"); err != nil {
					t.Fatal(err)
				}
			case "fifo-meta":
				if err := os.Remove(metaPath); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(metaPath, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(spool)
			if err != nil {
				t.Fatal(err)
			}
			var out, errb bytes.Buffer
			if code := dispatch([]string{"clean", "--apply", "--json"}, &out, &errb); code != 0 {
				t.Fatalf("exit %d: %s %s", code, &out, &errb)
			}
			after, err := os.ReadFile(spool)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("unproven payload changed", err)
			}
			if _, err := os.Stat(filepath.Join(root, "d.sock")); !os.IsNotExist(err) {
				t.Fatal("daemon started")
			}
		})
	}
}

type cleanTestWriter func([]byte) (int, error)

func (w cleanTestWriter) Write(p []byte) (int, error) { return w(p) }

func TestCleanReleasesLocksBeforeOutputAndReportsWriteErrors(t *testing.T) {
	root, _, _ := cleanFixture(t)
	writer := cleanTestWriter(func(p []byte) (int, error) {
		f, err := cleanSingleton(filepath.Join(root, "d.lock"))
		if err != nil {
			t.Errorf("output still holds singleton: %v", err)
		} else {
			_ = f.Close()
		}
		return 0, io.ErrClosedPipe
	})
	var errb bytes.Buffer
	if code := dispatch([]string{"clean", "--json"}, writer, &errb); code != 1 {
		t.Fatalf("write failure exit=%d", code)
	}
}

func TestCleanPartialInventoryKeepsCompletedLogResult(t *testing.T) {
	root, _, _ := cleanFixture(t)
	logPath := filepath.Join(root, "d.log")
	cleanWrite(t, logPath, bytes.Repeat([]byte("x"), diagnosticlog.MaxBytes+1))
	cc, err := clientConfig()
	if err != nil {
		t.Fatal(err)
	}
	report := collectClean(cc, true, 0, time.Now().Add(time.Second))
	if report.Complete || len(report.Items) < 2 || report.Items[0].Status != "cleaned" {
		t.Fatalf("report=%+v", report)
	}
	info, err := os.Stat(logPath)
	if err != nil || info.Size() != 0 {
		t.Fatal("log cleanup was not preserved", err)
	}
}

func TestCleanRefusesStateAndSessionDirectoryAliases(t *testing.T) {
	for _, target := range []string{"state", "session"} {
		t.Run(target, func(t *testing.T) {
			root, spool, _ := cleanFixture(t)
			path := root
			if target == "session" {
				path = filepath.Dir(spool)
			}
			moved := path + "-moved"
			if err := os.Rename(path, moved); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Remove(path); _ = os.RemoveAll(moved) })
			if err := os.Symlink(moved, path); err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(spool)
			if err != nil {
				t.Fatal(err)
			}
			var out, errb bytes.Buffer
			code := dispatch([]string{"clean", "--apply", "--json"}, &out, &errb)
			if code != 0 && code != 1 {
				t.Fatalf("exit %d: %s", code, &errb)
			}
			after, err := os.ReadFile(spool)
			if err != nil || !bytes.Equal(original, after) {
				t.Fatal("alias target changed", err)
			}
		})
	}
}

func TestCleanPartialMutationMustReportError(t *testing.T) {
	for _, err := range []error{errCleanUnsafe, shim.ErrHookRetirementUnproven, os.ErrNotExist} {
		item := cleanOutcome("spool", "hook-prefix", nil, nil, 12, true, true, err)
		if item.Status != "error" {
			t.Fatalf("partial mutation reported %q for %v", item.Status, err)
		}
	}
}

func TestCleanLogProgressSurvivesHookInspectionError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses metadata permission failure")
	}
	root, spool, _ := cleanFixture(t)
	logPath := filepath.Join(root, "d.log")
	cleanWrite(t, logPath, bytes.Repeat([]byte("x"), diagnosticlog.MaxBytes+1))
	if err := os.Chmod(filepath.Join(filepath.Dir(spool), "meta.json"), 0); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := dispatch([]string{"clean", "--apply", "--json"}, &out, &errb); code != 1 {
		t.Fatalf("exit %d: %s %s", code, &out, &errb)
	}
	var report cleanReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Complete || report.Items[0].Status != "cleaned" {
		t.Fatalf("misleading result: %s", &out)
	}
}

func TestCleanTextReportsObservedSizesAndRejectsForce(t *testing.T) {
	root, _, _ := cleanFixture(t)
	cleanWrite(t, filepath.Join(root, "d.log"), []byte("diagnostic"))
	var out, errb bytes.Buffer
	if code := dispatch([]string{"clean"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, &errb)
	}
	if !strings.Contains(out.String(), "allocated=") || !strings.Contains(out.String(), "logical=") {
		t.Fatalf("missing measurements: %s", &out)
	}
	before := cleanSnapshot(t, root)
	out.Reset()
	errb.Reset()
	if code := dispatch([]string{"clean", "--apply", "--force"}, &out, &errb); code != 2 {
		t.Fatalf("force accepted: %d", code)
	}
	if !reflect.DeepEqual(before, cleanSnapshot(t, root)) {
		t.Fatal("invalid invocation mutated state")
	}
}

func TestCleanProtectsSingletonViaAncestorAlias(t *testing.T) {
	root, spool, _ := cleanFixture(t)
	before, err := os.ReadFile(spool)
	if err != nil {
		t.Fatal(err)
	}
	alias := root + "-parent-alias"
	if err := os.Symlink(filepath.Dir(root), alias); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(alias) })
	for _, lockPath := range []string{
		filepath.Join(alias, filepath.Base(root), "session-one", shim.HookSpoolFile),
		alias + "/" + filepath.Base(root) + "/session-one/../session-one/" + shim.HookSpoolFile,
	} {
		t.Setenv("SWARM_DAEMON_LOCK", lockPath)
		var out, errb bytes.Buffer
		if code := dispatch([]string{"clean", "--apply", "--json"}, &out, &errb); code != 0 {
			t.Fatalf("exit %d: %s %s", code, &out, &errb)
		}
		after, err := os.ReadFile(spool)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("cleanup replaced aliased singleton", err)
		}
	}
}

func TestCleanRetainsUnsafeHookSidecars(t *testing.T) {
	for _, name := range []string{"hook.fold", "hook.fold.cert", "hooks.spool.producer", "hooks.spool.producer.lock", "hooks.spool.open.lock", "hooks.spool.incarnation", "hooks.spool.floor"} {
		for _, kind := range []string{"symlink", "hardlink", "fifo"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				_, spool, _ := cleanFixture(t)
				path := filepath.Join(filepath.Dir(spool), name)
				if _, err := os.Lstat(path); os.IsNotExist(err) {
					cleanWrite(t, path, []byte("0\n"))
				}
				if err := os.Rename(path, path+".target"); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "symlink":
					if err := os.Symlink(path+".target", path); err != nil {
						t.Fatal(err)
					}
				case "hardlink":
					if err := os.Link(path+".target", path); err != nil {
						t.Fatal(err)
					}
				case "fifo":
					if err := syscall.Mkfifo(path, 0600); err != nil {
						t.Fatal(err)
					}
				}
				before, err := os.ReadFile(spool)
				if err != nil {
					t.Fatal(err)
				}
				var out, errb bytes.Buffer
				if code := dispatch([]string{"clean", "--apply", "--json"}, &out, &errb); code != 0 {
					t.Fatalf("exit %d: %s %s", code, &out, &errb)
				}
				after, err := os.ReadFile(spool)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("unsafe sidecar permitted cleanup", err)
				}
			})
		}
	}
}

func TestCleanProtectsSingletonCaseAliases(t *testing.T) {
	root, spool, _ := cleanFixture(t)
	upper := filepath.Join(root, "SESSION-ONE", "HOOKS.SPOOL")
	lowerInfo, err := os.Stat(spool)
	if err != nil {
		t.Fatal(err)
	}
	upperInfo, err := os.Stat(upper)
	if os.IsNotExist(err) {
		t.Skip("case-sensitive filesystem")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(lowerInfo, upperInfo) {
		t.Fatal("case alias points at unexpected object")
	}
	t.Setenv("SWARM_DAEMON_LOCK", upper)
	before, err := os.ReadFile(spool)
	if err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := dispatch([]string{"clean", "--apply", "--json"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s %s", code, &out, &errb)
	}
	after, err := os.ReadFile(spool)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("cleanup replaced case-aliased singleton", err)
	}
}
