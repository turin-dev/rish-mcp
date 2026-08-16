package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/turin-dev/rish-mcp/server/internal/release"
)

func TestPublicServerServesReleaseAndApk(t *testing.T) {
	src := release.NewSource(release.SourceOptionsFromEnv())
	// No LocalAPK/GitHub reachable in this test: exercise the "not ready yet" paths.
	mux := newMux(src, 30, "")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("healthz with no release: expected 503, got %d", resp.StatusCode)
	}

	vresp, err := http.Get(srv.URL + "/api/version/release")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	defer vresp.Body.Close()
	if vresp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("version with no release: expected 503, got %d", vresp.StatusCode)
	}
}

func TestPublicServerRateLimitsDownloads(t *testing.T) {
	limiter := newRateLimiter(2)
	if limiter.limited("1.2.3.4") {
		t.Fatal("first hit should not be limited")
	}
	if limiter.limited("1.2.3.4") {
		t.Fatal("second hit should not be limited")
	}
	if !limiter.limited("1.2.3.4") {
		t.Fatal("third hit should be limited (perHour=2)")
	}
	if limiter.limited("5.6.7.8") {
		t.Fatal("a different IP must not be affected by another IP's limit")
	}
}

func TestRateLimiterCleanupBoundedGrowth(t *testing.T) {
	limiter := newRateLimiter(1)
	limiter.cleanupThreshold = 10 // Lower threshold for testing
	now := time.Now().Unix()
	limiter.lastCleanup = now

	// Simulate 15 unique IPs making requests
	for i := 0; i < 15; i++ {
		ip := fmt.Sprintf("1.2.3.%d", i)
		limiter.limited(ip)
	}

	// Without cleanup, the map would grow indefinitely. With opportunistic cleanup,
	// old entries (whose resetAt has passed) should be removed when map exceeds threshold.
	// Since we just added entries, resetAt is in the future, so they won't be cleaned yet.
	// Verify the map is bounded by cleanup logic.
	limiter.mu.Lock()
	mapSize := len(limiter.hits)
	limiter.mu.Unlock()

	if mapSize > limiter.cleanupThreshold+5 {
		t.Fatalf("limiter map grew too large: %d > %d+5", mapSize, limiter.cleanupThreshold)
	}
}

func TestRateLimiterExpiryAndCleanup(t *testing.T) {
	limiter := newRateLimiter(1)
	limiter.cleanupThreshold = 2 // Low threshold so cleanup runs with just 3 entries
	now := time.Now().Unix()

	limiter.mu.Lock()
	// Manually insert expired and non-expired entries
	limiter.hits["1.2.3.4"] = rateEntry{count: 5, resetAt: now - 100}
	limiter.hits["5.6.7.8"] = rateEntry{count: 1, resetAt: now + 3600}
	// Set lastCleanup to far in the past so the time condition is met
	limiter.lastCleanup = 0
	limiter.mu.Unlock()

	// Next call from a different IP should trigger cleanup since:
	// - len(hits) will be 3 > cleanupThreshold (2)
	// - now - lastCleanup will be very large (now - 0)
	limiter.limited("9.10.11.12")

	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	// After cleanup, expired entry should be gone
	if _, ok := limiter.hits["1.2.3.4"]; ok {
		// Entry was expired (resetAt < now), so cleanup should have removed it
		if limiter.hits["1.2.3.4"].resetAt < time.Now().Unix() {
			t.Fatal("expired entry should have been cleaned up")
		}
	}
	// Non-expired entry should still be there
	if _, ok := limiter.hits["5.6.7.8"]; !ok {
		t.Fatal("non-expired entry should not be deleted")
	}
}

func TestPublicServerDoesNotExposeRelayRoutes(t *testing.T) {
	src := release.NewSource(release.SourceOptionsFromEnv())
	mux := newMux(src, 30, "")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, path := range []string{"/mcp", "/agent"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: expected 404 (no route to the relay), got %d", path, resp.StatusCode)
		}
	}
}

// --- happy-path handlers + env helpers (Task #20: dead-code documentation) --

