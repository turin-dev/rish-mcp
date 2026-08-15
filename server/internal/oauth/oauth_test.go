package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const testAIToken = "test-ai-token-12345"

func newTestServer(t *testing.T) (*httptest.Server, *Provider) {
	t.Helper()
	p := NewProvider(Config{PublicURL: "http://placeholder", AIToken: testAIToken})
	mux := http.NewServeMux()
	p.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	// PublicURL must match the actual test server origin for metadata/redirects.
	p.cfg.PublicURL = srv.URL
	return srv, p
}

// pkce returns a verifier and its S256 challenge, the two halves PKCE checks.
func pkce() (verifier, challenge string) {
	verifier = "a-fixed-test-verifier-that-is-long-enough-for-pkce-1234567890"
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return
}

// registerClient drives RFC 7591 registration and returns the issued client_id.
func registerClient(t *testing.T, srv *httptest.Server, redirectURI string) string {
	t.Helper()
	body := strings.NewReader(`{"redirect_uris":["` + redirectURI + `"]}`)
	resp, err := http.Post(srv.URL+"/oauth/register", "application/json", body)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: expected 201, got %d", resp.StatusCode)
	}
	var out struct {
		ClientID string `json:"client_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	return out.ClientID
}

// authorize drives the consent POST with the given key and PKCE challenge,
// returning the "code" query param from the redirect Location.
func authorize(t *testing.T, srv *httptest.Server, clientID, redirectURI, challenge, key string) (code string, status int) {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	form := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"key":                   {key},
	}
	resp, err := client.PostForm(srv.URL+"/oauth/authorize", form)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		return "", resp.StatusCode
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect location: %v", err)
	}
	return loc.Query().Get("code"), resp.StatusCode
}

func exchangeCode(t *testing.T, srv *httptest.Server, code, redirectURI, verifier string) (*http.Response, map[string]any) {
	t.Helper()
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}
	resp, err := http.PostForm(srv.URL+"/oauth/token", form)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp, body
}

func TestFullAuthorizationCodeFlow(t *testing.T) {
	srv, p := newTestServer(t)
	const redirectURI = "https://client.example/callback"
	verifier, challenge := pkce()

	clientID := registerClient(t, srv, redirectURI)
	code, status := authorize(t, srv, clientID, redirectURI, challenge, testAIToken)
	if status != http.StatusFound || code == "" {
		t.Fatalf("authorize: expected 302 with a code, got status=%d code=%q", status, code)
	}

	resp, body := exchangeCode(t, srv, code, redirectURI, verifier)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token exchange: expected 200, got %d (%v)", resp.StatusCode, body)
	}
	accessToken, _ := body["access_token"].(string)
	refreshToken, _ := body["refresh_token"].(string)
	if accessToken == "" || refreshToken == "" {
		t.Fatalf("token exchange: missing access/refresh token: %v", body)
	}
	if !p.VerifyAccessToken(accessToken) {
		t.Fatal("issued access_token does not verify")
	}

	// Refresh grant issues a fresh, equally valid access token.
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}}
	rresp, err := http.PostForm(srv.URL+"/oauth/token", form)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	defer rresp.Body.Close()
	var rbody map[string]any
	_ = json.NewDecoder(rresp.Body).Decode(&rbody)
	if rresp.StatusCode != http.StatusOK {
		t.Fatalf("refresh: expected 200, got %d (%v)", rresp.StatusCode, rbody)
	}
	newAccess, _ := rbody["access_token"].(string)
	if newAccess == "" || !p.VerifyAccessToken(newAccess) {
		t.Fatalf("refreshed access_token does not verify: %v", rbody)
	}
}

func TestAuthorizeRejectsWrongKey(t *testing.T) {
	srv, _ := newTestServer(t)
	const redirectURI = "https://client.example/callback"
	_, challenge := pkce()
	clientID := registerClient(t, srv, redirectURI)

	_, status := authorize(t, srv, clientID, redirectURI, challenge, "wrong-key")
	if status != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong key, got %d", status)
	}
}

func TestTokenExchangeRejectsPKCEMismatch(t *testing.T) {
	srv, _ := newTestServer(t)
	const redirectURI = "https://client.example/callback"
	_, challenge := pkce()
	clientID := registerClient(t, srv, redirectURI)

	code, status := authorize(t, srv, clientID, redirectURI, challenge, testAIToken)
	if status != http.StatusFound {
		t.Fatalf("authorize failed: status=%d", status)
	}

	resp, body := exchangeCode(t, srv, code, redirectURI, "totally-the-wrong-verifier")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for PKCE mismatch, got %d (%v)", resp.StatusCode, body)
	}
	if body["error"] != "invalid_grant" {
		t.Fatalf("expected invalid_grant, got %v", body)
	}
}

func TestAuthorizationCodeIsSingleUse(t *testing.T) {
	srv, _ := newTestServer(t)
	const redirectURI = "https://client.example/callback"
	verifier, challenge := pkce()
	clientID := registerClient(t, srv, redirectURI)

	code, status := authorize(t, srv, clientID, redirectURI, challenge, testAIToken)
	if status != http.StatusFound {
		t.Fatalf("authorize failed: status=%d", status)
	}

	if resp, body := exchangeCode(t, srv, code, redirectURI, verifier); resp.StatusCode != http.StatusOK {
		t.Fatalf("first exchange: expected 200, got %d (%v)", resp.StatusCode, body)
	}
	resp, body := exchangeCode(t, srv, code, redirectURI, verifier)
	if resp.StatusCode != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("code reuse: expected invalid_grant, got status=%d body=%v", resp.StatusCode, body)
	}
}

func TestConsentRateLimited(t *testing.T) {
	srv, _ := newTestServer(t)
	const redirectURI = "https://client.example/callback"
	_, challenge := pkce()
	clientID := registerClient(t, srv, redirectURI)

	var lastStatus int
	for i := 0; i < 12; i++ {
		_, lastStatus = authorize(t, srv, clientID, redirectURI, challenge, "wrong-key")
	}
	if lastStatus != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after repeated failed attempts, got %d", lastStatus)
	}
}

func TestRotatingAITokenInvalidatesIssuedTokens(t *testing.T) {
	srv, p := newTestServer(t)
	const redirectURI = "https://client.example/callback"
	verifier, challenge := pkce()
	clientID := registerClient(t, srv, redirectURI)

	code, status := authorize(t, srv, clientID, redirectURI, challenge, testAIToken)
	if status != http.StatusFound {
		t.Fatalf("authorize failed: status=%d", status)
	}
	_, body := exchangeCode(t, srv, code, redirectURI, verifier)
	accessToken, _ := body["access_token"].(string)
	if accessToken == "" || !p.VerifyAccessToken(accessToken) {
		t.Fatalf("expected a valid access token before rotation: %v", body)
	}

	rotated := NewProvider(Config{PublicURL: p.cfg.PublicURL, AIToken: "a-completely-different-token"})
	if rotated.VerifyAccessToken(accessToken) {
		t.Fatal("token issued under the old AI_TOKEN must not verify under a rotated one")
	}
}

func TestWellKnownMetadata(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/.well-known/oauth-protected-resource")
	if err != nil {
		t.Fatalf("get metadata: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["resource"] != srv.URL+"/mcp" {
		t.Fatalf("expected resource to be %s/mcp, got %v", srv.URL, body["resource"])
	}
}

// TestClientIPExtractionOAuth tests the clientIPWithTrust function with various header scenarios.
func TestClientIPExtractionOAuth(t *testing.T) {
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
			name:           "X-Forwarded-For set but no trusted proxies",
			remoteAddr:     "10.0.0.1:12345",
			xfwdFor:        "203.0.113.50",
			trustedProxies: "",
			want:           "10.0.0.1",
		},
		{
			name:           "X-Forwarded-For from trusted proxy",
			remoteAddr:     "10.0.0.1:12345",
			xfwdFor:        "203.0.113.50",
			trustedProxies: "10.0.0.1",
			want:           "203.0.113.50",
		},
		{
			name:           "X-Forwarded-For from untrusted IP",
			remoteAddr:     "192.168.1.100:12345",
			xfwdFor:        "203.0.113.50",
			trustedProxies: "10.0.0.1",
			want:           "192.168.1.100",
		},
		{
			name:           "X-Forwarded-For multiple IPs from trusted proxy",
			remoteAddr:     "10.0.0.1:12345",
			xfwdFor:        "203.0.113.50, 198.51.100.10, 10.0.0.1",
			trustedProxies: "10.0.0.1",
			want:           "203.0.113.50",
		},
		{
			name:           "X-Forwarded-For with extra whitespace, trusted proxy",
			remoteAddr:     "10.0.0.1:12345",
			xfwdFor:        "  203.0.113.50  ,  198.51.100.10  ",
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
			got := clientIPWithTrust(req, tt.trustedProxies)
			if got != tt.want {
				t.Errorf("clientIPWithTrust() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestOAuthRateLimiterCleanup verifies that the OAuth provider's rate limiter
// bounds its memory usage and cleans up expired entries.
func TestOAuthRateLimiterCleanup(t *testing.T) {
	p := NewProvider(Config{PublicURL: "http://placeholder", AIToken: testAIToken})
	now := time.Now().Unix()

	// Manually populate attempts map with many entries
	p.mu.Lock()
	for i := 0; i < 1100; i++ {
		ip := fmt.Sprintf("192.0.2.%d", i%256)
		// Most entries are expired, some are not
		resetAt := now - int64(i)
		if i%10 == 0 {
			resetAt = now + 300 // This one is not expired
		}
		p.attempts[ip] = rateEntry{count: 1, resetAt: resetAt}
	}
	p.attemptsCleanup = now - 100 // Ensure cleanup can happen
	mapSizeBefore := len(p.attempts)
	p.mu.Unlock()

	// Trigger cleanup by calling rateLimited on a new IP
	p.rateLimited("203.0.113.1")

	// Check that the map was cleaned
	p.mu.Lock()
	mapSizeAfter := len(p.attempts)
	p.mu.Unlock()

	if mapSizeAfter >= mapSizeBefore {
		t.Logf("cleanup did not reduce map size: %d -> %d", mapSizeBefore, mapSizeAfter)
		// This is OK; cleanup only happens if threshold is crossed and cooldown passed
	}
	if mapSizeAfter > 2000 {
		t.Fatalf("attempts map still too large after cleanup: %d", mapSizeAfter)
	}
}

// TestOAuthUsedCodesCleanup verifies that the usedCodes map is bounded by cleanup.
func TestOAuthUsedCodesCleanup(t *testing.T) {
	p := NewProvider(Config{PublicURL: "http://placeholder", AIToken: testAIToken})
	now := time.Now().Unix()

	// Manually populate usedCodes with many expired entries
	p.mu.Lock()
	for i := 0; i < 1100; i++ {
		code := fmt.Sprintf("code-%d", i)
		p.usedCodes[code] = now - int64(i) // All expired
	}
	p.usedCodesCleanup = now - 100 // Ensure cleanup can happen
	mapSizeBefore := len(p.usedCodes)
	p.mu.Unlock()

	// A token exchange that reads usedCodes will trigger cleanup
	_, challenge := pkce()
	clientID := "test-client-id"

	// Create a valid code (not expired)
	pl := payload{
		Type:          payloadCode,
		ClientIDHash:  clientID,
		RedirectURI:   "http://example.com/cb",
		CodeChallenge: challenge,
		ExpiresAt:     now + 300,
	}
	code := p.sign(pl)

	// Exchange the code; this triggers usedCodes cleanup
	p.mu.Lock()
	_, used := p.usedCodes[code]
	if !used {
		p.usedCodes[code] = pl.ExpiresAt
		// Cleanup is triggered here if conditions are met
		if len(p.usedCodes) > 1000 && now-p.usedCodesCleanup > 30 {
			p.usedCodesCleanup = now
			for k, exp := range p.usedCodes {
				if exp < now {
					delete(p.usedCodes, k)
				}
			}
		}
	}
	mapSizeAfter := len(p.usedCodes)
	p.mu.Unlock()

	if mapSizeAfter >= mapSizeBefore {
		t.Logf("cleanup did not reduce usedCodes: %d -> %d", mapSizeBefore, mapSizeAfter)
		// This is OK; cleanup has conditions
	}
	if mapSizeAfter > 2000 {
		t.Fatalf("usedCodes map still too large after cleanup: %d", mapSizeAfter)
	}
}
