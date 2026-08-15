// Command relay is the rish-mcp relay + MCP server: it exposes run_shell and
// list_devices to AI/MCP clients over POST /mcp, and accepts the outbound
// WebSocket from Android agents on GET /agent.
package main

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/turin-dev/rish-mcp/server/internal/mcp"
	"github.com/turin-dev/rish-mcp/server/internal/oauth"
	"github.com/turin-dev/rish-mcp/server/internal/relay"
)

func main() {
	cfg, err := loadConfigFromEnv()
	if err != nil {
		log.Fatal(err)
	}

	reg := relay.NewRegistry()
	oauthProvider := oauth.NewProvider(oauth.Config{PublicURL: cfg.publicURL, AIToken: cfg.aiToken, TrustedProxies: cfg.trustedProxies})
	mux := newMux(reg, oauthProvider, cfg.aiToken, cfg.deviceToken, cfg.defaultTimeout, cfg.maxTimeout)
	srv := &http.Server{Addr: ":" + cfg.port, Handler: mux}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("relay graceful shutdown failed: %v", err)
		}
	}()

	log.Printf("rish-mcp relay listening on :%s", cfg.port)
	log.Printf("  MCP (AI):  POST /mcp   (Authorization: Bearer AI_TOKEN, or an OAuth access token)")
	log.Printf("  OAuth:     %s/oauth/authorize (claude.ai connectors)", cfg.publicURL)
	log.Printf("  Relay:     WS   /agent?token=DEVICE_TOKEN&deviceId=...&kind=android|watch")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

type runtimeConfig struct {
	port           string
	aiToken        string
	deviceToken    string
	defaultTimeout time.Duration
	maxTimeout     time.Duration
	publicURL      string
	trustedProxies string
}

func loadConfigFromEnv() (runtimeConfig, error) {
	port := envOr("PORT", "8080")
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return runtimeConfig{}, fmt.Errorf("PORT must be an integer from 1 to 65535")
	}
	aiToken := os.Getenv("AI_TOKEN")
	if aiToken == "" {
		return runtimeConfig{}, errors.New("AI_TOKEN is required")
	}
	deviceToken := os.Getenv("DEVICE_TOKEN")
	if deviceToken == "" {
		return runtimeConfig{}, errors.New("DEVICE_TOKEN is required")
	}
	defaultTimeout, err := envDurationMsChecked("DEFAULT_TIMEOUT_MS", 60_000)
	if err != nil {
		return runtimeConfig{}, err
	}
	maxTimeout, err := envDurationMsChecked("MAX_TIMEOUT_MS", 600_000)
	if err != nil {
		return runtimeConfig{}, err
	}
	if defaultTimeout > maxTimeout {
		return runtimeConfig{}, errors.New("DEFAULT_TIMEOUT_MS must not exceed MAX_TIMEOUT_MS")
	}
	publicURL := envOr("PUBLIC_URL", "http://localhost:"+port)
	if os.Getenv("PUBLIC_URL") == "" {
		log.Printf("WARNING: PUBLIC_URL not set; defaulting to %s — set it to the public https URL in production, or OAuth clients (claude.ai connectors) will fail", publicURL)
	}
	trustedProxies := envOr("TRUSTED_PROXIES", "")
	if trustedProxies != "" {
		for _, ip := range strings.Split(trustedProxies, ",") {
			ip = strings.TrimSpace(ip)
			if ip == "" || net.ParseIP(ip) == nil {
				return runtimeConfig{}, fmt.Errorf("TRUSTED_PROXIES contains invalid IP: %q", ip)
			}
		}
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return runtimeConfig{}, errors.New("PUBLIC_URL must be an absolute http(s) URL")
	}
	return runtimeConfig{port: port, aiToken: aiToken, deviceToken: deviceToken, defaultTimeout: defaultTimeout, maxTimeout: maxTimeout, publicURL: publicURL, trustedProxies: trustedProxies}, nil
}

