// Package oauth is a minimal OAuth 2.0 authorization server in front of
// /mcp, so clients that only speak OAuth (claude.ai custom connectors, hence
// the Claude mobile app) can connect. Single-user by design: "logging in" at
// /oauth/authorize means typing the relay's AI_TOKEN once; everything issued
// afterwards is a stateless HMAC-signed token derived from that same secret,
// so nothing is persisted and rotating AI_TOKEN revokes every issued token
// at once. Ported from before/server/src/oauth.ts — see before/docs/USAGE.md
// §6 for the full protocol writeup.
//
// Implements the parts MCP clients need (RFC 8414 + 9728 metadata, RFC 7591
// dynamic client registration, authorization-code grant with mandatory PKCE
// S256, refresh_token grant) and nothing else.
package oauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

const codeTTL = 5 * time.Minute

type Config struct {
	PublicURL      string // external base URL, no trailing slash, e.g. https://mcp.example.com
	AIToken        string // the "access key" the owner types on the consent page
	AccessTTL      time.Duration
	RefreshTTL     time.Duration
	TrustedProxies string // comma-separated list of proxy IPs that may set X-Forwarded-For; empty = no proxies trusted
}

type payloadType string

const (
	payloadClient       payloadType = "client"
	payloadCode         payloadType = "code"
	payloadAccessToken  payloadType = "at"
	payloadRefreshToken payloadType = "rt"
)

// payload mirrors the TS discriminated union oauth.ts uses for every issued
// token; Go has no such union so unused fields per Type are just omitted.
type payload struct {
	Type          payloadType `json:"t"`
	RedirectURIs  []string    `json:"ru,omitempty"`  // client
	ClientIDHash  string      `json:"cid,omitempty"` // code, rt
	RedirectURI   string      `json:"redirect_uri,omitempty"`
	CodeChallenge string      `json:"cc,omitempty"`
	IssuedAt      int64       `json:"iat,omitempty"`
	ExpiresAt     int64       `json:"exp,omitempty"`
}

type rateEntry struct {
	count   int
	resetAt int64
}

type Provider struct {
	cfg Config
	key []byte

	mu               sync.Mutex
	usedCodes        map[string]int64 // best-effort single-use enforcement; PKCE is the real guard
	usedCodesCleanup int64            // timestamp of last cleanup
	attempts         map[string]rateEntry
	attemptsCleanup  int64 // timestamp of last cleanup
}

func NewProvider(cfg Config) *Provider {
	if cfg.AccessTTL == 0 {
		cfg.AccessTTL = time.Hour
	}
	if cfg.RefreshTTL == 0 {
		cfg.RefreshTTL = 90 * 24 * time.Hour
	}
	cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")

	mac := hmac.New(sha256.New, []byte("rish-mcp-oauth-v1"))
	mac.Write([]byte(cfg.AIToken))

	return &Provider{
		cfg:       cfg,
		key:       mac.Sum(nil),
		usedCodes: make(map[string]int64),
		attempts:  make(map[string]rateEntry),
	}
}

// VerifyAccessToken is what the /mcp bearer check calls: accepts tokens
// issued via the OAuth flow, alongside the static AI_TOKEN.
func (p *Provider) VerifyAccessToken(token string) bool {
	pl, ok := p.verify(token)
	return ok && pl.Type == payloadAccessToken
}

