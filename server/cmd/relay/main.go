// Command relay is the rish-mcp relay + MCP server: it exposes run_shell and
// list_devices to AI/MCP clients over POST /mcp, and accepts the outbound
// WebSocket from Android agents on GET /agent.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/turin-dev/rish-mcp/server/internal/mcp"
	"github.com/turin-dev/rish-mcp/server/internal/oauth"
	"github.com/turin-dev/rish-mcp/server/internal/relay"
)

func main() {
	port := envOr("PORT", "8080")
	aiToken := requireEnv("AI_TOKEN")
	deviceToken := requireEnv("DEVICE_TOKEN")
	defaultTimeout := envDurationMs("DEFAULT_TIMEOUT_MS", 60_000)
	maxTimeout := envDurationMs("MAX_TIMEOUT_MS", 600_000)
	publicURL := envOr("PUBLIC_URL", "http://localhost:"+port)

	reg := relay.NewRegistry()
	oauthProvider := oauth.NewProvider(oauth.Config{PublicURL: publicURL, AIToken: aiToken})
	mux := newMux(reg, oauthProvider, aiToken, deviceToken, defaultTimeout, maxTimeout)

	log.Printf("rish-mcp relay listening on :%s", port)
	log.Printf("  MCP (AI):  POST /mcp   (Authorization: Bearer AI_TOKEN, or an OAuth access token)")
	log.Printf("  OAuth:     %s/oauth/authorize (claude.ai connectors)", publicURL)
	log.Printf("  Relay:     WS   /agent?token=DEVICE_TOKEN&deviceId=...&kind=android|watch")
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
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
	mux.HandleFunc("/healthz", healthzHandler(reg))
	mux.Handle("/agent", relay.HandleAgent(reg, deviceToken))
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
func mcpHandler(s *mcp.Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONRPCError(w, nil, -32000, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req mcp.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
		if presented != token && !oauthProvider.VerifyAccessToken(presented) {
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
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return time.Duration(n) * time.Millisecond
		}
	}
	return time.Duration(def) * time.Millisecond
}
