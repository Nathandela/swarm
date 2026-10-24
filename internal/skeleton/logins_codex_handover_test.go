package skeleton

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/appserver"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/loginworker"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/coder/websocket"
)

func TestCodexHandoverAccountAPIAdoptionABAAndReadiness(t *testing.T) {
	sock := backendAuthTestSocket(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	desired := codexAuthSelection{id: "a", epoch: 1, revision: 1}
	installed := ""
	allowed := json.RawMessage(`true`)
	wrong := false
	imports := 0
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, e := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if e != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		for {
			_, raw, e := ws.Read(r.Context())
			if e != nil {
				return
			}
			var fr struct {
				ID     json.RawMessage            `json:"id"`
				Method string                     `json:"method"`
				Params map[string]json.RawMessage `json:"params"`
			}
			if json.Unmarshal(raw, &fr) != nil {
				return
			}
			mu.Lock()
			var result any
			switch fr.Method {
			case "account/login/start":
				_ = json.Unmarshal(fr.Params["chatgptAccountId"], &installed)
				imports++
				result = map[string]any{"type": "chatgptAuthTokens"}
			case "account/rateLimits/read":
				account := installed
				if wrong {
					account = "other"
				}
				result = map[string]any{"accountId": account, "ordinaryUsageAllowed": allowed, "rateLimits": map[string]any{}}
			default:
				mu.Unlock()
				t.Errorf("unexpected native operation: %s", fr.Method)
				return
			}
			mu.Unlock()
			body, _ := json.Marshal(map[string]any{"id": fr.ID, "result": result})
			if ws.Write(r.Context(), websocket.MessageText, body) != nil {
				return
			}
		}
	})}
	go func() { _ = server.Serve(ln) }()
	defer func() { _ = server.Close() }()
	auth := newBackendAuthCoordinator(func(context.Context, bool) (backendAuthTokens, error) {
		mu.Lock()
		selection := desired
		mu.Unlock()
		return backendAuthTokens{AccessToken: "synthetic", AccountID: selection.id, selection: selection, current: func() bool { mu.Lock(); defer mu.Unlock(); return desired == selection }}, nil
	}, func() bool { return true })
	conn, err := appserver.Dial(context.Background(), sock, appserver.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	auth.bind(conn)
	var receipts []string
	state := newCodexHandoverFeed(auth, func() bool { return true }, func(selection codexAuthSelection, phase string, _ *codexScopedUsage) error {
		receipts = append(receipts, phase)
		return nil
	})
	for i, id := range []string{"a", "b", "a", "a"} {
		mu.Lock()
		revision := uint64(1)
		if i == 3 {
			revision = 2
		}
		desired = codexAuthSelection{id: id, epoch: uint64(i + 1), revision: revision}
		selection := desired
		mu.Unlock()
		state.invalidate()
		if err := state.synchronize(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !state.readyFor(selection) {
			t.Fatal("matching account API adoption did not admit selected grant")
		}
	}
	mu.Lock()
	allowed = json.RawMessage(`false`)
	selection := desired
	before := imports
	mu.Unlock()
	state.invalidate()
	if err := state.synchronize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state.readyFor(selection) || state.adoptedSelection() != selection {
		t.Fatal("false permission conflated historical adoption and current readiness")
	}
	mu.Lock()
	after := imports
	allowed = json.RawMessage(`null`)
	mu.Unlock()
	if after != before {
		t.Fatal("ordinary scoped read reset native authentication")
	}
	state.invalidate()
	if err := state.synchronize(context.Background()); err == nil {
		t.Fatal("null permission was treated as adoption evidence")
	}
	if state.readyFor(selection) || state.adoptedSelection() != selection {
		t.Fatal("failed read erased history or retained readiness")
	}
	mu.Lock()
	allowed = json.RawMessage(`true`)
	wrong = true
	mu.Unlock()
	state.invalidate()
	if err := state.synchronize(context.Background()); err == nil {
		t.Fatal("foreign account API result admitted target")
	}
	if len(receipts) < 4 {
		t.Fatal("native acknowledgement/adoption receipts missing")
	}
}

func TestCodexHandoverManualSelectionDoesNotStopBusyCohort(t *testing.T) {
	f := newLoginFixture(t)
	enableCodexNativeGrantUsage(t, f)
	r, _ := f.m.store.Snapshot()
	_, target, err := f.m.store.Add(r.Revision, logins.ProviderCodex, "b", usageCodexGrant("b"))
	if err != nil {
		t.Fatal(err)
	}
	c := newCodexHandoverCoordinator(f.m)
	f.m.codexHandover = c
	members := []logins.NativeMember{{SessionID: "source-1", NativeIncarnation: "native-1", ConversationID: "conversation-1", NativePID: 101, NativeStartTime: 201}, {SessionID: "source-2", NativeIncarnation: "native-2", ConversationID: "conversation-2", NativePID: 102, NativeStartTime: 202}}
	c.members = func() ([]logins.NativeMember, error) { return members, nil }
	c.ended = func(logins.NativeMember) bool { return false }
	first := f.rows["source-1"]
	first.Status.Turn = status.TurnActive
	f.rows[first.ID] = first
	second := f.rows["source-2"]
	second.Status.Interaction = status.InteractionPermission
	f.rows[second.ID] = second
	before, _ := f.m.store.Snapshot()
	if _, err = f.m.BeginSwitch(logins.ProviderCodex, target.ID, "manual"); err != nil {
		t.Fatal(err)
	}
	selected, _ := f.m.store.Snapshot()
	h, ok := selected.Handovers[logins.ProviderCodex]
	if !ok || h.ToID != target.ID || len(h.Members) != 2 || len(selected.Switches) != 0 || len(f.launched) != 0 || f.released != 0 {
		t.Fatal("normal native selection used stopped-writer/restart path")
	}
	if selected.Providers[logins.ProviderCodex].Epoch <= before.Providers[logins.ProviderCodex].Epoch {
		t.Fatal("desired epoch not advanced")
	}
	if f.m.inputGate("source-1") == nil {
		t.Fatal("unadopted process admitted a new turn")
	}
	if _, err = f.m.BeginSwitch(logins.ProviderCodex, f.first.ID, "back to A"); err != nil {
		t.Fatal(err)
	}
	again, _ := f.m.store.Snapshot()
	back := again.Handovers[logins.ProviderCodex]
	if back.ID != h.ID || back.ToID != f.first.ID || back.Epoch <= h.Epoch || len(back.Members) != 2 {
		t.Fatal("A-B-A lost exact pending cohort")
	}
}

func TestCodexHandoverZeroBaselinePreparedBeforeBootstrapRead(t *testing.T) {
	f := newLoginFixture(t)
	enableCodexNativeGrantUsage(t, f)
	c := newCodexHandoverCoordinator(f.m)
	f.m.codexHandover = c
	c.members = func() ([]logins.NativeMember, error) { return nil, nil }
	if err := f.m.prepareCodexSelection(); err != nil {
		t.Fatal(err)
	}
	r, _ := f.m.store.Snapshot()
	if _, err := selectedCodexAuth(r); err != nil {
		t.Fatal("owned baseline was not selected before native bootstrap")
	}
}

func TestCodexHandoverScopedReadCanReceiveRefreshWithoutDeadlock(t *testing.T) {
	sock := backendAuthTestSocket(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	callback := make(chan struct{}, 1)
	generic := make(chan string, 1)
	var imports atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, e := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if e != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		first := true
		var waiting json.RawMessage
		send := func(id json.RawMessage, result any) {
			raw, _ := json.Marshal(map[string]any{"id": id, "result": result})
			_ = ws.Write(r.Context(), websocket.MessageText, raw)
		}
		for {
			_, raw, e := ws.Read(r.Context())
			if e != nil {
				return
			}
			var fr struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(raw, &fr) != nil {
				return
			}
			switch fr.Method {
			case "account/login/start":
				imports.Add(1)
				send(fr.ID, map[string]string{"type": "chatgptAuthTokens"})
			case "account/rateLimits/read":
				if first {
					first = false
					waiting = fr.ID
					_ = ws.Write(r.Context(), websocket.MessageText, []byte(`{"id":"refresh","method":"account/chatgptAuthTokens/refresh","params":{"reason":"unauthorized"}}`))
					_ = ws.Write(r.Context(), websocket.MessageText, []byte(`{"id":"permission","method":"item/commandExecution/requestApproval","params":{}}`))
				} else {
					send(fr.ID, map[string]any{"accountId": "a", "ordinaryUsageAllowed": true})
				}
			case "":
				if string(fr.ID) == `"refresh"` {
					callback <- struct{}{}
					send(waiting, map[string]any{"accountId": "a", "ordinaryUsageAllowed": true})
				}
			default:
				t.Errorf("unexpected native operation: %s", fr.Method)
				return
			}
		}
	})}
	go func() { _ = server.Serve(ln) }()
	defer func() { _ = server.Close() }()
	selection := codexAuthSelection{id: "a", epoch: 1, revision: 1}
	auth := newBackendAuthCoordinator(func(context.Context, bool) (backendAuthTokens, error) {
		return backendAuthTokens{AccessToken: "synthetic", AccountID: "a", selection: selection, current: func() bool { return true }}, nil
	}, func() bool { return true })
	state := newCodexHandoverFeed(auth, func() bool { return true }, nil)
	conn, err := appserver.Dial(context.Background(), sock, appserver.Options{OnFrame: func(method string, raw json.RawMessage) {
		if !auth.intercept(method, raw) {
			generic <- method
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	auth.bind(conn)
	auth.startSync()
	defer auth.close()
	_ = state.synchronize(context.Background())
	select {
	case <-callback:
	case <-time.After(time.Second):
		t.Fatal("scoped account read starved its native refresh callback")
	}
	select {
	case method := <-generic:
		if method != "item/commandExecution/requestApproval" {
			t.Fatal("wrong generic control event")
		}
	case <-time.After(time.Second):
		t.Fatal("scoped read blocked permission ingress")
	}
	waitBackendIngress(t, func() bool { return state.readyFor(selection) })
	if imports.Load() != 1 {
		t.Fatal("ordinary refresh callback reset native authentication")
	}
}

func TestCodexHandoverLateOldAccountReadCannotAdoptNewSelection(t *testing.T) {
	sock := backendAuthTestSocket(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	sent := make(chan struct{})
	var mu sync.Mutex
	desired := codexAuthSelection{id: "a", epoch: 1, revision: 1}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, e := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if e != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		var writes sync.Mutex
		send := func(id json.RawMessage, result any) {
			writes.Lock()
			defer writes.Unlock()
			raw, _ := json.Marshal(map[string]any{"id": id, "result": result})
			_ = ws.Write(r.Context(), websocket.MessageText, raw)
		}
		account := ""
		first := true
		for {
			_, raw, e := ws.Read(r.Context())
			if e != nil {
				return
			}
			var fr struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params struct {
					Account string `json:"chatgptAccountId"`
				} `json:"params"`
			}
			_ = json.Unmarshal(raw, &fr)
			switch fr.Method {
			case "account/login/start":
				account = fr.Params.Account
				send(fr.ID, map[string]string{"type": "chatgptAuthTokens"})
			case "account/rateLimits/read":
				if first {
					first = false
					id := append(json.RawMessage(nil), fr.ID...)
					close(started)
					go func() {
						<-release
						send(id, map[string]any{"accountId": "a", "ordinaryUsageAllowed": true})
						close(sent)
					}()
				} else {
					send(fr.ID, map[string]any{"accountId": account, "ordinaryUsageAllowed": true})
				}
			default:
				t.Errorf("unexpected native request: %s", fr.Method)
				return
			}
		}
	})}
	go func() { _ = server.Serve(ln) }()
	defer func() { _ = server.Close() }()
	auth := newBackendAuthCoordinator(func(context.Context, bool) (backendAuthTokens, error) {
		mu.Lock()
		selection := desired
		mu.Unlock()
		return backendAuthTokens{AccessToken: "synthetic", AccountID: selection.id, selection: selection, current: func() bool { mu.Lock(); defer mu.Unlock(); return desired == selection }}, nil
	}, func() bool { return true })
	var adoptedA atomic.Bool
	state := newCodexHandoverFeed(auth, func() bool { return true }, func(selection codexAuthSelection, phase string, _ *codexScopedUsage) error {
		if phase == "adopted" && selection.id == "a" {
			adoptedA.Store(true)
		}
		return nil
	})
	conn, err := appserver.Dial(context.Background(), sock, appserver.Options{})
	if err != nil {
		t.Fatal(err)
	}
	auth.bind(conn)
	defer auth.close()
	first := make(chan error, 1)
	go func() { first <- state.synchronize(context.Background()) }()
	<-started
	mu.Lock()
	desired = codexAuthSelection{id: "b", epoch: 2, revision: 1}
	selection := desired
	mu.Unlock()
	state.invalidate()
	if err := <-first; err == nil {
		t.Fatal("superseded native read succeeded")
	}
	if err := state.synchronize(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-sent
	var barrier json.RawMessage
	if err := conn.Call(context.Background(), "account/rateLimits/read", map[string]any{}, &barrier); err != nil {
		t.Fatal(err)
	}
	if adoptedA.Load() || !state.readyFor(selection) || state.adoptedSelection() != selection {
		t.Fatal("late A result changed B adoption/readiness")
	}
}

func TestCodexHandoverHealthyMemberProgressesWhileLaggardPending(t *testing.T) {
	f := newLoginFixture(t)
	enableCodexNativeGrantUsage(t, f)
	r, _ := f.m.store.Snapshot()
	_, target, err := f.m.store.Add(r.Revision, logins.ProviderCodex, "b", usageCodexGrant("b"))
	if err != nil {
		t.Fatal(err)
	}
	c := newCodexHandoverCoordinator(f.m)
	f.m.codexHandover = c
	members := []logins.NativeMember{{SessionID: "source-1", NativeIncarnation: "native-1", ConversationID: "conversation-1", NativePID: 101, NativeStartTime: 201}, {SessionID: "source-2", NativeIncarnation: "native-2", ConversationID: "conversation-2", NativePID: 102, NativeStartTime: 202}}
	c.members = func() ([]logins.NativeMember, error) { return members, nil }
	c.ended = func(logins.NativeMember) bool { return false }
	if _, err = f.m.BeginSwitch(logins.ProviderCodex, target.ID, "manual"); err != nil {
		t.Fatal(err)
	}
	r, _ = f.m.store.Snapshot()
	selection, err := selectedCodexAuth(r)
	if err != nil {
		t.Fatal(err)
	}
	feed := newBackendFeed()
	s := &codexHandoverFeed{auth: &backendAuthCoordinator{syncWake: make(chan struct{}, 1)}, native: members[0], feed: feed, current: func() bool { return true }, installed: selection, target: selection, readySelection: selection, ready: true, lastAttempt: time.Now()}
	s.publish = func(selection codexAuthSelection, phase string, usage *codexScopedUsage) error {
		return f.m.publishCodexAdoption(s, selection, phase, usage)
	}
	c.feeds[feed] = s
	token := backendAuthTokens{selection: selection, current: func() bool { return true }}
	if err = s.publishProof(context.Background(), token, "adopted", &codexScopedUsage{allowed: true, order: 1}, 0); err != nil {
		t.Fatal(err)
	}
	f.m.stepCodexHandover()
	if err = f.m.inputGate("source-1"); err != nil {
		t.Fatalf("laggard blocked healthy native: %v", err)
	}
	if f.m.inputGate("source-2") == nil {
		t.Fatal("laggard admitted a new turn")
	}
	r, _ = f.m.store.Snapshot()
	h := r.Handovers[logins.ProviderCodex]
	if h.Members[0].Phase != "adopted" || h.Members[1].Phase != "pending" {
		t.Fatal("partial adoption was not preserved")
	}
	// Old unscoped A notifications can only invalidate readiness, never label B.
	raw := []byte(`{"params":{"threadId":"old-a","error":{"codexErrorInfo":"usageLimitExceeded"}}}`)
	f.m.observeNativeFailure("source-1", "instance", feed, "error", raw)
	r, _ = f.m.store.Snapshot()
	login, _ := loginByID(r, target.ID)
	if login.LimitKnown || login.AuthState != logins.AuthReady {
		t.Fatal("old unscoped failure poisoned selected B")
	}
	if s.readyFor(selection) {
		t.Fatal("terminal hint retained stale readiness")
	}
	if len(f.launched) != 0 || f.released != 0 {
		t.Fatal("hot adoption restarted a native process")
	}
	// A scoped account-level rejection from another member fences every B turn.
	peer := &codexHandoverFeed{native: members[1], feed: newBackendFeed(), current: func() bool { return true }}
	peer.publish = func(selection codexAuthSelection, phase string, usage *codexScopedUsage) error {
		return f.m.publishCodexAdoption(peer, selection, phase, usage)
	}
	if err = peer.publishProof(context.Background(), token, "adopted", &codexScopedUsage{allowed: false, order: 2}, 0); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.ready = true
	s.readySequence = s.sequence.Load()
	s.mu.Unlock()
	if f.m.inputGate("source-1") == nil {
		t.Fatal("historical ready member ignored current account permission=false")
	}
	r, _ = f.m.store.Snapshot()
	source, _ := loginByID(r, f.first.ID)
	targetLogin, _ := loginByID(r, target.ID)
	if source.LimitKnown || !targetLogin.LimitKnown {
		t.Fatal("scoped rejection was attributed to another saved login")
	}
}

func TestCodexHandoverSameAccountReloginAdvancesDesiredRevision(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("owned native grant replacement is Linux-qualified")
	}
	f := newLoginFixture(t)
	enableCodexNativeGrantUsage(t, f)
	c := newCodexHandoverCoordinator(f.m)
	f.m.codexHandover = c
	c.members = func() ([]logins.NativeMember, error) { return nil, nil }
	if err := f.m.prepareCodexSelection(); err != nil {
		t.Fatal(err)
	}
	r, _ := f.m.store.Snapshot()
	if h, ok := r.Handovers[logins.ProviderCodex]; ok {
		var err error
		r, err = f.m.store.CompleteCodexHandover(r.Revision, h.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	before := r.Providers[logins.ProviderCodex]
	lease, err := f.m.store.AcquireCodexRelogin(f.first.ID, strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	r, login, err := lease.ReplaceCredential(r.Revision, usageCodexGrant("owned"), strings.Repeat("a", 32))
	closeErr := lease.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("same-account native credential capture failed: %v %v", err, closeErr)
	}
	if _, err = f.m.BeginSwitch(logins.ProviderCodex, login.ID, "reauth"); err != nil {
		t.Fatal(err)
	}
	after, _ := f.m.store.Snapshot()
	p := after.Providers[logins.ProviderCodex]
	if p.ActiveID != before.ActiveID || p.Epoch <= before.Epoch || p.DesiredCredentialRevision != login.CredentialRevision || p.DesiredCredentialRevision <= before.DesiredCredentialRevision || len(after.Switches) != 0 || len(f.launched) != 0 {
		t.Fatal("same-account reauth did not create a fresh native desired tuple")
	}
}

func TestCodexHandoverRetiredNativeCannotReceiveCallbackCredentials(t *testing.T) {
	sock := backendAuthTestSocket(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	replied := make(chan []byte, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, e := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if e != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		_ = ws.Write(r.Context(), websocket.MessageText, []byte(`{"id":1,"method":"account/chatgptAuthTokens/refresh","params":{"reason":"unauthorized"}}`))
		_, raw, e := ws.Read(r.Context())
		if e == nil {
			replied <- raw
		}
	})}
	go func() { _ = server.Serve(ln) }()
	defer func() { _ = server.Close() }()
	var access atomic.Int32
	auth := newBackendAuthCoordinator(func(context.Context, bool) (backendAuthTokens, error) {
		access.Add(1)
		return backendAuthTokens{AccessToken: "PRIVATE", AccountID: "PII"}, nil
	}, func() bool { return true })
	_ = newCodexHandoverFeed(auth, func() bool { return false }, nil)
	conn, err := appserver.Dial(context.Background(), sock, appserver.Options{OnFrame: func(method string, raw json.RawMessage) { auth.intercept(method, raw) }})
	if err != nil {
		t.Fatal(err)
	}
	auth.bind(conn)
	defer auth.close()
	select {
	case raw := <-replied:
		var fr struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &fr) != nil || fr.Error.Message != "backend_auth_unavailable" || access.Load() != 0 {
			t.Fatal("obsolete native received private callback access")
		}
	case <-time.After(time.Second):
		t.Fatal("obsolete callback was not rejected")
	}
}