// newMux wires the registry, MCP server, OAuth provider, and HTTP routes
// together. Split out from main() so tests can spin up the whole thing
// against an httptest server without a real listening port or env vars.
func newMux(
	reg *relay.Registry,
	oauthProvider *oauth.Provider,
	aiToken, deviceToken string,
	defaultTimeout, maxTimeout time.Duration,
) *http.ServeMux {
	mcpServer := buildMCPServer(reg, defaultTimeout, maxTimeout)
	mux := http.NewServeMux()
	oauthProvider.RegisterRoutes(mux)
	limiter := relay.NewAgentConnLimiter()
	mux.HandleFunc("/healthz", healthzHandler(reg))
	mux.Handle("/agent", relay.HandleAgent(reg, deviceToken, limiter))
	mux.Handle("/mcp", requireBearer(aiToken, oauthProvider, mcpHandler(mcpServer)))
	return mux
}

// buildMCPServer wires the two MCP tools to the device registry. The tool
// contracts (name, args, response text shape) are carried over unchanged
// from the old TS server — see docs/DESIGN.md §3.3.
func buildMCPServer(reg *relay.Registry, defaultTimeout, maxTimeout time.Duration) *mcp.Server {
	s := mcp.NewServer("rish-mcp", "0.1.0")

	s.RegisterTool(mcp.Tool{
		Name: "run_shell",
		Description: "Executes a shell command on a connected Android phone, tablet, or Wear OS watch " +
			"as shell uid (2000), like adb shell. Returns stdout, stderr and the exit code. " +
			"Use list_devices first if unsure which device is online.",
		InputSchema: mcp.InputSchema{
			Type: "object",
			Properties: map[string]mcp.Property{
				"cmd":       {Type: "string", Description: "The shell command line to run, e.g. 'getprop ro.product.model'"},
				"deviceId":  {Type: "string", Description: "Target device id; optional when exactly one device is connected"},
				"timeoutMs": {Type: "number", Description: "Per-command timeout in ms"},
			},
			Required: []string{"cmd"},
		},
		Handler: runShellHandler(reg, defaultTimeout, maxTimeout),
	})

	s.RegisterTool(mcp.Tool{
		Name:        "list_devices",
		Description: "Lists Android phones, tablets, and Wear OS watches currently connected to the relay.",
		InputSchema: mcp.InputSchema{Type: "object", Properties: map[string]mcp.Property{}},
		Handler:     listDevicesHandler(reg),
	})

	return s
}

