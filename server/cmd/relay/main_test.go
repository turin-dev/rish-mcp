package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/turin-dev/rish-mcp/server/internal/relay"
)

// TestRunShellRoundTrip drives the whole skeleton end to end, mirroring
// before/server/test/smoke.mjs: a fake Android agent dials /agent, then an
// MCP client lists devices and runs a command through /mcp.
func TestRunShellRoundTrip(t *testing.T) {
	reg := relay.NewRegistry()
	mux := newMux(reg, "ai-token", "device-token", 5*time.Second, 30*time.Second)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") +
		"/agent?token=device-token&deviceId=dev1&name=Pixel&sdk=34&kind=android&ver=0.1.0&vc=1"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	defer conn.Close()

	go fakeAgent(conn)

	waitForDevices(t, srv.URL, 1)

	listResp := callTool(t, srv.URL, "ai-token", "list_devices", map[string]any{})
	if !strings.Contains(listResp, "dev1") {
		t.Fatalf("list_devices response missing dev1: %s", listResp)
	}

	shellResp := callTool(t, srv.URL, "ai-token", "run_shell", map[string]any{"cmd": "getprop ro.product.model"})
	if !strings.Contains(shellResp, "SM-TEST") {
		t.Fatalf("run_shell response missing expected stdout: %s", shellResp)
	}
	if !strings.Contains(shellResp, "exit=0") {
		t.Fatalf("run_shell response missing exit=0: %s", shellResp)
	}
}

func TestMCPRequiresBearer(t *testing.T) {
	reg := relay.NewRegistry()
	mux := newMux(reg, "ai-token", "device-token", 5*time.Second, 30*time.Second)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", bytes.NewReader([]byte(`{}`)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post /mcp: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without a bearer token, got %d", resp.StatusCode)
	}
}

func TestRunShellNoDeviceConnected(t *testing.T) {
	reg := relay.NewRegistry()
	mux := newMux(reg, "ai-token", "device-token", 5*time.Second, 30*time.Second)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp := callTool(t, srv.URL, "ai-token", "run_shell", map[string]any{"cmd": "id"})
	if !strings.Contains(resp, "no device is connected") {
		t.Fatalf("expected no-device error, got: %s", resp)
	}
}

// fakeAgent answers every "exec" frame with a canned successful result,
// standing in for the Android app during this skeleton stage.
func fakeAgent(conn *websocket.Conn) {
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req map[string]any
		if err := json.Unmarshal(raw, &req); err != nil {
			continue
		}
		if req["type"] != "exec" {
			continue
		}
		resp := map[string]any{
			"type": "result", "reqId": req["reqId"], "code": 0,
			"stdout": "SM-TEST\n", "stderr": "", "truncated": false, "durationMs": 5,
		}
		b, _ := json.Marshal(resp)
		_ = conn.WriteMessage(websocket.TextMessage, b)
	}
}

func waitForDevices(t *testing.T, base string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			var body struct {
				Devices int `json:"devices"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			if body.Devices >= want {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("device never registered within deadline")
}

func callTool(t *testing.T, base, aiToken, name string, args map[string]any) string {
	t.Helper()
	argsJSON, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": json.RawMessage(argsJSON)},
	}
	b, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, base+"/mcp", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+aiToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post /mcp: %v", err)
	}
	defer resp.Body.Close()
	var out bytes.Buffer
	_, _ = out.ReadFrom(resp.Body)
	return out.String()
}
