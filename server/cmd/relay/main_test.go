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

func TestBuildMCPServerVersion(t *testing.T) {
	s := buildMCPServer(relay.NewRegistry(), 5*time.Second, 30*time.Second)
	if s.Name != "rish-mcp" || s.Version != "1.0.0" {
		t.Fatalf("server identity = %s@%s, want rish-mcp@1.0.0", s.Name, s.Version)
	}
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
		"/agent?token=device-token&deviceId=dev1&name=Pixel&sdk=34&kind=android&ver=1.0.0&vc=10000&backend=shizuku"
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
	if !strings.Contains(listResp, `\"shellBackend\": \"shizuku\"`) {
		t.Fatalf("list_devices response missing shell backend: %s", listResp)
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
//
// The fields the test wants back (code, stdout, stderr, truncated) can be
// smuggled in on the exec frame itself; absent fields keep the original
// defaults so legacy round-trip expectations still hold.
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
		code, _ := req["code"].(float64)
		stdout := "SM-TEST\n"
		if v, ok := req["stdout"].(string); ok && v != "" {
			stdout = v
		}
		stderr := ""
		if v, ok := req["stderr"].(string); ok {
			stderr = v
		}
		truncated := false
		if v, ok := req["truncated"].(bool); ok {
			truncated = v
		}
		resp := map[string]any{
			"type": "result", "reqId": req["reqId"], "code": code,
			"stdout": stdout, "stderr": stderr, "truncated": truncated, "durationMs": 5,
		}
		b, _ := json.Marshal(resp)
		_ = conn.WriteMessage(websocket.TextMessage, b)
	}
}

// newTestServer wires up a relay mux without any device and hands back the
// registry (for dialing fake agents) plus the running server.
func newTestServer(t *testing.T) (*relay.Registry, *httptest.Server) {
	t.Helper()
	reg := relay.NewRegistry()
	mux := newMux(reg, testOAuthProvider(), "ai-token", "device-token", 5*time.Second, 30*time.Second)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return reg, srv
}

