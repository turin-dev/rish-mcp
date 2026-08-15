package relay

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func testDevicePair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	serverConn := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade test connection: %v", err)
			return
		}
		serverConn <- conn
	}))
	t.Cleanup(srv.Close)

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial test connection: %v", err)
	}
	server := <-serverConn
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	return server, client
}

func newTestDevice(id string, conn *websocket.Conn) *Device {
	now := time.Now()
	return &Device{
		ID: id, Name: "test", SDK: "35", Kind: KindAndroid,
		AgentVersion: "test", ConnectedAt: now, lastSeen: now,
		conn: conn, pending: make(map[string]chan pendingResult),
	}
}

func TestRegistryResolve(t *testing.T) {
	r := NewRegistry()
	if _, err := r.resolve(""); !errors.Is(err, ErrNoDevice) {
		t.Fatalf("empty registry: got %v, want ErrNoDevice", err)
	}

	r.add(newTestDevice("one", nil))
	if got, err := r.resolve(""); err != nil || got.ID != "one" {
		t.Fatalf("single device resolve: got %v, %v", got, err)
	}
	if _, err := r.resolve("missing"); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("missing device: got %v, want ErrDeviceNotFound", err)
	}

	r.add(newTestDevice("two", nil))
	if _, err := r.resolve(""); !errors.Is(err, ErrAmbiguousDevice) {
		t.Fatalf("multiple devices: got %v, want ErrAmbiguousDevice", err)
	}
}

func TestRegistryListSnapshot(t *testing.T) {
	r := NewRegistry()
	d := newTestDevice("one", nil)
	d.Name = "Pixel"
	d.Kind = KindWatch
	d.AgentVersionCode = 7
	d.pending["request"] = make(chan pendingResult, 1)
	r.add(d)

	got := r.List()
	if len(got) != 1 || got[0].ID != "one" || got[0].Name != "Pixel" || got[0].Kind != KindWatch {
		t.Fatalf("unexpected device snapshot: %#v", got)
	}
	if got[0].Pending != 1 || got[0].AgentVersionCode != 7 {
		t.Fatalf("snapshot omitted mutable metadata: %#v", got[0])
	}
}

func TestRegistryExecSuccess(t *testing.T) {
	r := NewRegistry()
	server, client := testDevicePair(t)
	r.add(newTestDevice("one", server))

	go func() {
		_, raw, err := client.ReadMessage()
		if err != nil {
			t.Errorf("agent read exec frame: %v", err)
			return
		}
		var frame execFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Errorf("decode exec frame: %v", err)
			return
		}
		// Resolve the pending request after observing the frame. The request ID
		// is deliberately read from the wire so this also checks that Exec sends it.
		r.resolveResult("one", frame.ReqID, Result{Code: 0, Stdout: frame.Cmd, DurationMs: 12})
	}()

	got, err := r.Exec(context.Background(), "one", "id", time.Second)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if got.Code != 0 || got.Stdout != "id" || got.DurationMs != 12 {
		t.Fatalf("unexpected result: %#v", got)
	}
}

func TestRegistryExecContextCancellation(t *testing.T) {
	r := NewRegistry()
	server, client := testDevicePair(t)
	r.add(newTestDevice("one", server))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := r.Exec(ctx, "one", "sleep 10", time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Exec: got %v, want context.Canceled", err)
	}
	_ = client
}