func TestCodexHandoverCloseCancelsScopedReadAndJoinsBothWorkers(t *testing.T) {
	sock := backendAuthTestSocket(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, e := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if e != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		for {
			_, raw, e := ws.Read(r.Context())
			if e != nil {
				return
			}
			var fr struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			_ = json.Unmarshal(raw, &fr)
			switch fr.Method {
			case "account/login/start":
				body, _ := json.Marshal(map[string]any{"id": fr.ID, "result": map[string]string{"type": "chatgptAuthTokens"}})
				_ = ws.Write(r.Context(), websocket.MessageText, body)
			case "account/rateLimits/read":
				close(entered)
			default:
				t.Errorf("unexpected native request: %s", fr.Method)
				return
			}
		}
	})}
	go func() { _ = server.Serve(ln) }()
	defer func() { _ = server.Close() }()
	selection := codexAuthSelection{id: "a", epoch: 1, revision: 1}
	auth := newBackendAuthCoordinator(func(context.Context, bool) (backendAuthTokens, error) {
		return backendAuthTokens{AccessToken: "synthetic", AccountID: "a", selection: selection, current: func() bool { return true }}, nil
	}, func() bool { return true })
	state := newCodexHandoverFeed(auth, func() bool { return true }, nil)
	conn, err := appserver.Dial(context.Background(), sock, appserver.Options{})
	if err != nil {
		t.Fatal(err)
	}
	auth.bind(conn)
	auth.startSync()
	auth.requestSync()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("scoped read not started")
	}
	done := make(chan struct{})
	go func() { auth.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("auth close left a scoped RPC or callback worker alive")
	}
	if state.readyFor(selection) {
		t.Fatal("closed coordinator retained admission readiness")
	}
	select {
	case <-auth.done:
	default:
		t.Fatal("refresh worker not joined")
	}
	select {
	case <-auth.syncDone:
	default:
		t.Fatal("sync worker not joined")
	}
}