// rawMCPRequest sends a bare HTTP request to the relay with a valid AI bearer
// token; bodies are sent verbatim (possibly malformed on purpose).
func rawMCPRequest(t *testing.T, base, method, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, base+path, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer ai-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// beginAgent dials /agent with the given device ID and starts answering exec
// frames, returning the connection.
func beginAgent(t *testing.T, base, deviceID string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(base, "http") +
		"/agent?token=device-token&deviceId=" + deviceID + "&name=Pixel&sdk=34&kind=android&ver=1.0.0&vc=10000"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	go fakeAgent(conn)
	return conn
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

// --- envDurationMs tests ----------------------------------------------------

func TestEnvDurationMs(t *testing.T) {
	t.Setenv("EDM_KEY", "5000")
	if d := envDurationMs("EDM_KEY", 1000); d != 5*time.Second {
		t.Fatalf("expected 5s, got %v", d)
	}
	t.Setenv("EDM_KEY", "not-a-number")
	if d := envDurationMs("EDM_KEY", 1000); d != 1*time.Second {
		t.Fatalf("expected 1s fallback on invalid, got %v", d)
	}
	os.Unsetenv("EDM_KEY")
	if d := envDurationMs("EDM_KEY", 1000); d != 1*time.Second {
		t.Fatalf("expected 1s fallback on unset, got %v", d)
	}
}

// --- MCP handler edge cases ------------------------------------------------

func TestMCPMethodNotAllowed(t *testing.T) {
	_, srv := newTestServer(t)
	resp := rawMCPRequest(t, srv.URL, http.MethodGet, "/mcp", "")
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET /mcp, got %d", resp.StatusCode)
	}
}

func TestMCPParseError(t *testing.T) {
	_, srv := newTestServer(t)
	resp := rawMCPRequest(t, srv.URL, http.MethodPost, "/mcp", `{invalid json}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed JSON, got %d", resp.StatusCode)
	}
}

func TestMCPNotification(t *testing.T) {
	_, srv := newTestServer(t)
	body := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	resp := rawMCPRequest(t, srv.URL, http.MethodPost, "/mcp", body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 for notification, got %d", resp.StatusCode)
	}
}

// --- run_shell error branches ----------------------------------------------

func TestRunShellRejectsEmptyCmd(t *testing.T) {
	_, srv := newTestServer(t)
	beginAgent(t, srv.URL, "dev1")
	waitForDevices(t, srv.URL, 1)
	resp := callTool(t, srv.URL, "ai-token", "run_shell", map[string]any{})
	if !strings.Contains(resp, "cmd is required") {
		t.Fatalf("expected cmd-required error, got: %s", resp)
	}
}

func TestRunShellRejectsInvalidJSON(t *testing.T) {
	_, srv := newTestServer(t)
	beginAgent(t, srv.URL, "dev1")
	waitForDevices(t, srv.URL, 1)
	resp := callTool(t, srv.URL, "ai-token", "run_shell", map[string]any{"cmd": 123})
	if !strings.Contains(resp, "cannot unmarshal") {
		t.Fatalf("expected unmarshal error, got: %s", resp)
	}
}

// --- custom fakeAgent helpers for result-branch coverage -------------------

// fakeAgentWithResult is like fakeAgent but returns a fixed relay.Result
// for every exec frame, allowing tests to cover truncated/stderr/exit-code
// branches that fakeAgent's defaults can't reach.
func fakeAgentWithResult(conn *websocket.Conn, res relay.Result) {
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
			"type": "result", "reqId": req["reqId"], "code": res.Code,
			"stdout": res.Stdout, "stderr": res.Stderr,
			"truncated": res.Truncated, "durationMs": res.DurationMs,
		}
		b, _ := json.Marshal(resp)
		_ = conn.WriteMessage(websocket.TextMessage, b)
	}
}

// beginAgentWithConfig is like beginAgent but uses a caller-supplied
// relay.Result so the fake agent returns specific truncated/stderr/code values.
func beginAgentWithConfig(t *testing.T, base, deviceID string, res relay.Result) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(base, "http") +
		"/agent?token=device-token&deviceId=" + deviceID + "&name=Pixel&sdk=34&kind=android&ver=1.0.0&vc=10000"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	go fakeAgentWithResult(conn, res)
	return conn
}

// --- run_shell result-branch tests ----------------------------------------

// TestRunShellTimeoutClamp exercises the timeout > maxTimeout clamp in
// runShellHandler: sending a timeoutMs that exceeds the 30s max must
// silently cap to maxTimeout rather than erroring.
func TestRunShellTimeoutClamp(t *testing.T) {
	_, srv := newTestServer(t)
	beginAgent(t, srv.URL, "dev1")
	waitForDevices(t, srv.URL, 1)
	resp := callTool(t, srv.URL, "ai-token", "run_shell", map[string]any{"cmd": "echo hi", "timeoutMs": 999999999})
	if !strings.Contains(resp, "exit=0") {
		t.Fatalf("expected successful response, got: %s", resp)
	}
}

// TestRunShellTruncatedOutput covers the res.Truncated branch that appends
// "[output truncated]" to the response body.
func TestRunShellTruncatedOutput(t *testing.T) {
	_, srv := newTestServer(t)
	beginAgentWithConfig(t, srv.URL, "dev1", relay.Result{Code: 0, Stdout: "partial output", Truncated: true, DurationMs: 5})
	waitForDevices(t, srv.URL, 1)
	resp := callTool(t, srv.URL, "ai-token", "run_shell", map[string]any{"cmd": "echo hi"})
	if !strings.Contains(resp, "output truncated") {
		t.Fatalf("expected truncated marker, got: %s", resp)
	}
}

// TestRunShellStderr covers the res.Stderr != "" branch that appends a
// stderr section to the response body.
func TestRunShellStderr(t *testing.T) {
	_, srv := newTestServer(t)
	beginAgentWithConfig(t, srv.URL, "dev1", relay.Result{Code: 0, Stdout: "ok", Stderr: "warning: something", DurationMs: 5})
	waitForDevices(t, srv.URL, 1)
	resp := callTool(t, srv.URL, "ai-token", "run_shell", map[string]any{"cmd": "echo hi"})
	if !strings.Contains(resp, "warning: something") {
		t.Fatalf("expected stderr in response, got: %s", resp)
	}
}

// TestRunShellNonZeroExit covers the res.Code != 0 branch that sets
// IsError: true on the CallResult.
func TestRunShellNonZeroExit(t *testing.T) {
	_, srv := newTestServer(t)
	beginAgentWithConfig(t, srv.URL, "dev1", relay.Result{Code: 1, Stdout: "", Stderr: "command failed", DurationMs: 5})
	waitForDevices(t, srv.URL, 1)
	resp := callTool(t, srv.URL, "ai-token", "run_shell", map[string]any{"cmd": "false"})
	if !strings.Contains(resp, "exit=1") {
		t.Fatalf("expected exit=1, got: %s", resp)
	}
	if !strings.Contains(resp, "isError") {
		t.Fatalf("expected isError in response, got: %s", resp)
	}
}
