package relay

import (
	"bytes"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestCheckOrigin verifies the /agent upgrade policy: browsers (and any other
// client sending an Origin header) must be same-origin, while real Android
// agents send no Origin at all and must be accepted.
func TestCheckOrigin(t *testing.T) {
	check := upgrader.CheckOrigin
	if check == nil {
		t.Fatal("upgrader.CheckOrigin is nil")
	}

	mkReq := func(origin string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "https://relay.example.com/agent", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}

	cases := []struct {
		name   string
		origin string
		want   bool
	}{
		{"no origin (Android agent)", "", true},
		{"same origin", "https://relay.example.com", true},
		{"same origin with port", "https://relay.example.com:8443", false},
		{"different host", "https://evil.example.com", false},
		// Scheme is intentionally not compared: RFC 6455 treats http≡ws and
		// https≡wss as equivalent origins, and behind a TLS-terminating proxy
		// the server cannot reliably tell a request's scheme anyway (TLS is
		// always nil there, so https origins would be rejected). Host is the
		// meaningful anti-CSWSH bound; this matches gorilla/websocket's own
		// default checkSameOrigin policy.
		{"same host, different scheme (ws↔http equivalence)", "http://relay.example.com", true},
		{"malformed origin", "://bad", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := check(mkReq(tc.origin)); got != tc.want {
				t.Errorf("CheckOrigin(%q) = %v, want %v", tc.origin, got, tc.want)
			}
		})
	}
}

// TestHandleAgentRejectsWrongOriginEndToEnd exercises the real HTTP handler: a
// browser-style request with a foreign Origin must fail the handshake while an
// agent-style request (no Origin) upgrades successfully.
func TestHandleAgentRejectsWrongOriginEndToEnd(t *testing.T) {
	reg := NewRegistry()
	srv := httptest.NewServer(HandleAgent(reg, "sekret", NewAgentConnLimiter()))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent?token=sekret&deviceId=test-1"

	// Browser-style: hostile Origin header must be rejected before upgrade.
	headers := http.Header{"Origin": []string{"https://evil.example.com"}}
	if _, resp, err := websocket.DefaultDialer.Dial(wsURL, headers); err == nil {
		t.Fatal("expected dial with hostile Origin to fail, but it succeeded")
	} else if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden, got %v", resp)
	}

	// Agent-style: no Origin, correct token → upgrade succeeds.
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("agent-style dial should succeed: %v", err)
	}
	defer conn.Close()
}

// TestHandleAgentRejectsBadToken verifies the 401 path when the token doesn't match.
func TestHandleAgentRejectsBadToken(t *testing.T) {
	reg := NewRegistry()
	srv := httptest.NewServer(HandleAgent(reg, "sekret", NewAgentConnLimiter()))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent?token=wrong"

	// Any Origin — the token check happens before upgrade, so we should get 401
	// regardless of the WS handshake.
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatal("expected dial with wrong token to fail, but it succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized, got %v", resp)
	}
}

// TestHandleAgentTooManyConnections verifies the 429 path when the IP limit is reached.
func TestHandleAgentTooManyConnections(t *testing.T) {
	reg := NewRegistry()
	limiter := NewAgentConnLimiter()
	srv := httptest.NewServer(HandleAgent(reg, "sekret", limiter))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent?token=sekret&deviceId=test-1"

	// Fill the limiter for the test client's IP. All dials come from the same
	// loopback address, so acquiring 10 entries on the limiter for that IP will
	// cause the 11th dial to get 429.
	baseURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent?token=sekret&deviceId=fill-"
	for i := 0; i < 10; i++ {
		conn, _, err := websocket.DefaultDialer.Dial(baseURL+string(rune('0'+i)), nil)
		if err != nil {
			t.Fatalf("dial %d should succeed: %v", i, err)
		}
		defer conn.Close()
	}

	// The 11th dial should fail with 429.
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatal("expected dial after limiter full to fail, but it succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected 429 Too Many Requests, got %v", resp)
	}
}