func TestCodexHandoverBoundsParallelNativeRPCWithoutFleetIdleBarrier(t *testing.T) {
	sock := backendAuthTestSocket(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 5)
	release := make(chan struct{})
	var active, maximum atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, e := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if e != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		done := make(chan struct{})
		defer close(done)
		var writes sync.Mutex
		send := func(id json.RawMessage, result any) {
			writes.Lock()
			defer writes.Unlock()
			body, _ := json.Marshal(map[string]any{"id": id, "result": result})
			_ = ws.Write(r.Context(), websocket.MessageText, body)
		}
		for {
			_, raw, e := ws.Read(r.Context())
			if e != nil {
				return
			}
			var fr struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			_ = json.Unmarshal(raw, &fr)
			switch fr.Method {
			case "account/login/start":
				send(fr.ID, map[string]string{"type": "chatgptAuthTokens"})
			case "account/rateLimits/read":
				id := append(json.RawMessage(nil), fr.ID...)
				go func() {
					count := active.Add(1)
					defer active.Add(-1)
					for {
						old := maximum.Load()
						if count <= old || maximum.CompareAndSwap(old, count) {
							break
						}
					}
					entered <- struct{}{}
					select {
					case <-release:
						send(id, map[string]any{"accountId": "a", "ordinaryUsageAllowed": true})
					case <-done:
					}
				}()
			default:
				t.Errorf("unexpected native operation: %s", fr.Method)
				return
			}
		}
	})}
	go func() { _ = server.Serve(ln) }()
	defer func() { _ = server.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	slots := make(chan struct{}, 4)
	selection := codexAuthSelection{id: "a", epoch: 1, revision: 1}
	results := make(chan error, 5)
	start := make(chan struct{})
	for i := 0; i < 5; i++ {
		auth := newBackendAuthCoordinator(func(context.Context, bool) (backendAuthTokens, error) {
			return backendAuthTokens{AccessToken: "synthetic", AccountID: "a", selection: selection, current: func() bool { return true }}, nil
		}, func() bool { return true })
		state := newCodexHandoverFeed(auth, func() bool { return true }, nil)
		state.slots = slots
		conn, e := appserver.Dial(ctx, sock, appserver.Options{})
		if e != nil {
			t.Fatal(e)
		}
		auth.bind(conn)
		defer auth.close()
		go func() { <-start; results <- state.synchronize(ctx) }()
	}
	close(start)
	for i := 0; i < 4; i++ {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("four independent native feeds did not progress")
		}
	}
	select {
	case <-entered:
		t.Fatal("fifth native account RPC exceeded concurrency bound")
	case <-time.After(50 * time.Millisecond):
	}
	release <- struct{}{}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("healthy feed waited for the whole fleet to finish")
	}
	for i := 0; i < 4; i++ {
		release <- struct{}{}
	}
	for i := 0; i < 5; i++ {
		select {
		case e := <-results:
			if e != nil {
				t.Fatal(e)
			}
		case <-ctx.Done():
			t.Fatal("native sync workers did not finish")
		}
	}
	if maximum.Load() > 4 {
		t.Fatal("parallel native RPC bound exceeded")
	}
}

