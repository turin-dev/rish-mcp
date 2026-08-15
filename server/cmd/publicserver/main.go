// Command publicserver is the public release endpoint — a separate process
// from the relay, ported from before/server/src/public.ts.
//
// The relay holds shell access to the owner's devices and is gated behind
// AI_TOKEN/DEVICE_TOKEN. This process holds no secrets and answers no
// questions about devices: it only says which agent build is current and
// hands out that APK. Running it as its own binary/container means the
// public hostname has no route to /mcp or /agent even if a proxy rule is
// wrong (docs/DESIGN.md §2.3/§5).
package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/turin-dev/rish-mcp/server/internal/release"
)

func main() {
	port := envOr("PORT", "8080")
	downloadsPerHour := envInt("DOWNLOADS_PER_HOUR", 30)
	// TRUSTED_PROXIES: comma-separated list of proxy IPs that can set X-Forwarded-For.
	// Empty string = no proxies trusted, only use RemoteAddr (safe default).
	// Use only if behind a reverse proxy like Traefik.
	trustedProxies := envOr("TRUSTED_PROXIES", "")

	src := release.NewSource(release.SourceOptionsFromEnv())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src.Start(ctx)

	mux := newMux(src, downloadsPerHour, trustedProxies)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	srv := &http.Server{Addr: ":" + port, Handler: mux}
	go func() {
		<-stop
		cancel()
		_ = srv.Close()
	}()

	log.Printf("rish-mcp public release endpoint on :%s", port)
	log.Printf("  GET /api/version/release   current agent build")
	log.Printf("  GET /agent.apk             that build, unauthenticated")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func newMux(src *release.Source, downloadsPerHour int, trustedProxies string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthzHandler(src))
	mux.HandleFunc("GET /api/version/release", versionHandler(src))
	mux.HandleFunc("GET /agent.apk", agentApkHandler(src, newRateLimiter(downloadsPerHour), trustedProxies))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	return mux
}

func healthzHandler(src *release.Source) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rel := src.Get()
		w.Header().Set("Content-Type", "application/json")
		if rel == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "release": nil})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "release": rel.VersionName})
	}
}

func versionHandler(src *release.Source) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rel := src.Get()
		if rel == nil {
			http.Error(w, `{"error":"release metadata unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"versionName": rel.VersionName,
			"versionCode": rel.VersionCode,
			"tag":         rel.Tag,
			"sizeBytes":   rel.SizeBytes,
			"sha256":      rel.SHA256,
			"modifiedAt":  rel.ModifiedAt,
			"download":    "/agent.apk",
		})
	}
}

func agentApkHandler(src *release.Source, limiter *rateLimiter, trustedProxies string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rel := src.Get()
		if rel == nil {
			http.Error(w, "apk not available", http.StatusServiceUnavailable)
			return
		}
		if limiter.limited(clientIP(r, trustedProxies)) {
			http.Error(w, "too many downloads; try again later", http.StatusTooManyRequests)
			return
		}
		// VersionName is attacker-influenced (it comes from the published APK's
		// AndroidManifest.xml) and http.Header.Set does not strip CR/LF, so an
		// un-sanitized value could inject response headers. Strip control
		// characters before embedding; see sanitizeHeaderValue.
		version := sanitizeHeaderValue(rel.VersionName)
		w.Header().Set("X-Apk-Version", version)
		w.Header().Set("X-Apk-Version-Code", strconv.Itoa(rel.VersionCode))
		w.Header().Set("X-Apk-Sha256", rel.SHA256)
		w.Header().Set("Content-Disposition", `attachment; filename="rish-mcp-agent-`+version+`.apk"`)
		w.Header().Set("Content-Type", "application/vnd.android.package-archive")
		http.ServeFile(w, r, rel.Path)
	}
}

// --- per-IP rate limit (the download route is unauthenticated by design) ---

type rateEntry struct {
	count   int
	resetAt int64
}

type rateLimiter struct {
	perHour          int
	mu               sync.Mutex
	hits             map[string]rateEntry
	lastCleanup      int64 // avoid excessive cleanup iterations
	cleanupThreshold int   // trigger cleanup when map exceeds this size
}

func newRateLimiter(perHour int) *rateLimiter {
	return &rateLimiter{
		perHour:          perHour,
		hits:             make(map[string]rateEntry),
		cleanupThreshold: 10_000,
	}
}

func (l *rateLimiter) limited(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now().Unix()
	e, ok := l.hits[ip]
	if !ok || e.resetAt < now {
		l.hits[ip] = rateEntry{count: 1, resetAt: now + 3600}
		// Opportunistic cleanup: expire old entries before they pile up
		if len(l.hits) > l.cleanupThreshold && now-l.lastCleanup > 60 {
			l.lastCleanup = now
			for k, v := range l.hits {
				if v.resetAt < now {
					delete(l.hits, k)
				}
			}
		}
		return false
	}
	e.count++
	l.hits[ip] = e
	return e.count > l.perHour
}

// clientIP extracts the client's real IP with trusted proxy support.
// trustedProxies is a comma-separated list of proxy IPs that may set X-Forwarded-For.
// If trustedProxies is empty, X-Forwarded-For is ignored (safe default).
// If trustedProxies is non-empty, X-Forwarded-For is trusted only if RemoteAddr
// matches one of the trusted proxy IPs.
func clientIP(r *http.Request, trustedProxies string) string {
	remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if remoteIP == "" {
		remoteIP = r.RemoteAddr
	}

	// Only trust X-Forwarded-For if the direct connection is from a trusted proxy.
	if trustedProxies != "" && isTrustedProxy(remoteIP, trustedProxies) {
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

func isTrustedProxy(ip, trustedProxies string) bool {
	for _, trusted := range strings.Split(trustedProxies, ",") {
		if strings.TrimSpace(trusted) == ip {
			return true
		}
	}
	return false
}

// sanitizeHeaderValue strips CR, LF, NUL, and other control characters
// (U+0000–U+001F except TAB, and U+007F) from a string so it can be safely
// embedded in an HTTP response header value. This is defense-in-depth: the
// Go http.Header.Set method does not validate or strip these characters.
func sanitizeHeaderValue(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		// Allow printable ASCII, TAB (\t), and anything ≥ 0x20 except DEL.
		if c >= 0x20 && c != 0x7f {
			b.WriteByte(c)
		} else if c == '\t' {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