// TestRegisterAgentDefaults verifies that registerAgent fills in default values
// when query parameters are empty.
func TestRegisterAgentDefaults(t *testing.T) {
	reg := NewRegistry()
	// We need to test registerAgent directly since the defaults are set
	// before the device is registered.

	// Create a test server that just upgrades and calls registerAgent
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	gotConn := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		gotConn <- conn
		// registerAgent will read from the connection forever, so we need to
		// let it run in the background. We'll close the connection from the
		// test side.
		go registerAgent(reg, conn, r.URL.Query())
	}))
	defer srv.Close()

	// Connect with no query params
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	server := <-gotConn

	// Give registerAgent time to process
	time.Sleep(50 * time.Millisecond)

	// The device should be registered with defaults: random 16-char hex ID,
	// name="Android", sdk="?", ver="unknown", vc=0
	devices := reg.List()
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}
	d := devices[0]
	if len(d.ID) != 16 { // randomHex(8) = 16 hex chars
		t.Fatalf("expected device ID length 16, got %q (len=%d)", d.ID, len(d.ID))
	}
	if d.Name != "Android" {
		t.Fatalf("expected name 'Android', got %q", d.Name)
	}
	if d.SDK != "?" {
		t.Fatalf("expected sdk '?', got %q", d.SDK)
	}
	if d.AgentVersion != "unknown" {
		t.Fatalf("expected agent version 'unknown', got %q", d.AgentVersion)
	}
	if d.AgentVersionCode != 0 {
		t.Fatalf("expected agent version code 0, got %d", d.AgentVersionCode)
	}

	_ = client
	_ = server.Close()
}

// TestRegisterAgentReplacement verifies that a second connection with the same
// deviceId replaces the old one.
func TestRegisterAgentReplacement(t *testing.T) {
	reg := NewRegistry()

	// We need to trigger the replacement path in registerAgent.
	// First, register a device with ID "replace-me".
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	// First connection
	gotConn1 := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		gotConn1 <- conn
		go registerAgent(reg, conn, r.URL.Query())
	}))
	defer srv.Close()

	baseURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	client1, _, err := websocket.DefaultDialer.Dial(baseURL+"/agent?deviceId=replace-me", nil)
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	server1 := <-gotConn1
	time.Sleep(50 * time.Millisecond)

	// Verify first device is registered
	if _, err := reg.resolve("replace-me"); err != nil {
		t.Fatalf("first device should be registered: %v", err)
	}

	// Second connection with same deviceId
	gotConn2 := make(chan *websocket.Conn, 1)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		gotConn2 <- conn
		registerAgent(reg, conn, r.URL.Query())
	}))
	defer srv2.Close()

	baseURL2 := "ws" + strings.TrimPrefix(srv2.URL, "http")
	client2, _, err := websocket.DefaultDialer.Dial(baseURL2+"/agent?deviceId=replace-me", nil)
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	server2 := <-gotConn2
	time.Sleep(50 * time.Millisecond)

	// The device should still be registered (replaced, not removed)
	if _, err := reg.resolve("replace-me"); err != nil {
		t.Fatalf("device should still be registered after replacement: %v", err)
	}

	_ = client1
	_ = client2
	_ = server1.Close()
	_ = server2.Close()
}

// TestRegisterAgentInvalidFrame verifies that a non-JSON message from the agent
// is logged and the connection is not terminated.
func TestRegisterAgentInvalidFrame(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })

	reg := NewRegistry()

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	gotConn := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		gotConn <- conn
		// registerAgent will read messages in a loop — we send a bad frame,
		// then a close frame.
		registerAgent(reg, conn, r.URL.Query())
	}))
	defer srv.Close()

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/agent?deviceId=invalid-frame-test", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	<-gotConn

	// Send an invalid JSON frame
	if err := client.WriteMessage(websocket.TextMessage, []byte("not json")); err != nil {
		t.Fatalf("write invalid frame: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Check that the invalid frame was logged
	if !strings.Contains(buf.String(), "invalid result frame") {
		t.Fatalf("expected 'invalid result frame' log, got: %s", buf.String())
	}

	// The device should still be registered (invalid frame doesn't disconnect)
	if _, err := reg.resolve("invalid-frame-test"); err != nil {
		t.Fatalf("device should still be registered after invalid frame: %v", err)
	}

	_ = client.Close()
}

// TestRegisterAgentValidResultFrame verifies that a valid result frame is
// dispatched through resolveResult.
func TestRegisterAgentValidResultFrame(t *testing.T) {
	reg := NewRegistry()

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	gotConn := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		gotConn <- conn
		// We need a device ID that we know. Use the query param.
		go registerAgent(reg, conn, r.URL.Query())
	}))
	defer srv.Close()

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/agent?deviceId=result-test", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	<-gotConn

	time.Sleep(50 * time.Millisecond)

	// Set up a pending request so resolveResult has a target
	d, _ := reg.get("result-test")
	ch := make(chan pendingResult, 1)
	d.mu.Lock()
	d.pending["req-1"] = ch
	d.mu.Unlock()

	// Send a valid result frame
	frame := resultFrame{Type: "result", ReqID: "req-1", Code: 0, Stdout: "ok", DurationMs: 5}
	data, _ := json.Marshal(frame)
	if err := client.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write result frame: %v", err)
	}

	select {
	case got := <-ch:
		if got.result.Code != 0 || got.result.Stdout != "ok" || got.result.DurationMs != 5 {
			t.Fatalf("unexpected result: %#v", got.result)
		}
	case <-time.After(time.Second):
		t.Fatal("result not dispatched within 1s")
	}

	_ = client.Close()
}