func TestCodexHandoverUsageHintsCoalescePerProviderAndTerminalHintBypassesCadence(t *testing.T) {
	f := newLoginFixture(t)
	enableCodexNativeGrantUsage(t, f)
	if err := f.m.prepareCodexSelection(); err != nil {
		t.Fatal(err)
	}
	r, _ := f.m.store.Snapshot()
	selection, err := selectedCodexAuth(r)
	if err != nil {
		t.Fatal(err)
	}
	c := f.m.codexHandover
	if c == nil {
		c = newCodexHandoverCoordinator(f.m)
		f.m.codexHandover = c
	}
	states := make([]*codexHandoverFeed, 2)
	for i, local := range []string{"source-1", "source-2"} {
		feed := newBackendFeed()
		s := &codexHandoverFeed{auth: &backendAuthCoordinator{syncWake: make(chan struct{}, 1)}, native: logins.NativeMember{SessionID: local}, feed: feed, current: func() bool { return true }, adopted: selection, readySelection: selection, ready: true}
		states[i] = s
		c.feeds[feed] = s
	}
	for range 100 {
		f.m.noteCodexHint("source-1", false)
	}
	queued := len(states[0].auth.syncWake) + len(states[1].auth.syncWake)
	if queued != 1 {
		t.Fatalf("usage burst queued %d account reads; want one provider read", queued)
	}
	for _, s := range states {
		select {
		case <-s.auth.syncWake:
		default:
		}
	}
	f.m.noteCodexHint("source-2", true)
	if len(states[1].auth.syncWake) != 1 || states[1].readyFor(selection) {
		t.Fatal("terminal hint did not immediately fence and schedule its affected feed")
	}
	// A second terminal error must also invalidate an already-running read.
	before := states[1].sequence.Load()
	cancelled := false
	states[1].cancelRead = func() { cancelled = true }
	f.m.noteCodexHint("source-2", true)
	if states[1].sequence.Load() == before || !cancelled {
		t.Fatal("terminal hint allowed an older in-flight read to restore readiness")
	}
}

