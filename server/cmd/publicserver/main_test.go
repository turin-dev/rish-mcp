package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/turin-dev/rish-mcp/server/internal/release"
)

func TestPublicServerServesReleaseAndApk(t *testing.T) {
	src := release.NewSource(release.SourceOptionsFromEnv())
	// No LocalAPK/GitHub reachable in this test: exercise the "not ready yet" paths.
	mux := newMux(src, 30)
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

func TestPublicServerDoesNotExposeRelayRoutes(t *testing.T) {
	src := release.NewSource(release.SourceOptionsFromEnv())
	mux := newMux(src, 30)
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

// TestSourceStartStop confirms Start()/cancel() against an unreachable API
// doesn't hang or panic the background poller.
func TestSourceStartStop(t *testing.T) {
	src := release.NewSource(release.SourceOptions{APIBase: "http://127.0.0.1:0", PollEvery: time.Hour, CacheDir: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	src.Start(ctx)
	cancel()
}