func (p *Provider) WWWAuthenticate() string {
	return fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`, p.cfg.PublicURL)
}

// RegisterRoutes mounts every OAuth endpoint on mux.
func (p *Provider) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", p.handleASMetadata)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server/", p.handleASMetadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", p.handlePRMetadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/", p.handlePRMetadata)
	mux.HandleFunc("POST /oauth/register", p.handleRegister)
	mux.HandleFunc("GET /oauth/authorize", p.handleAuthorizeGet)
	mux.HandleFunc("POST /oauth/authorize", p.handleAuthorizePost)
	mux.HandleFunc("POST /oauth/token", p.handleToken)
}

// --- signing ----------------------------------------------------------------

func (p *Provider) sign(pl payload) string {
	body, _ := json.Marshal(pl)
	b64body := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, p.key)
	mac.Write([]byte(b64body))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return b64body + "." + sig
}

func (p *Provider) verify(token string) (payload, bool) {
	dot := strings.LastIndex(token, ".")
	if dot < 1 {
		return payload{}, false
	}
	body, sig := token[:dot], token[dot+1:]
	mac := hmac.New(sha256.New, p.key)
	mac.Write([]byte(body))
	expect := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expect)) {
		return payload{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return payload{}, false
	}
	var pl payload
	if err := json.Unmarshal(raw, &pl); err != nil {
		return payload{}, false
	}
	if pl.ExpiresAt != 0 && pl.ExpiresAt < time.Now().Unix() {
		return payload{}, false
	}
	return pl, true
}

func sha256B64(s string) string {
	sum := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// --- rate limiting (consent-page submissions only — the sole brute-forceable
// surface, since it's the only place the real secret is compared) -----------

func (p *Provider) rateLimited(ip string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now().Unix()
	e, ok := p.attempts[ip]
	if !ok || e.resetAt < now {
		p.attempts[ip] = rateEntry{count: 1, resetAt: now + 300}
		// Opportunistic cleanup: expire old entries and prevent unbounded growth.
		// Cleanup is a linear scan, so only do it when the map is getting large.
		if len(p.attempts) > 1000 && now-p.attemptsCleanup > 30 {
			p.attemptsCleanup = now
			for k, v := range p.attempts {
				if v.resetAt < now {
					delete(p.attempts, k)
				}
			}
		}
		return false
	}
	e.count++
	p.attempts[ip] = e
	return e.count > 10
}

// clientIPWithTrust extracts the client's real IP with explicit trusted proxy support.
// trustedProxies is a comma-separated list of proxy IPs that may set X-Forwarded-For.
// If trustedProxies is empty, X-Forwarded-For is ignored (safe default).
// If trustedProxies is non-empty, X-Forwarded-For is trusted only if RemoteAddr
// matches one of the trusted proxy IPs.
func clientIPWithTrust(r *http.Request, trustedProxies string) string {
	remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if remoteIP == "" {
		remoteIP = r.RemoteAddr
	}

	// Only trust X-Forwarded-For if the direct connection is from a trusted proxy.
	if trustedProxies != "" && isTrustedProxyOAuth(remoteIP, trustedProxies) {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			// Take the first (leftmost) IP; it's the original client.
			if i := strings.IndexByte(fwd, ','); i >= 0 {
				fwd = fwd[:i]
			}
			if ip := strings.TrimSpace(fwd); ip != "" {
				return ip
			}
		}
	}

	// Fallback: use the direct connection IP.
	return remoteIP
}

func isTrustedProxyOAuth(ip, trustedProxies string) bool {
	for _, trusted := range strings.Split(trustedProxies, ",") {
		if strings.TrimSpace(trusted) == ip {
			return true
		}
	}
	return false
}

// --- well-known metadata ------------------------------------------------

func (p *Provider) handleASMetadata(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                p.cfg.PublicURL,
		"authorization_endpoint":                p.cfg.PublicURL + "/oauth/authorize",
		"token_endpoint":                        p.cfg.PublicURL + "/oauth/token",
		"registration_endpoint":                 p.cfg.PublicURL + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{},
	})
}

func (p *Provider) handlePRMetadata(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 p.cfg.PublicURL + "/mcp",
		"authorization_servers":    []string{p.cfg.PublicURL},
		"bearer_methods_supported": []string{"header"},
	})
}

// --- dynamic client registration (RFC 7591) ------------------------------

func (p *Provider) handleRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RedirectURIs []string `json:"redirect_uris"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "invalid JSON body")
		return
	}
	if len(body.RedirectURIs) == 0 || !allHTTPURLs(body.RedirectURIs) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "redirect_uris (http/https) required")
		return
	}
	clientID := p.sign(payload{Type: payloadClient, RedirectURIs: body.RedirectURIs, IssuedAt: time.Now().Unix()})
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  clientID,
		"redirect_uris":              body.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	})
}