func TestCodexScopedUsageRejectsMalformedWeeklyBucket(t *testing.T) {
	for _, bucket := range []string{`{"usedPercent":101,"windowDurationMins":10080,"resetsAt":1}`, `{"usedPercent":-1,"windowDurationMins":10080,"resetsAt":1}`} {
		if _, ok := decodeCodexScopedUsage(json.RawMessage(`{"accountId":"a","ordinaryUsageAllowed":true,"rateLimits":{"primary":`+bucket+`}}`), "a", time.Now()); ok {
			t.Fatal("malformed present weekly bucket authorized readiness")
		}
	}
}

func TestCodexHandoverOlderTrueCannotClearNewerFalse(t *testing.T) {
	f := newLoginFixture(t)
	enableCodexNativeGrantUsage(t, f)
	c := newCodexHandoverCoordinator(f.m)
	f.m.codexHandover = c
	if err := f.m.prepareCodexSelection(); err != nil {
		t.Fatal(err)
	}
	r, _ := f.m.store.Snapshot()
	sel, _ := selectedCodexAuth(r)
	s := &codexHandoverFeed{native: logins.NativeMember{SessionID: "source-1"}, current: func() bool { return true }}
	s.publish = func(selection codexAuthSelection, phase string, usage *codexScopedUsage) error {
		return f.m.publishCodexAdoption(s, selection, phase, usage)
	}
	token := backendAuthTokens{selection: sel}
	if err := s.publishProof(context.Background(), token, "adopted", &codexScopedUsage{allowed: false, order: 2}, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.publishProof(context.Background(), token, "adopted", &codexScopedUsage{allowed: true, order: 1}, 0); err == nil {
		t.Fatal("older true publication was accepted after newer false")
	}
	r, _ = f.m.store.Snapshot()
	login, _ := loginByID(r, sel.id)
	if c.usageAllowed || !login.LimitKnown {
		t.Fatal("older true cleared provider or durable false")
	}
	if err := s.publishProof(context.Background(), token, "adopted", &codexScopedUsage{allowed: true, order: 3}, 0); err != nil {
		t.Fatal(err)
	}
	if !c.usageAllowed {
		t.Fatal("genuinely fresh true did not restore account permission")
	}
}

func TestCodexHandoverInvalidationDuringUsageWriteKeepsProviderFailClosed(t *testing.T) {
	f := newLoginFixture(t)
	enableCodexNativeGrantUsage(t, f)
	c := newCodexHandoverCoordinator(f.m)
	f.m.codexHandover = c
	if err := f.m.prepareCodexSelection(); err != nil {
		t.Fatal(err)
	}
	r, _ := f.m.store.Snapshot()
	sel, _ := selectedCodexAuth(r)
	s := &codexHandoverFeed{native: logins.NativeMember{SessionID: "source-1"}, current: func() bool { return true }}
	s.publish = func(selection codexAuthSelection, phase string, usage *codexScopedUsage) error {
		return f.m.publishCodexAdoption(s, selection, phase, usage)
	}
	token := backendAuthTokens{selection: sel}
	if err := s.publishProof(context.Background(), token, "adopted", &codexScopedUsage{allowed: false, order: 1}, 0); err != nil {
		t.Fatal(err)
	}
	c.afterUsageWrite = s.invalidate
	if err := s.publishProof(context.Background(), token, "adopted", &codexScopedUsage{allowed: true, order: 2}, 0); err == nil {
		t.Fatal("invalidated in-disk publication succeeded")
	}
	r, _ = f.m.store.Snapshot()
	login, _ := loginByID(r, sel.id)
	if c.usageAllowed || !login.LimitKnown {
		t.Fatal("invalidated read cleared provider or durable false")
	}
}

func TestCodexHandoverPublicationReplacementAndAuthWriteCannotDeadlock(t *testing.T) {
	f := newLoginFixture(t)
	enableCodexNativeGrantUsage(t, f)
	c := newCodexHandoverCoordinator(f.m)
	f.m.codexHandover = c
	if err := f.m.prepareCodexSelection(); err != nil {
		t.Fatal(err)
	}
	r, _ := f.m.store.Snapshot()
	selection, _ := selectedCodexAuth(r)
	sock := backendAuthTestSocket(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		for {
			_, raw, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			var frame struct {
				ID json.RawMessage `json:"id"`
			}
			_ = json.Unmarshal(raw, &frame)
			if len(frame.ID) > 0 {
				body, _ := json.Marshal(map[string]any{"id": frame.ID, "result": map[string]string{"type": "chatgptAuthTokens"}})
				_ = ws.Write(r.Context(), websocket.MessageText, body)
			}
		}
	})}
	go func() { _ = server.Serve(ln) }()
	defer func() { _ = server.Close() }()
	conn, err := appserver.Dial(context.Background(), sock, appserver.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f.m.d.contextGuards = &contextGuardManager{}
	f.m.d.contextGuards.replaceMu.Lock()
	var releaseReplacement sync.Once
	unlockReplacement := func() { releaseReplacement.Do(f.m.d.contextGuards.replaceMu.Unlock) }
	defer unlockReplacement()
	writeReached := make(chan struct{})
	releasePublication := make(chan struct{})
	c.afterUsageWrite = func() { close(writeReached); <-releasePublication }
	s := &codexHandoverFeed{native: logins.NativeMember{SessionID: "source-1"}, current: func() bool { return true }}
	s.publish = func(selection codexAuthSelection, phase string, usage *codexScopedUsage) error {
		return f.m.publishCodexAdoption(s, selection, phase, usage)
	}
	published := make(chan error, 1)
	go func() {
		published <- s.publishProof(ctx, backendAuthTokens{selection: selection}, "adopted", &codexScopedUsage{allowed: true, order: 1}, 0)
	}()
	gate := &f.m.admission[loginProviderIndex(logins.ProviderCodex)]
	// In the old cycle publication owns admission while waiting for replacement.
	deadline := time.Now().Add(time.Second)
	for gate.TryLock() {
		gate.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("publication never acquired admission")
		}
		time.Sleep(time.Millisecond)
	}
	guardReached := make(chan struct{})
	auth := newBackendAuthCoordinator(nil, func() bool { return true })
	auth.bind(conn)
	defer auth.close()
	installed := make(chan error, 1)
	go func() {
		installed <- auth.install(ctx, backendAuthTokens{AccessToken: "synthetic", AccountID: "a", acquire: func() (func(), bool) { close(guardReached); gate.Lock(); return gate.Unlock, true }}, 0)
	}()
	select {
	case <-guardReached:
	case <-ctx.Done():
		t.Fatal("auth did not reach guarded socket write")
	}
	replaced := make(chan error, 1)
	go func() { err := conn.Notify(ctx, "test/replacement", nil); unlockReplacement(); replaced <- err }()
	close(releasePublication)
	select {
	case err := <-published:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		unlockReplacement()
		<-published
		<-installed
		<-replaced
		t.Fatal("publication/admission, replacement, and native write formed a lock cycle")
	}
	if err := <-installed; err != nil {
		t.Fatal(err)
	}
	if err := <-replaced; err != nil {
		t.Fatal(err)
	}
}

