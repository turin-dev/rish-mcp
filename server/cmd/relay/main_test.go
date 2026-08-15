package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/turin-dev/rish-mcp/server/internal/oauth"
	"github.com/turin-dev/rish-mcp/server/internal/relay"
)

func testOAuthProvider() *oauth.Provider {
	return oauth.NewProvider(oauth.Config{PublicURL: "http://test.invalid", AIToken: "ai-token"})
}

// TestRunShellRoundTrip drives the whole skeleton end to end, mirroring
// before/server/test/smoke.mjs: a fake Android agent dials /agent, then an
// MCP client lists devices and runs a command through /mcp.
func TestRunShellRoundTrip(t *testing.T) {
	reg := relay.NewRegistry()
	mux := newMux(reg, testOAuthProvider(), "ai-token", "device-token", 5*time.Second, 30*time.Second)
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
	mux := newMux(reg, testOAuthProvider(), "ai-token", "device-token", 5*time.Second, 30*time.Second)
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
	mux := newMux(reg, testOAuthProvider(), "ai-token", "device-token", 5*time.Second, 30*time.Second)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp := callTool(t, srv.URL, "ai-token", "run_shell", map[string]any{"cmd": "id"})
	if !strings.Contains(resp, "no device is connected") {
		t.Fatalf("expected no-device error, got: %s", resp)
	}
}

func TestRunShellRejectsOversizedCmd(t *testing.T) {
	reg := relay.NewRegistry()
	mux := newMux(reg, testOAuthProvider(), "ai-token", "device-token", 5*time.Second, 30*time.Second)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	big := strings.Repeat("x", maxCmdLen+1)
	resp := callTool(t, srv.URL, "ai-token", "run_shell", map[string]any{"cmd": big})
	if !strings.Contains(resp, "cmd is too long") {
		t.Fatalf("oversized cmd accepted: %s", resp)
	}
}

// TestMCPRejectsOversizedBody verifies the MaxBytesReader gate on /mcp: a body
// larger than maxMCPBodyBytes must fail (413) instead of being read into
// memory. http.MaxBytesReader itself writes the 413 once the limit is
// exceeded, before the JSON decoder even gets to report the parse error.
func TestMCPRejectsOversizedBody(t *testing.T) {
	reg := relay.NewRegistry()
	mux := newMux(reg, testOAuthProvider(), "ai-token", "device-token", 5*time.Second, 30*time.Second)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	pad := strings.Repeat(" ", maxMCPBodyBytes)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", bytes.NewReader([]byte(`{"pad":"`+pad+`"}`)))
	req.Header.Set("Authorization", "Bearer ai-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post /mcp oversized body: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized body, got %d", resp.StatusCode)
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

// --- loadConfigFromEnv -------------------------------------------------
// Set up with a helper that clears the relay env so tests are hermetic.

func withRelayEnv(t *testing.T, env map[string]string, fn func()) {
	t.Helper()
	saved := map[string]string{}
	for _, k := range []string{
		"PORT", "AI_TOKEN", "DEVICE_TOKEN", "DEFAULT_TIMEOUT_MS",
		"MAX_TIMEOUT_MS", "PUBLIC_URL", "TRUSTED_PROXIES",
	} {
		saved[k] = os.Getenv(k)
		os.Unsetenv(k)
	}
	for k, v := range env {
		os.Setenv(k, v)
	}
	defer func() {
		for k, v := range saved {
			if v == "" {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, v)
			}
		}
	}()
	fn()
}

func TestLoadConfigValid(t *testing.T) {
	withRelayEnv(t, map[string]string{
		"AI_TOKEN":     "ai",
		"DEVICE_TOKEN": "device",
		"PUBLIC_URL":   "https://mcp.example.com",
	}, func() {
		cfg, err := loadConfigFromEnv()
		if err != nil {
			t.Fatalf("valid config rejected: %v", err)
		}
		if cfg.port != "8080" || cfg.aiToken != "ai" || cfg.deviceToken != "device" {
			t.Fatalf("unexpected config: %+v", cfg)
		}
		if cfg.publicURL != "https://mcp.example.com" {
			t.Fatalf("publicURL = %q", cfg.publicURL)
		}
		if cfg.defaultTimeout != 60*time.Second || cfg.maxTimeout != 600*time.Second {
			t.Fatalf("unexpected timeouts: %+v", cfg)
		}
	})
}