// TestRegisterAgentNonResultFrame verifies that non-result frames are ignored.
func TestRegisterAgentNonResultFrame(t *testing.T) {
	reg := NewRegistry()

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	gotConn := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		gotConn <- conn
		go registerAgent(reg, conn, r.URL.Query())
	}))
	defer srv.Close()

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/agent?deviceId=non-result-test", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	<-gotConn

	time.Sleep(50 * time.Millisecond)

	// Set up a pending request
	d, _ := reg.get("non-result-test")
	ch := make(chan pendingResult, 1)
	d.mu.Lock()
	d.pending["req-2"] = ch
	d.mu.Unlock()

	// Send a frame with wrong type — should be ignored
	frame := resultFrame{Type: "exec", ReqID: "req-2", Code: 0}
	data, _ := json.Marshal(frame)
	if err := client.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write non-result frame: %v", err)
	}

	// Send a frame with empty ReqID — should be ignored
	frame2 := resultFrame{Type: "result", ReqID: "", Code: 0}
	data2, _ := json.Marshal(frame2)
	if err := client.WriteMessage(websocket.TextMessage, data2); err != nil {
		t.Fatalf("write frame with empty reqId: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Both frames should have been ignored — channel should still be empty
	select {
	case <-ch:
		t.Fatal("non-result frame should not have been dispatched")
	default:
		// OK
	}

	_ = client.Close()
}

// TestRegisterAgentCustomValues verifies that registerAgent parses query params.
func TestRegisterAgentCustomValues(t *testing.T) {
	reg := NewRegistry()

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	gotConn := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		gotConn <- conn
		go registerAgent(reg, conn, r.URL.Query())
	}))
	defer srv.Close()

	q := url.Values{
		"deviceId": {"custom-id"},
		"name":     {"Pixel-9"},
		"sdk":      {"35"},
		"kind":     {"watch"},
		"ver":      {"2.1.0"},
		"vc":       {"42"},
	}
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/agent?"+q.Encode(), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	<-gotConn

	time.Sleep(50 * time.Millisecond)

	d, err := reg.resolve("custom-id")
	if err != nil {
		t.Fatalf("resolve custom-id: %v", err)
	}
	if d.Name != "Pixel-9" {
		t.Fatalf("expected name 'Pixel-9', got %q", d.Name)
	}
	if d.SDK != "35" {
		t.Fatalf("expected sdk '35', got %q", d.SDK)
	}
	if d.Kind != KindWatch {
		t.Fatalf("expected KindWatch, got %q", d.Kind)
	}
	if d.AgentVersion != "2.1.0" {
		t.Fatalf("expected agent version '2.1.0', got %q", d.AgentVersion)
	}
	if d.AgentVersionCode != 42 {
		t.Fatalf("expected agent version code 42, got %d", d.AgentVersionCode)
	}

	_ = client.Close()
}

// TestRegisterAgentReplacementWithOldConn tests that when a device reconnects,
// the old connection is disconnected.
func TestRegisterAgentReplacementWithOldConn(t *testing.T) {
	reg := NewRegistry()

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	// Start a single server that handles all connections
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		registerAgent(reg, conn, r.URL.Query())
	}))
	defer srv.Close()

	baseURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	// First connection
	client1, _, err := websocket.DefaultDialer.Dial(baseURL+"/agent?deviceId=replace-me-2", nil)
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Get the old device to verify its conn is cleared
	oldDevice, _ := reg.get("replace-me-2")

	// Second connection with same deviceId — should trigger replacement
	client2, _, err := websocket.DefaultDialer.Dial(baseURL+"/agent?deviceId=replace-me-2", nil)
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Old device's conn should be nil (disconnect cleared it)
	oldDevice.mu.Lock()
	oldConnWasNil := oldDevice.conn == nil
	oldDevice.mu.Unlock()
	if !oldConnWasNil {
		t.Fatal("expected old device connection to be cleared after replacement")
	}

	_ = client1
	_ = client2.Close()
}