func allHTTPURLs(uris []string) bool {
	for _, u := range uris {
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return false
		}
	}
	return true
}

// --- authorize (consent) ---------------------------------------------------

type authRequest struct {
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	State         string
}

func (p *Provider) parseAuthRequest(v map[string][]string) (authRequest, error) {
	get := func(k string) string {
		if vs := v[k]; len(vs) > 0 {
			return vs[0]
		}
		return ""
	}
	clientID := get("client_id")
	redirectURI := get("redirect_uri")
	pl, ok := p.verify(clientID)
	if !ok || pl.Type != payloadClient {
		return authRequest{}, errors.New("unknown client_id")
	}
	if !slices.Contains(pl.RedirectURIs, redirectURI) {
		return authRequest{}, errors.New("redirect_uri not registered for this client")
	}
	if get("response_type") != "code" {
		return authRequest{}, errors.New("response_type must be 'code'")
	}
	cc := get("code_challenge")
	if cc == "" || get("code_challenge_method") != "S256" {
		return authRequest{}, errors.New("PKCE S256 code_challenge required")
	}
	return authRequest{ClientID: clientID, RedirectURI: redirectURI, CodeChallenge: cc, State: get("state")}, nil
}

func (p *Provider) handleAuthorizeGet(w http.ResponseWriter, r *http.Request) {
	req, err := p.parseAuthRequest(r.URL.Query())
	if err != nil {
		http.Error(w, "invalid authorization request: "+err.Error(), http.StatusBadRequest)
		return
	}
	renderConsentPage(w, http.StatusOK, req, "")
}

func (p *Provider) handleAuthorizePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if ip := clientIPWithTrust(r, p.cfg.TrustedProxies); p.rateLimited(ip) {
		log.Printf("[oauth] authorize rate limited: %s", ip)
		http.Error(w, "too many attempts, try again in a few minutes", http.StatusTooManyRequests)
		return
	}
	req, err := p.parseAuthRequest(r.PostForm)
	if err != nil {
		http.Error(w, "invalid authorization request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !hmac.Equal([]byte(r.PostFormValue("key")), []byte(p.cfg.AIToken)) {
		renderConsentPage(w, http.StatusUnauthorized, req, "Wrong access key.")
		return
	}
	code := p.sign(payload{
		Type:          payloadCode,
		ClientIDHash:  sha256B64(req.ClientID),
		RedirectURI:   req.RedirectURI,
		CodeChallenge: req.CodeChallenge,
		ExpiresAt:     time.Now().Add(codeTTL).Unix(),
	})
	loc, err := url.Parse(req.RedirectURI)
	if err != nil {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}
	q := loc.Query()
	q.Set("code", code)
	if req.State != "" {
		q.Set("state", req.State)
	}
	loc.RawQuery = q.Encode()
	http.Redirect(w, r, loc.String(), http.StatusFound)
}

// --- token ------------------------------------------------------------------

func (p *Provider) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "")
		return
	}
	switch r.PostFormValue("grant_type") {
	case "authorization_code":
		p.handleAuthCodeGrant(w, r)
	case "refresh_token":
		p.handleRefreshGrant(w, r)
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "")
	}
}