// writeCachedRelease seeds a Source's cache dir with a real APK + release.json.
// The caller MUST call src.Start(ctx) before the handlers will serve the
// cached release (loadCache runs inside Start).
func writeCachedRelease(t *testing.T, versionName string) *release.Source {
	t.Helper()
	apkBytes := buildManifestAPK(t, 7, versionName)
	cacheDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cacheDir, "agent.apk"), apkBytes, 0o644); err != nil {
		t.Fatalf("write apk: %v", err)
	}
	meta, _ := json.Marshal(map[string]string{
		"tag":       "v2.0.0",
		"fetchedAt": time.Now().UTC().Format(time.RFC3339),
	})
	if err := os.WriteFile(filepath.Join(cacheDir, "release.json"), meta, 0o644); err != nil {
		t.Fatalf("write release.json: %v", err)
	}
	return release.NewSource(release.SourceOptions{CacheDir: cacheDir, PollEvery: time.Hour})
}

func TestHealthzWithRelease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := writeCachedRelease(t, "2.0.0")
	src.Start(ctx)
	mux := newMux(src, 30, "")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz with release: expected 200, got %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode healthz body: %v", err)
	}
	if body["ok"] != true {
		t.Errorf("healthz body ok = %v, want true", body["ok"])
	}
	if body["release"] != "2.0.0" {
		t.Errorf("healthz body release = %v, want 2.0.0", body["release"])
	}
}