func TestRegistryExecContextCancellationAfterAdmission(t *testing.T) {
	r := NewRegistry()
	server, client := testDevicePair(t)
	r.add(newTestDevice("one", server))
	ctx, cancel := context.WithCancel(context.Background())

	type outcome struct {
		result Result
		err    error
	}
	ch := make(chan outcome, 1)
	go func() {
		result, err := r.Exec(ctx, "one", "id", time.Minute)
		ch <- outcome{result: result, err: err}
	}()

	// Wait for the exec frame on the wire — proves we cleared resolve and the
	// admission gate and are now inside the select, blocked on ch/timer/ctx.
	if _, _, err := client.ReadMessage(); err != nil {
		t.Fatalf("agent read exec frame: %v", err)
	}

	cancel() // cancel while the call is waiting on a result

	select {
	case got := <-ch:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("cancelled Exec: got %v, want context.Canceled", got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("Exec did not return after context cancel")
	}
}

func TestRegistryExecTimeout(t *testing.T) {
	r := NewRegistry()
	server, client := testDevicePair(t)
	r.add(newTestDevice("one", server))

	started := time.Now()
	_, err := r.Exec(context.Background(), "one", "sleep 10", time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("timed out Exec: got %v, want ErrTimeout", err)
	}
	if elapsed := time.Since(started); elapsed < 2*time.Second {
		t.Fatalf("timeout returned before grace period: %s", elapsed)
	}
	_ = client
}

func TestRegistryExecPendingLimit(t *testing.T) {
	r := NewRegistry()
	server, client := testDevicePair(t)
	d := newTestDevice("one", server)
	r.add(d)

	// Fill the pending map to the cap without issuing real Exec calls — the
	// goroutines in TestRegistryExecWriteFailure etc. prove the conn side; here
	// we only exercise the admission gate in Exec.
	d.mu.Lock()
	for i := 0; i < maxPendingPerDevice; i++ {
		d.pending[fmt.Sprintf("req-%d", i)] = make(chan pendingResult, 1)
	}
	d.mu.Unlock()

	_, err := r.Exec(context.Background(), "one", "id", time.Second)
	if !errors.Is(err, ErrTooManyPending) {
		t.Fatalf("over-cap Exec: got %v, want ErrTooManyPending", err)
	}

	// One slot frees up → admission succeeds again. The exec frame goes out on
	// the wire; nobody resolves it, so use a short timeout and expect ErrTimeout
	// (proving we got past the gate, not a hang).
	d.mu.Lock()
	delete(d.pending, "req-0")
	d.mu.Unlock()

	_, err = r.Exec(context.Background(), "one", "id", time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("freed-slot Exec: got %v, want ErrTimeout (past gate)", err)
	}
	_ = client
}

func TestRegistryExecWriteFailure(t *testing.T) {
	r := NewRegistry()
	server, client := testDevicePair(t)
	_ = client.Close()
	_ = server.Close()
	r.add(newTestDevice("one", server))

	_, err := r.Exec(context.Background(), "one", "id", time.Second)
	if err == nil || !strings.Contains(err.Error(), `send command to device "one"`) {
		t.Fatalf("write failure: got %v", err)
	}
}

func TestResolveResultIgnoresUnknownRequest(t *testing.T) {
	r := NewRegistry()
	server, client := testDevicePair(t)
	r.add(newTestDevice("one", server))

	r.resolveResult("one", "unknown", Result{Code: 1})
	if _, err := r.resolve("one"); err != nil {
		t.Fatalf("device removed while resolving unknown request: %v", err)
	}
	_ = client
}

func TestRandomHex(t *testing.T) {
	got := randomHex(16)
	if len(got) != 32 {
		t.Fatalf("randomHex length = %d, want 32", len(got))
	}
	if _, err := hex.DecodeString(got); err != nil {
		t.Fatalf("randomHex returned non-hex data: %v", err)
	}
}

func TestRegistryDisconnectFailsPendingExec(t *testing.T) {
	r := NewRegistry()
	server, client := testDevicePair(t)
	d := newTestDevice("one", server)
	r.add(d)

	type outcome struct {
		result Result
		err    error
	}
	ch := make(chan outcome, 1)
	go func() {
		result, err := r.Exec(context.Background(), "one", "id", time.Minute)
		ch <- outcome{result: result, err: err}
	}()

	if _, _, err := client.ReadMessage(); err != nil {
		t.Fatalf("agent read exec frame: %v", err)
	}
	r.disconnect(d, ErrDeviceDisconnected)

	select {
	case got := <-ch:
		if !errors.Is(got.err, ErrDeviceDisconnected) {
			t.Fatalf("disconnected Exec: got %v, want ErrDeviceDisconnected", got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("Exec did not return after disconnect")
	}
	if d.conn != nil {
		t.Fatal("disconnect left device connection assigned")
	}
}

func TestRegistryReplacementKeepsNewOwner(t *testing.T) {
	r := NewRegistry()
	oldServer, oldClient := testDevicePair(t)
	newServer, newClient := testDevicePair(t)
	old := newTestDevice("one", oldServer)
	current := newTestDevice("one", newServer)
	if got := r.add(old); got != nil {
		t.Fatalf("first add returned %#v", got)
	}
	if got := r.add(current); got != old {
		t.Fatalf("replacement returned %#v, want old device", got)
	}

	r.disconnect(old, ErrDeviceReplaced)
	got, err := r.resolve("one")
	if err != nil || got != current {
		t.Fatalf("replacement owner = %#v, %v; want current device", got, err)
	}
	if old.conn != nil {
		t.Fatal("old device connection was not cleared")
	}
	_ = oldClient
	_ = newClient
}

// --- Registry edge cases ---

func TestRegistryRemove(t *testing.T) {
	r := NewRegistry()
	server, client := testDevicePair(t)
	d := newTestDevice("one", server)
	r.add(d)

	// Remove with wrong conn → should not delete
	r.remove("one", client)
	if _, err := r.resolve("one"); err != nil {
		t.Fatal("remove with wrong conn should not delete device")
	}

	// Remove with matching conn → should delete
	r.remove("one", server)
	if _, err := r.resolve("one"); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatal("remove with matching conn should delete device")
	}

	// Remove non-existent device → no-op (should not panic)
	r.remove("ghost", server)
}

func TestNormalizeKind(t *testing.T) {
	cases := []struct {
		input string
		want  Kind
	}{
		{"watch", KindWatch},
		{"android", KindAndroid},
		{"", KindAndroid},
		{"foobar", KindAndroid},
		{"WATCH", KindAndroid}, // case-sensitive
	}
	for _, tc := range cases {
		got := NormalizeKind(tc.input)
		if got != tc.want {
			t.Errorf("NormalizeKind(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestRegistryExecResolveError(t *testing.T) {
	r := NewRegistry()
	r.add(newTestDevice("one", nil))
	_, err := r.Exec(context.Background(), "nonexistent", "id", time.Second)
	if !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("Exec with unknown device: got %v, want ErrDeviceNotFound", err)
	}

	// Empty registry → ErrNoDevice, not ErrDeviceNotFound
	r2 := NewRegistry()
	if _, err := r2.Exec(context.Background(), "", "id", time.Second); !errors.Is(err, ErrNoDevice) {
		t.Fatalf("Exec on empty registry: got %v, want ErrNoDevice", err)
	}
}

func TestRegistryDisconnectFullPendingChannel(t *testing.T) {
	r := NewRegistry()
	d := newTestDevice("one", nil)
	r.add(d)

	// Fill the pending channel to capacity before disconnect, so the
	// select in disconnect hits the default branch (dropped result).
	d.mu.Lock()
	ch := make(chan pendingResult, 1)
	ch <- pendingResult{result: Result{Code: 0}}
	d.pending["req-full"] = ch
	d.mu.Unlock()

	r.disconnect(d, ErrDeviceDisconnected)

	d.mu.Lock()
	if len(d.pending) != 0 {
		t.Fatalf("disconnect left %d pending entries", len(d.pending))
	}
	d.mu.Unlock()

	// Disconnecting twice must be a no-op (pending map already drained).
	r.disconnect(d, ErrDeviceDisconnected)
}

func TestRegistryExecDisconnectedDevice(t *testing.T) {
	r := NewRegistry()
	server, client := testDevicePair(t)
	d := newTestDevice("one", server)
	r.add(d)

	// Set conn to nil to simulate a disconnected device
	d.mu.Lock()
	d.conn = nil
	d.mu.Unlock()

	_, err := r.Exec(context.Background(), "one", "id", time.Second)
	if !errors.Is(err, ErrDeviceDisconnected) {
		t.Fatalf("Exec on disconnected device: got %v, want ErrDeviceDisconnected", err)
	}
	_ = client
}

func TestResolveResultUnknownDevice(t *testing.T) {
	r := NewRegistry()
	// Should not panic or error
	r.resolveResult("nonexistent", "req-1", Result{Code: 1})
}

func TestResolveResultLateResult(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })

	r := NewRegistry()
	server, client := testDevicePair(t)
	d := newTestDevice("one", server)
	r.add(d)

	// Pre-fill the pending channel so resolveResult's select hits default
	ch := make(chan pendingResult, 1)
	ch <- pendingResult{result: Result{Code: 0}}
	d.mu.Lock()
	d.pending["req-late"] = ch
	d.mu.Unlock()

	r.resolveResult("one", "req-late", Result{Code: 42})

	if !strings.Contains(buf.String(), "dropped late result") {
		t.Fatalf("expected 'dropped late result' log, got: %s", buf.String())
	}
	_ = client
}

func TestSanitizeLogField(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"hello", "hello"},
		{"hello\nworld", "helloworld"},
		{"hello\r\nworld", "helloworld"},
		{"hello\x00world", "helloworld"},
		{"tab\there", "tab\there"},
		{string(make([]byte, 100)), ""}, // all control chars → empty
	}
	for _, tc := range cases {
		got := sanitizeLogField(tc.input)
		if got != tc.want {
			t.Errorf("sanitizeLogField(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}

	// Truncation test: 64+ chars
	long := string(bytes.Repeat([]byte{'a'}, 80))
	got := sanitizeLogField(long)
	if len(got) != 64 {
		t.Errorf("sanitizeLogField(long) length = %d, want 64", len(got))
	}
}

func TestClientIP(t *testing.T) {
	cases := []struct {
		remoteAddr string
		want       string
	}{
		{"127.0.0.1:12345", "127.0.0.1"},
		{"[::1]:12345", "::1"},
		{"192.168.1.1:80", "192.168.1.1"},
		// SplitHostPort failure cases → returns RemoteAddr as-is
		{"127.0.0.1", "127.0.0.1"},
		{"::1", "::1"},
		{"", ""},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.remoteAddr
		got := clientIP(r)
		if got != tc.want {
			t.Errorf("clientIP(%q) = %q, want %q", tc.remoteAddr, got, tc.want)
		}
	}
}

func TestAgentConnLimiterAcquireRelease(t *testing.T) {
	l := NewAgentConnLimiter()
	ip := "10.0.0.1"

	// Acquire 10 times — all should succeed
	for i := 0; i < 10; i++ {
		if !l.acquire(ip) {
			t.Fatalf("acquire %d should succeed", i+1)
		}
	}

	// 11th should fail
	if l.acquire(ip) {
		t.Fatal("11th acquire should fail")
	}

	// Release 9 times — map should still have the key
	for i := 0; i < 9; i++ {
		l.release(ip)
	}
	l.mu.Lock()
	_, ok := l.active[ip]
	l.mu.Unlock()
	if !ok {
		t.Fatal("expected ip to still be in map after 9 releases")
	}

	// 10th release → should delete the key
	l.release(ip)
	l.mu.Lock()
	_, ok = l.active[ip]
	l.mu.Unlock()
	if ok {
		t.Fatal("expected ip to be removed from map after last release")
	}

	// Release on empty map → no-op (should not panic)
	l.release(ip)
}