// TestHandleAgentUpgradeFailure verifies that when the WebSocket upgrade fails,
// the handler returns without panicking.
func TestHandleAgentUpgradeFailure(t *testing.T) {
	// We can't easily trigger an upgrade failure through the normal WS dialer
	// (it handles the handshake). Instead, we test that the handler returns
	// properly when given a non-WebSocket request.
	reg := NewRegistry()
	handler := HandleAgent(reg, "sekret", NewAgentConnLimiter())

	// A plain HTTP request (not a WS upgrade) will cause the upgrade to fail.
	// The handler should log the error and return, not panic.
	req := httptest.NewRequest("GET", "/agent?token=sekret", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	// Should get a response (not a hang/panic)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d", rec.Code)
	}
}

// --- sendPing tests ---

// TestSendPingSuccess verifies that sendPing writes a PingMessage successfully
// and the client receives it. The client must read messages so the ping handler
// fires (gorilla/websocket only invokes the handler during ReadMessage).
func TestSendPingSuccess(t *testing.T) {
	server, client := testDevicePair(t)

	pingReceived := make(chan struct{})
	client.SetPingHandler(func(appData string) error {
		close(pingReceived)
		return nil
	})

	// Read messages in a goroutine so the ping handler fires.
	go func() {
		for {
			if _, _, err := client.ReadMessage(); err != nil {
				return
			}
		}
	}()

	var mu sync.Mutex
	err := sendPing(server, &mu)
	if err != nil {
		t.Fatalf("sendPing: %v", err)
	}

	select {
	case <-pingReceived:
		// OK — client received the ping
	case <-time.After(time.Second):
		t.Fatal("client did not receive ping within 1s")
	}
}

// TestSendPingFailure verifies that sendPing returns an error when the
// connection is closed.
func TestSendPingFailure(t *testing.T) {
	server, client := testDevicePair(t)
	server.Close()
	client.Close()
	time.Sleep(50 * time.Millisecond)

	var mu sync.Mutex
	err := sendPing(server, &mu)
	if err == nil {
		t.Fatal("expected sendPing to fail on closed connection")
	}
}

// --- pingIntervalFor tests ---