func TestCodexHandoverInvalidatedFalseCannotExhaustOrRotate(t *testing.T) {
	f := newLoginFixture(t)
	enableCodexNativeGrantUsage(t, f)
	c := newCodexHandoverCoordinator(f.m)
	f.m.codexHandover = c
	c.members = func() ([]logins.NativeMember, error) { return nil, nil }
	if err := f.m.prepareCodexSelection(); err != nil {
		t.Fatal(err)
	}
	r, _ := f.m.store.Snapshot()
	if _, _, err := f.m.store.Add(r.Revision, logins.ProviderCodex, "valid alternate", usageCodexGrant("alternate")); err != nil {
		t.Fatal(err)
	}
	r, _ = f.m.store.Snapshot()
	sel, _ := selectedCodexAuth(r)
	s := &codexHandoverFeed{native: logins.NativeMember{SessionID: "source-1"}, current: func() bool { return true }}
	s.publish = func(selection codexAuthSelection, phase string, usage *codexScopedUsage) error {
		return f.m.publishCodexAdoption(s, selection, phase, usage)
	}
	token := backendAuthTokens{selection: sel}
	if err := s.publishProof(context.Background(), token, "adopted", &codexScopedUsage{allowed: true, order: 1}, 0); err != nil {
		t.Fatal(err)
	}
	c.afterUsageWrite = s.invalidate
	if err := s.publishProof(context.Background(), token, "adopted", &codexScopedUsage{allowed: false, order: 2}, 0); err == nil {
		t.Fatal("invalidated false read was accepted")
	}
	r, _ = f.m.store.Snapshot()
	login, _ := loginByID(r, sel.id)
	if login.LimitKnown {
		t.Error("invalidated false read left durable exhaustion")
	}
	f.m.step()
	r, _ = f.m.store.Snapshot()
	if r.Providers[logins.ProviderCodex].ActiveID != sel.id {
		t.Fatal("invalidated false read rotated desired login")
	}
	// If restoration cannot persist, a leftover limit still lacks valid evidence.
	if _, err := f.m.store.UpdateExhaustion(r.Revision, sel.id, time.Time{}, false); err != nil {
		t.Fatal(err)
	}
	f.m.step()
	r, _ = f.m.store.Snapshot()
	if r.Providers[logins.ProviderCodex].ActiveID != sel.id {
		t.Fatal("leftover invalidated exhaustion authorized automatic rotation")
	}
}