func TestLoadConfigRejectsInvalidPort(t *testing.T) {
	for _, port := range []string{"0", "70000", "abc", "-1"} {
		withRelayEnv(t, map[string]string{"PORT": port, "AI_TOKEN": "a", "DEVICE_TOKEN": "d"}, func() {
			if _, err := loadConfigFromEnv(); err == nil {
				t.Fatalf("PORT=%s accepted", port)
			}
		})
	}
}

func TestLoadConfigRequiresTokens(t *testing.T) {
	withRelayEnv(t, map[string]string{"DEVICE_TOKEN": "d"}, func() {
		if _, err := loadConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "AI_TOKEN") {
			t.Fatalf("missing AI_TOKEN: got %v", err)
		}
	})
	withRelayEnv(t, map[string]string{"AI_TOKEN": "a"}, func() {
		if _, err := loadConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "DEVICE_TOKEN") {
			t.Fatalf("missing DEVICE_TOKEN: got %v", err)
		}
	})
}

func TestLoadConfigRejectsInvalidTimeouts(t *testing.T) {
	for _, tc := range []struct{ def, max, want string }{
		{"0", "600000", "DEFAULT_TIMEOUT_MS"},
		{"60001", "60000", "must not exceed"},
		{"-5", "600000", "DEFAULT_TIMEOUT_MS"},
		{"60000", "not-a-number", "MAX_TIMEOUT_MS"},
	} {
		withRelayEnv(t, map[string]string{
			"AI_TOKEN": "a", "DEVICE_TOKEN": "d",
			"DEFAULT_TIMEOUT_MS": tc.def, "MAX_TIMEOUT_MS": tc.max,
		}, func() {
			_, err := loadConfigFromEnv()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("timeouts %s/%s: got %v, want %q", tc.def, tc.max, err, tc.want)
			}
		})
	}
}

func TestLoadConfigRejectsInvalidPublicURL(t *testing.T) {
	for _, u := range []string{"localhost:8080", "://nope", "ftp://x"} {
		withRelayEnv(t, map[string]string{"AI_TOKEN": "a", "DEVICE_TOKEN": "d", "PUBLIC_URL": u}, func() {
			if _, err := loadConfigFromEnv(); err == nil {
				t.Fatalf("PUBLIC_URL=%q accepted", u)
			}
		})
	}
}

func TestLoadConfigRejectsInvalidTrustedProxies(t *testing.T) {
	for _, list := range []string{"10.0.0.1,not-an-ip", "10.0.0.1,,", "::zz"} {
		withRelayEnv(t, map[string]string{
			"AI_TOKEN": "a", "DEVICE_TOKEN": "d", "TRUSTED_PROXIES": list,
		}, func() {
			if _, err := loadConfigFromEnv(); err == nil {
				t.Fatalf("TRUSTED_PROXIES=%q accepted", list)
			}
		})
	}
}

func TestLoadConfigAcceptsTrustedProxiesWithSpaces(t *testing.T) {
	withRelayEnv(t, map[string]string{
		"AI_TOKEN": "a", "DEVICE_TOKEN": "d",
		"TRUSTED_PROXIES": "10.0.0.1, ::1, 192.168.1.5",
	}, func() {
		cfg, err := loadConfigFromEnv()
		if err != nil {
			t.Fatalf("valid TRUSTED_PROXIES rejected: %v", err)
		}
		if cfg.trustedProxies != "10.0.0.1, ::1, 192.168.1.5" {
			t.Fatalf("unexpected trustedProxies: %q", cfg.trustedProxies)
		}
	})
}