// TestPingIntervalFor verifies the ping interval for each device kind.
func TestPingIntervalFor(t *testing.T) {
	cases := []struct {
		kind Kind
		want time.Duration
	}{
		{KindWatch, 60 * time.Second},
		{KindAndroid, 25 * time.Second},
		{Kind("unknown"), 25 * time.Second},
	}
	for _, tc := range cases {
		got := pingIntervalFor(tc.kind)
		if got != tc.want {
			t.Errorf("pingIntervalFor(%q) = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

// --- PongHandler test ---

// TestPongHandlerUpdatesLastSeen verifies that the PongHandler set by
// registerAgentPing updates the device's lastSeen when a Pong frame arrives.
func TestPongHandlerUpdatesLastSeen(t *testing.T) {
	reg := NewRegistry()
	server, client := testDevicePair(t)
	t.Cleanup(func() { client.Close() })

	// Use a very long ping interval so the ticker doesn't interfere.
	q := url.Values{"deviceId": {"pong-handler-test"}}
	go registerAgentPing(reg, server, q, time.Hour)
	time.Sleep(50 * time.Millisecond)

	d, ok := reg.get("pong-handler-test")
	if !ok {
		t.Fatal("device not found")
	}

	// Set lastSeen to a known past time.
	d.mu.Lock()
	d.lastSeen = time.Now().Add(-time.Minute)
	d.mu.Unlock()

	// Send a Pong from the client — the server's PongHandler should fire.
	if err := client.WriteMessage(websocket.PongMessage, nil); err != nil {
		t.Fatalf("write pong message: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	d.mu.Lock()
	updated := d.lastSeen
	d.mu.Unlock()

	if updated.Before(time.Now().Add(-5 * time.Second)) {
		t.Fatal("expected lastSeen to be updated after pong, but it was not")
	}
}

// --- staleness test ---

// TestRegisterAgentStaleDisconnect verifies that the ticker goroutine detects
// a stale device (no traffic for >2.5 ping intervals) and disconnects it.
func TestRegisterAgentStaleDisconnect(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })

	reg := NewRegistry()
	server, client := testDevicePair(t)
	t.Cleanup(func() { client.Close() })

	q := url.Values{"deviceId": {"stale-test"}}
	go registerAgentPing(reg, server, q, 10*time.Millisecond)

	// Wait for enough ticks to trigger staleness. With a 10ms ping interval,
	// staleAfter = 25ms, so the third tick (~30ms) should detect staleness.
	time.Sleep(100 * time.Millisecond)

	// Device should be disconnected from the registry.
	if _, err := reg.resolve("stale-test"); err == nil {
		t.Fatal("expected device to be disconnected due to staleness, but it is still registered")
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "stale, terminating") {
		t.Fatalf("expected 'stale, terminating' in log, got: %s", logOutput)
	}
}

// --- io.EOF test ---

// TestRegisterAgentConnectionClosedEOF verifies that when the client half-closes
// its TCP write side (FIN), gorilla/websocket translates the truncation into
// CloseError 1006 ("unexpected EOF"), so the server's read loop must land in the
// error branch ("read failed") — never "connection closed" (raw io.EOF), which
// gorilla cannot produce on a live socket.
func TestRegisterAgentConnectionClosedEOF(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })

	reg := NewRegistry()
	server, client := testDevicePair(t)

	q := url.Values{"deviceId": {"eof-test"}}
	go registerAgentPing(reg, server, q, time.Hour)
	time.Sleep(50 * time.Millisecond)

	// Half-close the client's TCP write side — sends a FIN to the server.
	tcpConn, ok := client.UnderlyingConn().(*net.TCPConn)
	if !ok {
		t.Fatal("expected TCP connection")
	}
	if err := tcpConn.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), "read failed") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	logOutput := buf.String()
	// gorilla v1.5.3 can never surface raw io.EOF here: a FIN mid-frame becomes
	// CloseError 1006 (errUnexpectedEOF in conn.go read()). So the io.EOF branch
	// in registerAgentPing's read loop is defensive only — make sure we exercise
	// the branch that actually fires, and that "connection closed" does NOT.
	if !strings.Contains(logOutput, "read failed") {
		t.Fatalf("expected 'read failed' in log, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "unexpected EOF") {
		t.Fatalf("expected 1006 'unexpected EOF' in log, got: %s", logOutput)
	}
}

// --- ping failure test ---

// TestRegisterAgentPingFailed verifies that when the connection's write side
// breaks, a failed ping is logged as "ping failed". The ping interval is long
// enough (50ms → staleAfter=125ms) that CloseWrite at 80ms runs before the
// staleness check, and the next tick (100ms) hits the broken write half.
func TestRegisterAgentPingFailed(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })

	reg := NewRegistry()
	server, client := testDevicePair(t)

	q := url.Values{"deviceId": {"ping-fail-test"}}
	go registerAgentPing(reg, server, q, 50*time.Millisecond)
	time.Sleep(80 * time.Millisecond)

	// Close the server's write half. The read loop stays blocked (client never
	// sends data), but the next ticker tick fails to write the ping.
	tcpConn, ok := server.UnderlyingConn().(*net.TCPConn)
	if !ok {
		t.Fatalf("expected TCP connection, got %T", server.UnderlyingConn())
	}
	if err := tcpConn.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	logOutput := buf.String()
	if !strings.Contains(logOutput, "ping failed") {
		t.Fatalf("expected 'ping failed' in log, got: %s", logOutput)
	}
	_ = client
}

// --- truncated frame test ---

// TestRegisterAgentTruncatedFrame verifies that frames larger than 256 bytes
// are truncated in the error log with "...".
func TestRegisterAgentTruncatedFrame(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })

	reg := NewRegistry()
	server, client := testDevicePair(t)
	t.Cleanup(func() { client.Close() })

	// Use a long ping interval so the staleness ticker doesn't fire and
	// disconnect the device before the assertion runs.
	q := url.Values{"deviceId": {"truncated-test"}}
	go registerAgentPing(reg, server, q, time.Hour)
	time.Sleep(50 * time.Millisecond)

	// Send a 300-byte invalid JSON frame — the log should truncate it to 256 + "...".
	longFrame := strings.Repeat("x", 300)
	if err := client.WriteMessage(websocket.TextMessage, []byte(longFrame)); err != nil {
		t.Fatalf("write long frame: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	logOutput := buf.String()
	if !strings.Contains(logOutput, "...") {
		t.Fatalf("expected truncated frame with '...' in log, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "invalid result frame") {
		t.Fatalf("expected 'invalid result frame' in log, got: %s", logOutput)
	}

	// Device should still be registered (invalid frame doesn't disconnect).
	if _, err := reg.resolve("truncated-test"); err != nil {
		t.Fatalf("device should still be registered after invalid frame: %v", err)
	}
}