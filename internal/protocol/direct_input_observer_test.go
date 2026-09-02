package protocol

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/wire"
)

func TestDirectInputObserverMarksImmediatelyBeforeFirstSendInputAttempt(t *testing.T) {
	d := newSendInputDaemon()
	d.setMetas(statusMeta("sess1", status.TurnIdle, status.InteractionNone))
	sock := tmpSock(t)
	srv, err := Serve(d, sock)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	srv.SetDirectInputObserver(func(local string, class DirectInputClass) error {
		if local != "sess1" {
			t.Errorf("observed local = %q, want sess1", local)
		}
		if class != DirectInputDraft {
			t.Errorf("observed class = %q, want draft", class)
		}
		if st := d.stream(0); st == nil || len(st.written()) != 0 {
			t.Errorf("observer ran after the first Input attempt: stream=%v writes=%v", st, func() []inputWrite {
				if st == nil {
					return nil
				}
				return st.written()
			}())
		}
		once.Do(func() { close(entered) })
		<-release
		return nil
	})

	done := make(chan error, 1)
	go func() {
		done <- srv.SendInput("sess1", SendInputReq{Text: "draft only"})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("direct-input observer was not reached")
	}

	fenceEntered := make(chan struct{})
	fenceDone := make(chan error, 1)
	go func() {
		fenceDone <- srv.WithInputFence("sess1", func() error {
			close(fenceEntered)
			return nil
		})
	}()
	select {
	case <-fenceEntered:
		t.Fatal("observer did not run under attachMu -> inMu")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	if err := <-fenceDone; err != nil {
		t.Fatalf("WithInputFence: %v", err)
	}
	if got := string(concat(d.onlyStream(t).written())); got != "draft only" {
		t.Fatalf("PTY input = %q, want draft only", got)
	}
}

func TestDirectInputObserverFailureRefusesSendInputBeforeBytes(t *testing.T) {
	d := newSendInputDaemon()
	d.setMetas(statusMeta("sess1", status.TurnIdle, status.InteractionNone))
	sock := tmpSock(t)
	srv, err := Serve(d, sock)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	srv.SetDirectInputObserver(func(string, DirectInputClass) error { return errors.New("marker fsync failed") })

	err = srv.SendInput("sess1", SendInputReq{Text: "must not land", Submit: true})
	if err == nil || !strings.Contains(err.Error(), "marker fsync failed") {
		t.Fatalf("SendInput = %v, want marker failure", err)
	}
	if got := concat(d.onlyStream(t).written()); len(got) != 0 {
		t.Fatalf("marker failure still wrote %q", got)
	}
}

func TestDirectInputSubmitAdvancesOnlyAfterSuccessfulEnter(t *testing.T) {
	for _, tc := range []struct {
		name        string
		failAt      int
		wantClasses []DirectInputClass
	}{
		{name: "success", wantClasses: []DirectInputClass{DirectInputDraft, DirectInputSubmitted}},
		{name: "ambiguous_first_write_failure", failAt: 1, wantClasses: []DirectInputClass{DirectInputDraft}},
		{name: "mid_message_enter_failure", failAt: 2, wantClasses: []DirectInputClass{DirectInputDraft}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newSendInputDaemon()
			d.failAt = tc.failAt
			d.setMetas(statusMeta("sess1", status.TurnIdle, status.InteractionNone))
			sock := tmpSock(t)
			srv, err := Serve(d, sock)
			if err != nil {
				t.Fatalf("Serve: %v", err)
			}
			t.Cleanup(func() { _ = srv.Close() })

			var (
				mu      sync.Mutex
				classes []DirectInputClass
			)
			srv.SetDirectInputObserver(func(_ string, class DirectInputClass) error {
				writes := d.onlyStream(t).written()
				mu.Lock()
				classes = append(classes, class)
				mu.Unlock()
				switch class {
				case DirectInputDraft:
					if len(writes) != 0 {
						t.Errorf("Draft marker followed %d Input writes", len(writes))
					}
				case DirectInputSubmitted:
					if len(writes) != 2 || string(writes[1].payload) != "\r" {
						t.Errorf("Submitted marker preceded successful Enter: writes=%q", concat(writes))
					}
				}
				return nil
			})

			err = srv.SendInput("sess1", SendInputReq{Text: "ambiguous", Submit: true})
			if tc.failAt == 0 && err != nil {
				t.Fatalf("SendInput success case: %v", err)
			}
			if tc.failAt != 0 && err == nil {
				t.Fatal("injected Input failure returned nil")
			}
			mu.Lock()
			got := append([]DirectInputClass(nil), classes...)
			mu.Unlock()
			if len(got) != len(tc.wantClasses) {
				t.Fatalf("observer classes = %v, want %v", got, tc.wantClasses)
			}
			for i := range got {
				if got[i] != tc.wantClasses[i] {
					t.Fatalf("observer classes = %v, want %v", got, tc.wantClasses)
				}
			}
		})
	}
}