func runShellHandler(reg *relay.Registry, defaultTimeout, maxTimeout time.Duration) func(context.Context, json.RawMessage) (mcp.CallResult, error) {
	return func(ctx context.Context, raw json.RawMessage) (mcp.CallResult, error) {
		var args struct {
			Cmd       string `json:"cmd"`
			DeviceID  string `json:"deviceId"`
			TimeoutMs int64  `json:"timeoutMs"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return mcp.CallResult{}, err
		}
		if args.Cmd == "" {
			return mcp.CallResult{}, errors.New("cmd is required")
		}
		if len(args.Cmd) > maxCmdLen {
			return mcp.CallResult{}, errors.New("cmd is too long")
		}
		timeout := defaultTimeout
		if args.TimeoutMs > 0 {
			timeout = time.Duration(args.TimeoutMs) * time.Millisecond
			if timeout > maxTimeout {
				timeout = maxTimeout
			}
		}
		res, err := reg.Exec(ctx, args.DeviceID, args.Cmd, timeout)
		if err != nil {
			return mcp.CallResult{
				Content: []mcp.ContentItem{{Type: "text", Text: err.Error()}},
				IsError: true,
			}, nil
		}
		body := "exit=" + strconv.Itoa(res.Code) + " (" + strconv.FormatInt(res.DurationMs, 10) + "ms)"
		if res.Truncated {
			body += " [output truncated]"
		}
		body += "\n--- stdout ---\n" + res.Stdout
		if res.Stderr != "" {
			body += "\n--- stderr ---\n" + res.Stderr
		}
		return mcp.CallResult{
			Content: []mcp.ContentItem{{Type: "text", Text: body}},
			IsError: res.Code != 0,
		}, nil
	}
}

func listDevicesHandler(reg *relay.Registry) func(context.Context, json.RawMessage) (mcp.CallResult, error) {
	return func(ctx context.Context, raw json.RawMessage) (mcp.CallResult, error) {
		devices := reg.List()
		b, err := json.MarshalIndent(devices, "", "  ")
		if err != nil {
			return mcp.CallResult{}, err
		}
		return mcp.CallResult{Content: []mcp.ContentItem{{Type: "text", Text: string(b)}}}, nil
	}
}

// mcpHandler serves stateless MCP: one JSON-RPC request per POST, no session
// state kept between calls (mirrors the old server's sessionIdGenerator:
// undefined stateless mode).
//
// The request body is capped at maxMCPBodyBytes: net/http does not limit body
// sizes by default, and while /mcp is token-gated, an authenticated (or
// leaked-token) client could otherwise balloon relay memory with one giant
// JSON payload. MaxBytesReader also turns an oversized body into a clean
// decode error instead of an unbounded read.
const maxMCPBodyBytes = 1 << 20 // 1 MiB

// maxCmdLen is the maximum length of a shell command string sent to a device.
// The execFrame is sent over the WebSocket (which has a 1 MB read limit on the
// device side), but the relay itself doesn't enforce a cap on the cmd field.
// A 64 KB limit keeps commands practical while preventing a malicious or
// buggy client from pushing arbitrarily large strings through the relay.
const maxCmdLen = 64 << 10 // 64 KiB

func mcpHandler(s *mcp.Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONRPCError(w, nil, -32000, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxMCPBodyBytes)
		var req mcp.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				// Distinguish an oversized body from malformed JSON: a client
				// that hit the cap gets 413 so it can back off instead of
				// retrying a request that is fundamentally too big.
				writeJSONRPCError(w, nil, -32000, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			writeJSONRPCError(w, nil, -32700, "parse error", http.StatusBadRequest)
			return
		}
		resp := s.Handle(r.Context(), req)
		if resp == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func writeJSONRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(mcp.Response{JSONRPC: "2.0", ID: id, Error: &mcp.RPCError{Code: code, Message: msg}})
}

// requireBearer accepts the static AI_TOKEN or a valid OAuth access token
// (claude.ai custom connectors don't support static bearer tokens, hence the
// OAuth layer — see docs/DESIGN.md §4). A 401 carries WWW-Authenticate so an
// OAuth-capable client can discover the flow via RFC 9728.
func requireBearer(token string, oauthProvider *oauth.Provider, next http.HandlerFunc) http.HandlerFunc {
	const prefix = "Bearer "
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		var presented string
		if len(auth) > len(prefix) && auth[:len(prefix)] == prefix {
			presented = auth[len(prefix):]
		}
		if !hmac.Equal([]byte(presented), []byte(token)) && !oauthProvider.VerifyAccessToken(presented) {
			w.Header().Set("WWW-Authenticate", oauthProvider.WWWAuthenticate())
			writeJSONRPCError(w, nil, -32001, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func healthzHandler(reg *relay.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"devices": len(reg.List()),
		})
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("FATAL: missing required env %s", key)
	}
	return v
}

func envDurationMs(key string, def int64) time.Duration {
	value, err := envDurationMsChecked(key, def)
	if err != nil {
		log.Printf("invalid %s: %v; using default", key, err)
		return time.Duration(def) * time.Millisecond
	}
	return value
}

func envDurationMsChecked(key string, def int64) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return time.Duration(def) * time.Millisecond, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer in milliseconds", key)
	}
	return time.Duration(n) * time.Millisecond, nil
}