func (p *Provider) handleAuthCodeGrant(w http.ResponseWriter, r *http.Request) {
	code := r.PostFormValue("code")
	pl, ok := p.verify(code)
	if !ok || pl.Type != payloadCode {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "bad or expired code")
		return
	}

	p.mu.Lock()
	_, used := p.usedCodes[code]
	if !used {
		p.usedCodes[code] = pl.ExpiresAt
		// Opportunistic cleanup: prevent unbounded growth of usedCodes map.
		// Cleanup scans the map, so only do it when getting large and not too frequently.
		now := time.Now().Unix()
		if len(p.usedCodes) > 1000 && now-p.usedCodesCleanup > 30 {
			p.usedCodesCleanup = now
			for k, exp := range p.usedCodes {
				if exp < now {
					delete(p.usedCodes, k)
				}
			}
		}
	}
	p.mu.Unlock()
	if used {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "code already used")
		return
	}

	if pl.RedirectURI != r.PostFormValue("redirect_uri") {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri mismatch")
		return
	}
	verifier := r.PostFormValue("code_verifier")
	if sha256B64(verifier) != pl.CodeChallenge {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	writeJSON(w, http.StatusOK, p.issueTokens(pl.ClientIDHash))
}

func (p *Provider) handleRefreshGrant(w http.ResponseWriter, r *http.Request) {
	pl, ok := p.verify(r.PostFormValue("refresh_token"))
	if !ok || pl.Type != payloadRefreshToken {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "bad or expired refresh_token")
		return
	}
	writeJSON(w, http.StatusOK, p.issueTokens(pl.ClientIDHash))
}

func (p *Provider) issueTokens(cid string) map[string]any {
	return map[string]any{
		"access_token": p.sign(payload{Type: payloadAccessToken, ExpiresAt: time.Now().Add(p.cfg.AccessTTL).Unix()}),
		"token_type":   "Bearer",
		"expires_in":   int(p.cfg.AccessTTL.Seconds()),
		"refresh_token": p.sign(payload{
			Type: payloadRefreshToken, ClientIDHash: cid, ExpiresAt: time.Now().Add(p.cfg.RefreshTTL).Unix(),
		}),
	}
}

// --- consent page (html/template auto-escapes, so no manual HTML escaping) --

var consentTmpl = template.Must(template.New("consent").Parse(`<!doctype html>
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>rish-mcp — authorize</title>
<style>
  body { font: 16px system-ui, sans-serif; background: #111; color: #eee; display: grid; place-items: center; min-height: 100vh; margin: 0; }
  form { background: #1c1c1e; padding: 2rem; border-radius: 12px; width: min(90vw, 22rem); }
  h1 { font-size: 1.1rem; margin: 0 0 .5rem; }
  p { color: #9a9aa0; font-size: .85rem; margin: 0 0 1rem; }
  input[type=password] { width: 100%; box-sizing: border-box; padding: .6rem; border-radius: 8px; border: 1px solid #333; background: #111; color: #eee; }
  button { margin-top: 1rem; width: 100%; padding: .6rem; border: 0; border-radius: 8px; background: #d97757; color: #fff; font-weight: 600; cursor: pointer; }
  .err { color: #ff6b6b; font-size: .85rem; margin-top: .5rem; }
</style>
<form method="post" action="">
  <h1>rish-mcp</h1>
  <p>An MCP client is asking for shell access to your phone. Paste the relay's <code>AI_TOKEN</code> to allow it.</p>
  <input type="hidden" name="response_type" value="code">
  <input type="hidden" name="client_id" value="{{.ClientID}}">
  <input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
  <input type="hidden" name="code_challenge" value="{{.CodeChallenge}}">
  <input type="hidden" name="code_challenge_method" value="S256">
  {{if .State}}<input type="hidden" name="state" value="{{.State}}">{{end}}
  <input type="password" name="key" placeholder="access key (AI_TOKEN)" autofocus required>
  {{if .Error}}<div class="err">{{.Error}}</div>{{end}}
  <button>Authorize</button>
</form>`))

func renderConsentPage(w http.ResponseWriter, status int, req authRequest, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = consentTmpl.Execute(w, struct {
		authRequest
		Error string
	}{req, errMsg})
}

// --- json helpers -----------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeOAuthError(w http.ResponseWriter, status int, code, desc string) {
	body := map[string]string{"error": code}
	if desc != "" {
		body["error_description"] = desc
	}
	writeJSON(w, status, body)
}