func TestDirectInputOnlyExplicitEnterCanAdvanceDraft(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  SendInputReq
	}{
		{name: "escape", req: SendInputReq{Key: "esc"}},
		{name: "ctrl_c", req: SendInputReq{Key: "ctrl-c"}},
		{name: "tab", req: SendInputReq{Key: "tab"}},
		{name: "up", req: SendInputReq{Key: "up"}},
		{name: "down", req: SendInputReq{Key: "down"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newSendInputDaemon()
			d.setMetas(statusMeta("sess1", status.TurnIdle, status.InteractionNone))
			sock := tmpSock(t)
			srv, err := Serve(d, sock)
			if err != nil {
				t.Fatalf("Serve: %v", err)
			}
			t.Cleanup(func() { _ = srv.Close() })
			classes := make(chan DirectInputClass, 2)
			srv.SetDirectInputObserver(func(_ string, class DirectInputClass) error {
				classes <- class
				return nil
			})
			if err := srv.SendInput("sess1", tc.req); err != nil {
				t.Fatalf("SendInput: %v", err)
			}
			if got := <-classes; got != DirectInputDraft {
				t.Fatalf("first class = %q, want draft", got)
			}
			select {
			case got := <-classes:
				t.Fatalf("non-Enter key advanced marker to %q", got)
			default:
			}
		})
	}
}

func TestRawDirectInputOnlyExactCRCanAdvanceDraft(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []byte
		want bool
	}{
		{name: "exact_enter", in: []byte{'\r'}, want: true},
		{name: "printable", in: []byte("draft")},
		{name: "paste_with_cr", in: []byte("foo\rbar")},
		{name: "ctrl_c", in: []byte{0x03}},
		{name: "escape_sequence", in: []byte("\x1b[A")},
		{name: "backspace", in: []byte{0x7f}},
		{name: "newline_paste", in: []byte("a\nb")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := rawDirectInputSubmits(tc.in); got != tc.want {
				t.Fatalf("rawDirectInputSubmits(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestDirectInputObserverCoversRawAttachedInputOnOwnerAndRemoteServers(t *testing.T) {
	for _, remote := range []bool{false, true} {
		name := "owner"
		if remote {
			name = "remote"
		}
		t.Run(name, func(t *testing.T) {
			d := newStubDaemon()
			d.setMetas(statusMeta("sess1", status.TurnIdle, status.InteractionNone))
			sock := tmpSock(t)
			var (
				srv *Server
				err error
			)
			if remote {
				srv, err = ServeRemoteWithID(d, sock, "ep-observer")
			} else {
				srv, err = Serve(d, sock)
			}
			if err != nil {
				t.Fatalf("Serve(remote=%v): %v", remote, err)
			}
			t.Cleanup(func() { _ = srv.Close() })

			observed := make(chan struct {
				local string
				class DirectInputClass
			}, 2)
			srv.SetDirectInputObserver(func(local string, class DirectInputClass) error {
				st := d.lastStream()
				if class == DirectInputDraft && (st == nil || len(st.inputBytes()) != 0) {
					t.Errorf("Draft observer ran after raw Input: stream=%v bytes=%q", st, func() []byte {
						if st == nil {
							return nil
						}
						return st.inputBytes()
					}())
				}
				if class == DirectInputSubmitted && (st == nil || string(st.inputBytes()) != "\r") {
					t.Errorf("Submitted observer preceded successful raw Enter: stream=%v", st)
				}
				observed <- struct {
					local string
					class DirectInputClass
				}{local: local, class: class}
				return nil
			})

			payload := []byte("raw draft")
			wantClasses := []DirectInputClass{DirectInputDraft}
			if remote {
				payload = []byte{'\r'}
				wantClasses = append(wantClasses, DirectInputSubmitted)
				rc := rawDial(t, sock)
				rep := rc.hello(Version, []string{CapRemoteGateway})
				sid := rep.EndpointID + "/sess1"
				_ = takeControl(t, rc, rep.EndpointID, sid, 3600)
				rc.writeFrame(wire.TDataIn, payload)
				rc.writeControl(Control{Op: OpList, EndpointID: rep.EndpointID})
				syncControlOp(t, rc, OpList)
			} else {
				c, err := Dial(sock, nil)
				if err != nil {
					t.Fatalf("Dial: %v", err)
				}
				t.Cleanup(func() { _ = c.Close() })
				att, err := c.Attach(c.endpointID + "/sess1")
				if err != nil {
					t.Fatalf("Attach: %v", err)
				}
				t.Cleanup(func() { _ = att.Detach() })
				if err := att.Input(payload); err != nil {
					t.Fatalf("Input: %v", err)
				}
			}

			for _, wantClass := range wantClasses {
				select {
				case got := <-observed:
					if got.local != "sess1" || got.class != wantClass {
						t.Fatalf("observed = %q/%q, want sess1/%q", got.local, got.class, wantClass)
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("raw input was not observed as %q", wantClass)
				}
			}
			if st := d.lastStream(); st == nil || !strings.Contains(string(st.inputBytes()), string(payload)) {
				t.Fatalf("raw input did not reach PTY: stream=%v", st)
			}
		})
	}
}
