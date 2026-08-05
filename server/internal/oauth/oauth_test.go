package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
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