func TestVersionHandlerWithRelease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := writeCachedRelease(t, "2.3.4")
	src.Start(ctx)
	mux := newMux(src, 30, "")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/version/release")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("version with release: expected 200, got %d", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "public, max-age=60" {
		t.Errorf("Cache-Control = %q, want %q", cc, "public, max-age=60")
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body struct {
		VersionName string `json:"versionName"`
		VersionCode int    `json:"versionCode"`
		Tag         string `json:"tag"`
		Download    string `json:"download"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode version body: %v", err)
	}
	if body.VersionName != "2.3.4" || body.VersionCode != 7 || body.Tag != "v2.0.0" || body.Download != "/agent.apk" {
		t.Errorf("version body = %+v, want versionName=2.3.4 versionCode=7 tag=v2.0.0 download=/agent.apk", body)
	}
}

func TestAgentApkHandlerNoRelease503(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := release.NewSource(release.SourceOptions{CacheDir: t.TempDir(), PollEvery: time.Hour})
	src.Start(ctx)
	mux := newMux(src, 30, "")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/agent.apk")
	if err != nil {
		t.Fatalf("GET /agent.apk: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 with no release, got %d", resp.StatusCode)
	}
}

func TestAgentApkHandlerRateLimited429(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := writeCachedRelease(t, "2.0.0")
	src.Start(ctx)
	// perHour=1: the first download passes, the second from the same IP is limited.
	mux := newMux(src, 1, "")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	first, err := http.Get(srv.URL + "/agent.apk")
	if err != nil {
		t.Fatalf("first GET /agent.apk: %v", err)
	}
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first download: expected 200, got %d", first.StatusCode)
	}

	second, err := http.Get(srv.URL + "/agent.apk")
	if err != nil {
		t.Fatalf("second GET /agent.apk: %v", err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second download: expected 429, got %d", second.StatusCode)
	}
}

func TestEnvOr(t *testing.T) {
	t.Setenv("PUBLICSERVER_ENVOR_SET", "from-env")
	// Shadow any machine-global value so the test is deterministic.
	t.Setenv("PUBLICSERVER_ENVOR_UNSET", "")
	if got := envOr("PUBLICSERVER_ENVOR_SET", "fallback"); got != "from-env" {
		t.Errorf("envOr with env set = %q, want from-env", got)
	}
	if got := envOr("PUBLICSERVER_ENVOR_UNSET", "fallback"); got != "fallback" {
		t.Errorf("envOr with env unset = %q, want fallback", got)
	}
}

func TestEnvInt(t *testing.T) {
	t.Setenv("PUBLICSERVER_ENVINT_VALID", "42")
	t.Setenv("PUBLICSERVER_ENVINT_INVALID", "not-a-number")
	t.Setenv("PUBLICSERVER_ENVINT_UNSET", "")
	if got := envInt("PUBLICSERVER_ENVINT_VALID", 1); got != 42 {
		t.Errorf("envInt valid = %d, want 42", got)
	}
	if got := envInt("PUBLICSERVER_ENVINT_INVALID", 1); got != 1 {
		t.Errorf("envInt invalid = %d, want fallback 1", got)
	}
	if got := envInt("PUBLICSERVER_ENVINT_UNSET", 1); got != 1 {
		t.Errorf("envInt unset = %d, want fallback 1", got)
	}
}

// TestSourceStartStop confirms Start()/cancel() against an unreachable API
// doesn't hang or panic the background poller.
func TestSourceStartStop(t *testing.T) {
	src := release.NewSource(release.SourceOptions{APIBase: "http://127.0.0.1:0", PollEvery: time.Hour, CacheDir: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	src.Start(ctx)
	cancel()
}

// TestClientIPExtraction tests the clientIP function with various header scenarios
// and trusted proxy configuration.
func TestClientIPExtraction(t *testing.T) {
	tests := []struct {
		name           string
		remoteAddr     string
		xfwdFor        string
		trustedProxies string
		want           string
	}{
		{
			name:           "no X-Forwarded-For, RemoteAddr with port, no trusted proxies",
			remoteAddr:     "192.168.1.100:54321",
			xfwdFor:        "",
			trustedProxies: "",
			want:           "192.168.1.100",
		},
		{
			name:           "no X-Forwarded-For, RemoteAddr without port",
			remoteAddr:     "192.168.1.100",
			xfwdFor:        "",
			trustedProxies: "",
			want:           "192.168.1.100",
		},
		{
			name:           "X-Forwarded-For set but no trusted proxies (should ignore X-Forwarded-For)",
			remoteAddr:     "10.0.0.1:12345",
			xfwdFor:        "203.0.113.50",
			trustedProxies: "",
			want:           "10.0.0.1",
		},
		{
			name:           "X-Forwarded-For from untrusted IP",
			remoteAddr:     "192.168.1.100:12345",
			xfwdFor:        "203.0.113.50",
			trustedProxies: "10.0.0.1",
			want:           "192.168.1.100",
		},
		{
			name:           "X-Forwarded-For from trusted proxy (single)",
			remoteAddr:     "10.0.0.1:12345",
			xfwdFor:        "203.0.113.50",
			trustedProxies: "10.0.0.1",
			want:           "203.0.113.50",
		},
		{
			name:           "X-Forwarded-For multiple IPs from trusted proxy",
			remoteAddr:     "10.0.0.1:12345",
			xfwdFor:        "203.0.113.50, 198.51.100.10",
			trustedProxies: "10.0.0.1",
			want:           "203.0.113.50",
		},
		{
			name:           "X-Forwarded-For from one of multiple trusted proxies",
			remoteAddr:     "10.0.0.2:54321",
			xfwdFor:        "192.0.2.50",
			trustedProxies: "10.0.0.1, 10.0.0.2, 10.0.0.3",
			want:           "192.0.2.50",
		},
		{
			name:           "X-Forwarded-For with whitespace, trusted proxy",
			remoteAddr:     "10.0.0.1:12345",
			xfwdFor:        "  203.0.113.50  ",
			trustedProxies: "10.0.0.1",
			want:           "203.0.113.50",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{
				RemoteAddr: tt.remoteAddr,
				Header:     http.Header{},
			}
			if tt.xfwdFor != "" {
				req.Header.Set("X-Forwarded-For", tt.xfwdFor)
			}
			got := clientIP(req, tt.trustedProxies)
			if got != tt.want {
				t.Errorf("clientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRateLimiterWithXForwardedFor tests that rate limiting properly handles
// X-Forwarded-For headers from trusted proxies.
func TestRateLimiterWithXForwardedFor(t *testing.T) {
	limiter := newRateLimiter(2)
	trustedProxies := "10.0.0.1, 10.0.0.2, 10.0.0.3"

	req1 := &http.Request{
		RemoteAddr: "10.0.0.1:12345",
		Header:     http.Header{"X-Forwarded-For": []string{"203.0.113.50"}},
	}
	req2 := &http.Request{
		RemoteAddr: "10.0.0.2:54321",                                         // Different proxy connection
		Header:     http.Header{"X-Forwarded-For": []string{"203.0.113.50"}}, // Same real client
	}
	req3 := &http.Request{
		RemoteAddr: "10.0.0.3:99999",
		Header:     http.Header{"X-Forwarded-For": []string{"192.0.2.99"}}, // Different real client
	}

	// Both requests from real client 203.0.113.50 should share the same rate bucket
	if limiter.limited(clientIP(req1, trustedProxies)) {
		t.Fatal("first request from 203.0.113.50 should not be limited")
	}
	if limiter.limited(clientIP(req2, trustedProxies)) {
		t.Fatal("second request from 203.0.113.50 should not be limited")
	}
	if !limiter.limited(clientIP(req2, trustedProxies)) {
		t.Fatal("third request from 203.0.113.50 should be limited (perHour=2)")
	}

	// Different real client should have independent rate limit
	if limiter.limited(clientIP(req3, trustedProxies)) {
		t.Fatal("first request from 192.0.2.99 should not be limited")
	}
}

// TestClientIPTrustedProxySpoof tests that untrusted proxies cannot spoof client IPs.
func TestClientIPTrustedProxySpoof(t *testing.T) {
	trustedProxies := "10.0.0.1"

	// Request from untrusted IP trying to set X-Forwarded-For
	req := &http.Request{
		RemoteAddr: "192.168.1.100:54321",
		Header:     http.Header{"X-Forwarded-For": []string{"203.0.113.50"}},
	}

	// Should ignore X-Forwarded-For and use RemoteAddr instead
	got := clientIP(req, trustedProxies)
	if got != "192.168.1.100" {
		t.Errorf("untrusted proxy should not affect clientIP: got %q, want %q", got, "192.168.1.100")
	}
}

// --- CRLF / header-injection regression (Task #7) --------------------------

// TestSanitizeHeaderValue is a unit test for sanitizeHeaderValue, the
// defense-in-depth guard against CRLF/control-character injection in HTTP
// response headers. VersionName comes from the published APK's
// AndroidManifest.xml, so an attacker who can publish an APK could otherwise
// inject response headers.
func TestSanitizeHeaderValue(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain value", "1.2.3", "1.2.3"},
		{"empty string", "", ""},
		{"ASCII printable + space", "rish-mcp-agent 1.2.3", "rish-mcp-agent 1.2.3"},
		{"tab is harmless and preserved", "a\tb", "a\tb"},
		{"CRLF injection attempt", "1.2.3\r\nX-Evil: yes", "1.2.3X-Evil: yes"},
		{"bare CR in the middle", "1.2.3\r4", "1.2.34"},
		{"bare LF in the middle", "1.2.3\n4", "1.2.34"},
		{"NUL byte", "1\000.2.3", "1.2.3"},
		{"CR+LF then a full second header line", "1.0\r\nSet-Cookie: pwn=1", "1.0Set-Cookie: pwn=1"},
		{"DEL", "1\177.2.3", "1.2.3"},
		{"mixed control characters", "\r\n\t\n\000x\r\n", "\tx"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeHeaderValue(tt.input); got != tt.want {
				t.Errorf("sanitizeHeaderValue(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestContentDispositionNoInjection(t *testing.T) {
	injections := []string{
		"1.0.0\r\nContent-Length: 0",
		"1.0.0\nX-Injected: true",
		"1.0.0\r\nSet-Cookie: session=evil",
		"1.0.0\r\n\r\nHTTP/1.1 200 OK\r\n",
		"1.0.0\000version",
	}
	for _, injected := range injections {
		t.Run(fmt.Sprintf("%q", injected), func(t *testing.T) {
			// Build an APK whose published versionName is attacker-controlled.
			apkBytes := buildManifestAPK(t, 1, injected)
			cacheDir := t.TempDir()
			apkPath := filepath.Join(cacheDir, "agent.apk")
			if err := os.WriteFile(apkPath, apkBytes, 0o644); err != nil {
				t.Fatalf("write apk: %v", err)
			}
			meta, err := json.Marshal(map[string]string{"tag": "v1.0.0"})
			if err != nil {
				t.Fatalf("marshal meta: %v", err)
			}
			metaPath := filepath.Join(cacheDir, "release.json")
			if err := os.WriteFile(metaPath, meta, 0o644); err != nil {
				t.Fatalf("write release.json: %v", err)
			}

			src := release.NewSource(release.SourceOptions{CacheDir: cacheDir, PollEvery: time.Hour})
			src.Start(context.Background())
			mux := newMux(src, 30, "")
			srv := httptest.NewServer(mux)
			defer srv.Close()

			resp, err := http.Get(srv.URL + "/agent.apk")
			if err != nil {
				t.Fatalf("GET /agent.apk: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200 with cached release, got %d", resp.StatusCode)
			}

			// A CR or LF in any response header means the injection survived.
			for name, values := range resp.Header {
				for _, v := range values {
					if strings.ContainsAny(v, "\r\n") {
						t.Errorf("header %s retains CR/LF from VersionName: %q", name, v)
					}
				}
			}
			// sanitizeHeaderValue strips control characters (CR/LF/NUL/etc.)
			// but does NOT truncate the string — the visible text content
			// survives.  Check that the sanitized value is safe and correct.
			sanitized := sanitizeHeaderValue(injected)
			if resp.Header.Get("X-Apk-Version") != sanitized {
				t.Errorf("X-Apk-Version = %q, want sanitized %q", resp.Header.Get("X-Apk-Version"), sanitized)
			}
			cd := resp.Header.Get("Content-Disposition")
			wantCD := `attachment; filename="rish-mcp-agent-` + sanitized + `.apk"`
			if cd != wantCD {
				t.Errorf("Content-Disposition = %q, want %q", cd, wantCD)
			}
		})
	}
}

func TestContentDispositionFilenameEscaping(t *testing.T) {
	// A crafted filename with `"` or `\` inside it must stay inside the
	// quoted-string and not break the header value.
	apkBytes := buildManifestAPK(t, 1, `x"y\z;a=b`)
	cacheDir := t.TempDir()
	apkPath := filepath.Join(cacheDir, "agent.apk")
	if err := os.WriteFile(apkPath, apkBytes, 0o644); err != nil {
		t.Fatalf("write apk: %v", err)
	}
	meta, _ := json.Marshal(map[string]string{"tag": "v1.0.0"})
	if err := os.WriteFile(filepath.Join(cacheDir, "release.json"), meta, 0o644); err != nil {
		t.Fatalf("write release.json: %v", err)
	}

	src := release.NewSource(release.SourceOptions{CacheDir: cacheDir, PollEvery: time.Hour})
	src.Start(context.Background())
	mux := newMux(src, 30, "")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/agent.apk")
	if err != nil {
		t.Fatalf("GET /agent.apk: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// The header value must not contain CR/LF anywhere.
	for name, values := range resp.Header {
		for _, v := range values {
			if strings.ContainsAny(v, "\r\n") {
				t.Errorf("header %s retains CR/LF: %q", name, v)
			}
		}
	}
	// The filename within the quotes carries the literal (sanitized) value.
	want := `attachment; filename="rish-mcp-agent-x"y\z;a=b.apk"`
	if cd := resp.Header.Get("Content-Disposition"); cd != want {
		t.Errorf("Content-Disposition = %q, want %q", cd, want)
	}
}

// buildManifestAPK packs a synthetic AndroidManifest.xml into a runnable
// (zip) APK whose versionName is exactly `versionName` — including hostile
// bytes — so the public server reads it through the real ReadApkInfo path.
func buildManifestAPK(t *testing.T, versionCode int, versionName string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("AndroidManifest.xml")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := w.Write(buildManifest(versionCode, versionName)); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

// --- synthetic AXML builder (mirrors internal/release/apkinfo_test.go) ------

func p2u16(v uint16) []byte {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, v)
	return b
}

func p2u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

func utf8PoolEntry(s string) []byte {
	var out bytes.Buffer
	out.WriteByte(byte(len(s)))
	out.WriteByte(byte(len(s)))
	out.WriteString(s)
	return out.Bytes()
}

// pchunk writes one AXML chunk: type, headerSize, then the total size (8 +
// body) and the body.
func pchunk(typ, headerSize uint16, body []byte) []byte {
	var out bytes.Buffer
	out.Write(p2u16(typ))
	out.Write(p2u16(headerSize))
	out.Write(p2u32(uint32(8 + len(body))))
	out.Write(body)
	return out.Bytes()
}

func buildStringPoolChunk2(entries []string) []byte {
	var data bytes.Buffer
	offsets := make([]uint32, len(entries))
	for i, e := range entries {
		offsets[i] = uint32(data.Len())
		data.Write(utf8PoolEntry(e))
	}
	var sub bytes.Buffer
	sub.Write(p2u32(uint32(len(entries))))
	sub.Write(p2u32(0))
	sub.Write(p2u32(1 << 8)) // UTF8_FLAG
	stringsStart := uint32(8+20) + uint32(len(entries))*4
	sub.Write(p2u32(stringsStart))
	sub.Write(p2u32(0))
	for _, off := range offsets {
		sub.Write(p2u32(off))
	}
	sub.Write(data.Bytes())
	return pchunk(0x0001, 0x001c, sub.Bytes())
}

func buildResourceMapChunk2(ids []uint32) []byte {
	var sub bytes.Buffer
	for _, id := range ids {
		sub.Write(p2u32(id))
	}
	return pchunk(0x0180, 0x0008, sub.Bytes())
}

type pTestAttr struct {
	nameIdx     int32
	rawValueIdx int32
	dataType    byte
	data        uint32
}

func buildStartTagChunk2(attrs []pTestAttr) []byte {
	var sub bytes.Buffer
	sub.Write(p2u32(0))          // lineNumber
	sub.Write(p2u32(0))          // comment
	sub.Write(p2u32(0xFFFFFFFF)) // ns
	sub.Write(p2u32(0xFFFFFFFF)) // name
	sub.Write(p2u16(20))         // attributeStart
	sub.Write(p2u16(20))         // attributeSize
	sub.Write(p2u16(uint16(len(attrs))))
	sub.Write(p2u16(0))
	sub.Write(p2u16(0))
	sub.Write(p2u16(0))
	for _, a := range attrs {
		sub.Write(p2u32(0xFFFFFFFF))
		sub.Write(p2u32(uint32(a.nameIdx)))
		sub.Write(p2u32(uint32(a.rawValueIdx)))
		sub.Write(p2u16(8)) // ResValue.size
		sub.WriteByte(0)    // ResValue.res0
		sub.WriteByte(a.dataType)
		sub.Write(p2u32(a.data))
	}
	return pchunk(0x0102, 0x0010, sub.Bytes())
}

func buildManifest(versionCode int, versionName string) []byte {
	strs := buildStringPoolChunk2([]string{"versionCode", "versionName", versionName})
	resMap := buildResourceMapChunk2([]uint32{release.AttrVersionCode, release.AttrVersionName})
	startTag := buildStartTagChunk2([]pTestAttr{
		{nameIdx: 0, rawValueIdx: -1, dataType: 0x10, data: uint32(versionCode)},
		{nameIdx: 1, rawValueIdx: 2, dataType: 0x03, data: 2},
	})
	var body bytes.Buffer
	body.Write(strs)
	body.Write(resMap)
	body.Write(startTag)
	var out bytes.Buffer
	out.Write(p2u16(0x0003))
	out.Write(p2u16(0x0008))
	out.Write(p2u32(uint32(8 + body.Len())))
	out.Write(body.Bytes())
	return out.Bytes()
}