func TestCodexHandoverInvalidationSerializesWithFinalPublication(t *testing.T) {
	f := newLoginFixture(t)
	enableCodexNativeGrantUsage(t, f)
	c := newCodexHandoverCoordinator(f.m)
	f.m.codexHandover = c
	if err := f.m.prepareCodexSelection(); err != nil {
		t.Fatal(err)
	}
	r, _ := f.m.store.Snapshot()
	sel, _ := selectedCodexAuth(r)
	s := &codexHandoverFeed{native: logins.NativeMember{SessionID: "source-1"}, current: func() bool { return true }}
	s.publish = func(selection codexAuthSelection, phase string, usage *codexScopedUsage) error {
		return f.m.publishCodexAdoption(s, selection, phase, usage)
	}
	finalFence := make(chan struct{})
	c.afterUsageWrite = func() {
		s.mu.Lock()
		original := s.publishFence
		s.publishFence = func() bool { close(finalFence); return original() }
		s.mu.Unlock()
		c.mu.Lock()
	}
	published := make(chan error, 1)
	go func() {
		published <- s.publishProof(context.Background(), backendAuthTokens{selection: sel}, "adopted", &codexScopedUsage{allowed: true, order: 1}, 0)
	}()
	<-finalFence
	invalidated := make(chan struct{})
	go func() { s.invalidate(); close(invalidated) }()
	// The publisher owns the metadata lock until its provider assignment completes.
	end := time.Now().Add(50 * time.Millisecond)
	for s.sequence.Load() == 0 && time.Now().Before(end) {
		runtime.Gosched()
	}
	crossed := s.sequence.Load() != 0
	c.mu.Unlock()
	<-published
	<-invalidated
	if crossed {
		t.Fatal("invalidation advanced sequence inside final publication critical section")
	}
}

