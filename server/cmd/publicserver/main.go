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
	"sync"
	"syscall"
	"time"

	"github.com/turin-dev/rish-mcp/server/internal/release"
)

func main() {
	port := envOr("PORT", "8080")
	downloadsPerHour := envInt("DOWNLOADS_PER_HOUR", 30)

	src := release.NewSource(release.SourceOptionsFromEnv())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src.Start(ctx)

	mux := newMux(src, downloadsPerHour)

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

func newMux(src *release.Source, downloadsPerHour int) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthzHandler(src))
	mux.HandleFunc("GET /api/version/release", versionHandler(src))
	mux.HandleFunc("GET /agent.apk", agentApkHandler(src, newRateLimiter(downloadsPerHour)))
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

func agentApkHandler(src *release.Source, limiter *rateLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rel := src.Get()
		if rel == nil {
			http.Error(w, "apk not available", http.StatusServiceUnavailable)
			return
		}
		if limiter.limited(clientIP(r)) {
			http.Error(w, "too many downloads; try again later", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("X-Apk-Version", rel.VersionName)
		w.Header().Set("X-Apk-Version-Code", strconv.Itoa(rel.VersionCode))
		w.Header().Set("X-Apk-Sha256", rel.SHA256)
		w.Header().Set("Content-Disposition", `attachment; filename="rish-mcp-agent-`+rel.VersionName+`.apk"`)
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
	perHour int
	mu      sync.Mutex
	hits    map[string]rateEntry
}

func newRateLimiter(perHour int) *rateLimiter {
	return &rateLimiter{perHour: perHour, hits: make(map[string]rateEntry)}
}

func (l *rateLimiter) limited(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now().Unix()
	e, ok := l.hits[ip]
	if !ok || e.resetAt < now {
		l.hits[ip] = rateEntry{count: 1, resetAt: now + 3600}
		if len(l.hits) > 10_000 {
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

func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return fwd
	}
	// RemoteAddr is "ip:port"; strip the port so repeated requests from the
	// same client (a fresh ephemeral port each time) share one rate bucket.
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
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