func TestCodexHandoverManualSelectionWinsStaleAutomaticRejection(t *testing.T) {
	f := newLoginFixture(t)
	enableCodexNativeGrantUsage(t, f)
	c := newCodexHandoverCoordinator(f.m)
	f.m.codexHandover = c
	c.members = func() ([]logins.NativeMember, error) { return nil, nil }
	if err := f.m.prepareCodexSelection(); err != nil {
		t.Fatal(err)
	}
	r, _ := f.m.store.Snapshot()
	sel, _ := selectedCodexAuth(r)
	c.usageSelection = sel
	c.usageValid = true
	c.usageAllowed = false
	if _, err := f.m.store.UpdateExhaustion(r.Revision, sel.id, time.Time{}, false); err != nil {
		t.Fatal(err)
	}
	r, _ = f.m.store.Snapshot()
	_, b, err := f.m.store.Add(r.Revision, logins.ProviderCodex, "b", usageCodexGrant("b"))
	if err != nil {
		t.Fatal(err)
	}
	r, _ = f.m.store.Snapshot()
	_, dest, err := f.m.store.Add(r.Revision, logins.ProviderCodex, "c", usageCodexGrant("c"))
	if err != nil {
		t.Fatal(err)
	}
	stale, _ := f.m.store.Snapshot()
	if _, err = f.m.BeginSwitch(logins.ProviderCodex, b.ID, "manual"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.m.BeginSwitch(logins.ProviderCodex, dest.ID, "manual"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.m.beginCodexAutomaticHandover(stale); err == nil {
		t.Fatal("stale automatic rejection superseded newer manual selection")
	}
	r, _ = f.m.store.Snapshot()
	if r.Providers[logins.ProviderCodex].ActiveID != dest.ID {
		t.Fatal("manual destination was overwritten")
	}
	// A proof can become unknown without a registry mutation.
	c.usageSelection, _ = selectedCodexAuth(r)
	c.usageValid = false
	if _, err = f.m.beginCodexAutomaticHandover(r); err == nil {
		t.Fatal("invalidated provider evidence authorized automatic selection")
	}
	c.usageValid = true
	if _, err = f.m.store.UpdateExhaustion(r.Revision, dest.ID, time.Time{}, false); err != nil {
		t.Fatal(err)
	}
	r, _ = f.m.store.Snapshot()
	if _, err = f.m.beginCodexAutomaticHandover(r); err != nil {
		t.Fatal(err)
	}
	r, _ = f.m.store.Snapshot()
	if r.Providers[logins.ProviderCodex].ActiveID == dest.ID {
		t.Fatal("fresh provider rejection did not select an eligible alternate")
	}
}

func TestCodexHandoverUnorderedUsageCannotMutateReceiptsOrAvailability(t *testing.T) {
	f := newLoginFixture(t)
	enableCodexNativeGrantUsage(t, f)
	c := newCodexHandoverCoordinator(f.m)
	f.m.codexHandover = c
	native := logins.NativeMember{SessionID: "source-1", NativeIncarnation: "native-1", NativePID: 101, NativeStartTime: 201}
	c.members = func() ([]logins.NativeMember, error) { return []logins.NativeMember{native}, nil }
	if err := f.m.prepareCodexSelection(); err != nil {
		t.Fatal(err)
	}
	r, _ := f.m.store.Snapshot()
	selection, _ := selectedCodexAuth(r)
	s := &codexHandoverFeed{native: native, current: func() bool { return true }}
	s.publish = func(selection codexAuthSelection, phase string, usage *codexScopedUsage) error {
		return f.m.publishCodexAdoption(s, selection, phase, usage)
	}
	if err := s.publishProof(context.Background(), backendAuthTokens{selection: selection}, "adopted", &codexScopedUsage{allowed: false}, 0); err == nil {
		t.Fatal("unordered usage accepted")
	}
	after, _ := f.m.store.Snapshot()
	if after.Revision != r.Revision {
		t.Fatal("unordered result mutated registry before rejection")
	}
}

func TestCodexHandoverReloginBetweenUsageWriteAndRestoreCannotPoisonFreshGrant(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("owned native grant replacement is Linux-qualified")
	}
	f := newLoginFixture(t)
	enableCodexNativeGrantUsage(t, f)
	c := newCodexHandoverCoordinator(f.m)
	f.m.codexHandover = c
	c.members = func() ([]logins.NativeMember, error) { return nil, nil }
	if err := f.m.prepareCodexSelection(); err != nil {
		t.Fatal(err)
	}
	r, _ := f.m.store.Snapshot()
	selection, _ := selectedCodexAuth(r)
	s := &codexHandoverFeed{native: logins.NativeMember{SessionID: "source-1"}, current: func() bool { return true }}
	s.publish = func(selection codexAuthSelection, phase string, usage *codexScopedUsage) error {
		return f.m.publishCodexAdoption(s, selection, phase, usage)
	}
	token := backendAuthTokens{selection: selection, current: func() bool {
		latest, _ := f.m.store.Snapshot()
		now, err := selectedCodexAuth(latest)
		return err == nil && now == selection
	}}
	if err := s.publishProof(context.Background(), token, "adopted", &codexScopedUsage{allowed: false, order: 1}, 0); err != nil {
		t.Fatal(err)
	}
	c.afterUsageWrite = func() {
		latest, _ := f.m.store.Snapshot()
		jobID := strings.Repeat("b", 32)
		lease, err := f.m.store.AcquireCodexRelogin(selection.id, jobID)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = lease.ReplaceCredential(latest.Revision, usageCodexGrant("owned"), jobID)
		closeErr := lease.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("relogin failed: %v %v", err, closeErr)
		}
	}
	if err := s.publishProof(context.Background(), token, "adopted", &codexScopedUsage{allowed: true, order: 2}, 0); err == nil {
		t.Fatal("obsolete read accepted after relogin")
	}
	after, _ := f.m.store.Snapshot()
	fresh, _ := loginByID(after, selection.id)
	if fresh.CredentialRevision <= selection.revision || fresh.LimitKnown {
		t.Fatal("obsolete restore poisoned replacement credential")
	}
}

func TestCodexHandoverCompletedReloginWaitsForProviderAdmission(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("owned native grant replacement is Linux-qualified")
	}
	f := newLoginFixture(t)
	enableCodexNativeGrantUsage(t, f)
	c := newCodexHandoverCoordinator(f.m)
	f.m.codexHandover = c
	c.members = func() ([]logins.NativeMember, error) { return nil, nil }
	if err := f.m.prepareCodexSelection(); err != nil {
		t.Fatal(err)
	}
	r, _ := f.m.store.Snapshot()
	selection, _ := selectedCodexAuth(r)
	api := &coreAPI{logins: f.m}
	gate := &f.m.admission[loginProviderIndex(logins.ProviderCodex)]
	gate.Lock()
	done := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		done <- api.consumeCodexRelogin(r.Revision, loginworker.Config{LoginID: selection.id, JobID: strings.Repeat("c", 32)}, usageCodexGrant("owned"))
	}()
	<-started
	var early error
	earlyDone := false
	select {
	case early = <-done:
		earlyDone = true
	case <-time.After(100 * time.Millisecond):
	}
	gate.Unlock()
	if earlyDone {
		t.Fatalf("completed relogin crossed an owned native write admission fence: %v", early)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	after, _ := f.m.store.Snapshot()
	fresh, _ := loginByID(after, selection.id)
	if fresh.CredentialRevision <= selection.revision {
		t.Fatal("released relogin did not publish fresh credential")
	}
}
