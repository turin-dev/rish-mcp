package release

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func buildTestApkBytes(t *testing.T, versionCode int, versionName string) []byte {
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

// fakeGitHub stands in for the GitHub Releases API + asset CDN: one handler
// serves /repos/<repo>/releases/latest, another serves the .apk bytes at a
// URL that handler points to.
func fakeGitHub(t *testing.T, tag string, apkBytes []byte) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/test/repo/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": tag,
			"assets": []map[string]string{
				{"name": "agent.apk", "browser_download_url": srv.URL + "/download/agent.apk"},
			},
		})
	})
	mux.HandleFunc("/download/agent.apk", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(apkBytes)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestSourceFetchesAndCachesRelease(t *testing.T) {
	apkBytes := buildTestApkBytes(t, 3, "1.0.0")
	srv := fakeGitHub(t, "v1.0.0", apkBytes)

	src := NewSource(SourceOptions{
		Repo:      "test/repo",
		CacheDir:  t.TempDir(),
		APIBase:   srv.URL,
		PollEvery: time.Hour,
	})

	src.refresh(context.Background())

	rel := src.Get()
	if rel == nil {
		t.Fatal("expected a release after refresh, got nil")
	}
	if rel.Tag != "v1.0.0" {
		t.Errorf("Tag = %q, want v1.0.0", rel.Tag)
	}
	if rel.VersionName != "1.0.0" {
		t.Errorf("VersionName = %q, want 1.0.0", rel.VersionName)
	}
	if rel.VersionCode != 3 {
		t.Errorf("VersionCode = %d, want 3", rel.VersionCode)
	}

	// A second refresh with the same tag is a no-op — same release, unchanged.
	src.refresh(context.Background())
	if rel2 := src.Get(); rel2.Tag != rel.Tag || rel2.SHA256 != rel.SHA256 {
		t.Fatalf("redundant refresh changed the release: %+v -> %+v", rel, rel2)
	}
}

func TestSourceKeepsPreviousReleaseOnBadDownload(t *testing.T) {
	goodAPK := buildTestApkBytes(t, 1, "1.0.0")
	srv := fakeGitHub(t, "v1.0.0", goodAPK)

	src := NewSource(SourceOptions{Repo: "test/repo", CacheDir: t.TempDir(), APIBase: srv.URL, PollEvery: time.Hour})
	src.refresh(context.Background())
	good := src.Get()
	if good == nil || good.VersionName != "1.0.0" {
		t.Fatalf("setup: expected the good release to be cached first, got %+v", good)
	}

	// Now GitHub reports a new tag whose "apk" is garbage.
	badSrv := fakeGitHub(t, "v2.0.0", []byte("not an apk"))
	src.opts.APIBase = badSrv.URL
	src.refresh(context.Background())

	after := src.Get()
	if after.Tag != good.Tag || after.VersionName != good.VersionName {
		t.Fatalf("a bad download must not replace the working release: had %+v, now %+v", good, after)
	}
}

func TestSourceLocalAPKOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.apk")
	if err := os.WriteFile(path, buildTestApkBytes(t, 9, "9.9.9"), 0o644); err != nil {
		t.Fatalf("write local apk: %v", err)
	}

	src := NewSource(SourceOptions{LocalAPK: path})
	rel := src.Get()
	if rel == nil {
		t.Fatal("expected a release from LocalAPK, got nil")
	}
	if rel.Tag != "local" || rel.VersionName != "9.9.9" || rel.VersionCode != 9 {
		t.Errorf("unexpected release from LocalAPK: %+v", rel)
	}
}

func TestSourceLoadsCacheWithoutNetwork(t *testing.T) {
	apkBytes := buildTestApkBytes(t, 5, "5.0.0")
	srv := fakeGitHub(t, "v5.0.0", apkBytes)
	cacheDir := t.TempDir()

	first := NewSource(SourceOptions{Repo: "test/repo", CacheDir: cacheDir, APIBase: srv.URL, PollEvery: time.Hour})
	first.refresh(context.Background())
	if first.Get() == nil {
		t.Fatal("setup: expected a cached release")
	}

	// A fresh Source pointed at an unreachable API must still serve the
	// on-disk cache once loadCache() runs (what Start() does before polling).
	second := NewSource(SourceOptions{Repo: "test/repo", CacheDir: cacheDir, APIBase: "http://127.0.0.1:0", PollEvery: time.Hour})
	second.loadCache()

	rel := second.Get()
	if rel == nil {
		t.Fatal("expected loadCache to restore the on-disk release, got nil")
	}
	if rel.VersionName != "5.0.0" {
		t.Errorf("VersionName = %q, want 5.0.0", rel.VersionName)
	}
}

// --- tmp-file cleanup tests (Task #8 / #9) ---------------------------------

func TestRemoveStaleTmp(t *testing.T) {
	t.Run("removes existing tmp file", func(t *testing.T) {
		dir := t.TempDir()
		opts := SourceOptions{CacheDir: dir}
		s := NewSource(opts)

		tmpPath := filepath.Join(dir, "agent.apk.tmp")
		if err := os.WriteFile(tmpPath, []byte("leftover"), 0o644); err != nil {
			t.Fatalf("write tmp: %v", err)
		}

		s.removeStaleTmp()

		if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
			t.Fatal("expected tmp file to be removed")
		}
	})

	t.Run("no tmp file is a no-op", func(t *testing.T) {
		dir := t.TempDir()
		opts := SourceOptions{CacheDir: dir}
		s := NewSource(opts)

		// Just must not panic or error.
		s.removeStaleTmp()
	})

	t.Run("cache dir missing is a no-op", func(t *testing.T) {
		dir := t.TempDir()
		nonExistent := filepath.Join(dir, "nonexistent")
		s := NewSource(SourceOptions{CacheDir: nonExistent})

		// Must not panic or error.
		s.removeStaleTmp()
	})

	t.Run("remove error other than not-exist is logged", func(t *testing.T) {
		var buf bytes.Buffer
		old := log.Writer()
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(old) })

		dir := t.TempDir()
		opts := SourceOptions{CacheDir: dir}
		s := NewSource(opts)
		// Make the tmp path a non-empty directory — os.Remove on a non-empty
		// directory returns ENOTEMPTY even when running as root, which
		// exercises the error branch.
		tmpPath := filepath.Join(dir, "agent.apk.tmp")
		if err := os.MkdirAll(filepath.Join(tmpPath, "subdir"), 0o755); err != nil {
			t.Fatalf("mkdir tmp dir: %v", err)
		}

		s.removeStaleTmp()

		if !strings.Contains(buf.String(), "failed to clean up stale tmp file") {
			t.Fatalf("expected log about failed cleanup, got: %s", buf.String())
		}
	})
}

func TestDownloadAndPublishCleansUpTmpOnFailure(t *testing.T) {
	t.Run("bad APK bytes removes tmp file", func(t *testing.T) {
		ctx := context.Background()
		dir := t.TempDir()
		s := NewSource(SourceOptions{CacheDir: dir})

		// Use a local HTTP server that serves non-APK bytes.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("not an apk at all"))
		}))
		defer srv.Close()

		// There should be no tmp file before the call.
		tmpPath := filepath.Join(dir, "agent.apk.tmp")
		if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
			t.Fatal("expected no tmp file before download")
		}

		err := s.downloadAndPublish(ctx, srv.URL+"/bad.apk", "v1.0.0")
		if err == nil {
			t.Fatal("expected downloadAndPublish to fail with bad APK bytes")
		}

		// After the failure, the tmp file must be gone.
		if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
			t.Fatal("expected tmp file to be cleaned up after failed download")
		}
	})

	t.Run("rename failure removes tmp file", func(t *testing.T) {
		ctx := context.Background()
		dir := t.TempDir()
		s := NewSource(SourceOptions{CacheDir: dir})

		// On Linux os.Rename overwrites a read-only target file, so make
		// agent.apk a non-empty directory instead — renaming a file onto a
		// directory fails with EISDIR/ENOTDIR, exercising the rename-error path.
		apkPath := filepath.Join(dir, "agent.apk")
		if err := os.Mkdir(apkPath, 0o755); err != nil {
			t.Fatalf("mkdir agent.apk: %v", err)
		}
		if err := os.WriteFile(filepath.Join(apkPath, "stub"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write stub: %v", err)
		}

		// Serve valid APK bytes.
		apkBytes := buildTestApkBytes(t, 1, "1.0.0")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(apkBytes)
		}))
		defer srv.Close()

		tmpPath := filepath.Join(dir, "agent.apk.tmp")
		if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
			t.Fatal("expected no tmp file before download")
		}

		err := s.downloadAndPublish(ctx, srv.URL+"/agent.apk", "v1.0.0")
		if err == nil {
			t.Fatal("expected downloadAndPublish to fail (rename onto non-empty directory)")
		}

		// After the failure, the tmp file must be removed.
		if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
			t.Fatal("expected tmp file to be cleaned up after failed rename")
		}
	})
}

// --- SourceOptionsFromEnv / envOr coverage ---------------------------------

func TestSourceOptionsFromEnvDefaults(t *testing.T) {
	opts := SourceOptionsFromEnv()
	if opts.Repo != "turin-dev/rish-mcp" {
		t.Errorf("Repo = %q, want turin-dev/rish-mcp", opts.Repo)
	}
	if opts.CacheDir != "/var/cache/rish-mcp" {
		t.Errorf("CacheDir = %q, want /var/cache/rish-mcp", opts.CacheDir)
	}
	if opts.APIBase != "https://api.github.com" {
		t.Errorf("APIBase = %q, want https://api.github.com", opts.APIBase)
	}
	if opts.PollEvery != 15*time.Minute {
		t.Errorf("PollEvery = %v, want 15m", opts.PollEvery)
	}
	if opts.LocalAPK != "" {
		t.Errorf("LocalAPK = %q, want empty", opts.LocalAPK)
	}
}

func TestSourceOptionsFromEnvCustomValues(t *testing.T) {
	t.Setenv("GITHUB_REPO", "my/custom-repo")
	t.Setenv("RELEASE_CACHE_DIR", "/tmp/custom-cache")
	t.Setenv("GITHUB_API_BASE", "https://my-gh-api.example.com")
	t.Setenv("RELEASE_POLL_MS", "5000")
	t.Setenv("APK_PATH", "/tmp/test.apk")

	opts := SourceOptionsFromEnv()
	if opts.Repo != "my/custom-repo" {
		t.Errorf("Repo = %q", opts.Repo)
	}
	if opts.CacheDir != "/tmp/custom-cache" {
		t.Errorf("CacheDir = %q", opts.CacheDir)
	}
	if opts.APIBase != "https://my-gh-api.example.com" {
		t.Errorf("APIBase = %q", opts.APIBase)
	}
	if opts.PollEvery != 5*time.Second {
		t.Errorf("PollEvery = %v, want 5s", opts.PollEvery)
	}
	if opts.LocalAPK != "/tmp/test.apk" {
		t.Errorf("LocalAPK = %q", opts.LocalAPK)
	}
}

func TestSourceOptionsFromEnvInvalidPollMs(t *testing.T) {
	t.Setenv("RELEASE_POLL_MS", "not-a-number")
	opts := SourceOptionsFromEnv()
	if opts.PollEvery != 15*time.Minute {
		t.Errorf("PollEvery = %v, want 15m (default after parse failure)", opts.PollEvery)
	}
}

// --- Start coverage -------------------------------------------------------

func TestStartLocalAPKMode(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })

	dir := t.TempDir()
	apkPath := filepath.Join(dir, "local.apk")
	if err := os.WriteFile(apkPath, buildTestApkBytes(t, 42, "42.0.0"), 0o644); err != nil {
		t.Fatalf("write apk: %v", err)
	}

	src := NewSource(SourceOptions{LocalAPK: apkPath})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src.Start(ctx)

	rel := src.Get()
	if rel == nil {
		t.Fatal("expected local APK release, got nil")
	}
	if rel.Tag != "local" {
		t.Errorf("Tag = %q, want local", rel.Tag)
	}
	if rel.VersionName != "42.0.0" {
		t.Errorf("VersionName = %q, want 42.0.0", rel.VersionName)
	}
	if !strings.Contains(buf.String(), "serving local APK_PATH=") {
		t.Fatalf("expected log about local APK, got: %s", buf.String())
	}
}

func TestStartPollsAndStops(t *testing.T) {
	apkBytes := buildTestApkBytes(t, 7, "7.0.0")

	// Count requests so we can verify polling stops after cancel.
	var (
		mu         sync.Mutex
		reqCount   int
	)
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/test/repo/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqCount++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v7.0.0",
			"assets": []map[string]string{
				{"name": "agent.apk", "browser_download_url": srv.URL + "/download/agent.apk"},
			},
		})
	})
	mux.HandleFunc("/download/agent.apk", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(apkBytes)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	src := NewSource(SourceOptions{
		Repo:      "test/repo",
		CacheDir:  t.TempDir(),
		APIBase:   srv.URL,
		PollEvery: 50 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src.Start(ctx)

	// Wait for the initial refresh to complete.
	var rel *Release
	for i := 0; i < 20; i++ {
		rel = src.Get()
		if rel != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rel == nil {
		t.Fatal("expected a release after Start, got nil")
	}
	if rel.Tag != "v7.0.0" {
		t.Errorf("Tag = %q, want v7.0.0", rel.Tag)
	}
	if rel.VersionName != "7.0.0" {
		t.Errorf("VersionName = %q, want 7.0.0", rel.VersionName)
	}

	// Record count before the ticker-driven poll window.
	mu.Lock()
	before := reqCount
	mu.Unlock()

	// Stay alive past at least one poll tick so the select loop's ticker.C
	// branch actually runs (PollEvery=50ms; 120ms covers two ticks even under
	// scheduler jitter). The tick's refresh() finds the same tag and no-ops,
	// but the request counter still increments.
	time.Sleep(120 * time.Millisecond)

	mu.Lock()
	after := reqCount
	mu.Unlock()
	if after <= before {
		t.Fatal("ticker.C branch did not fire: no requests after initial refresh")
	}

	cancel()
	// After cancel, wait a full tick window and verify no new requests arrive.
	time.Sleep(150 * time.Millisecond)

	mu.Lock()
	postCancel := reqCount
	mu.Unlock()
	if postCancel != after {
		t.Fatalf("polling did not stop after cancel: %d -> %d requests", after, postCancel)
	}
}

// --- Get edge cases -------------------------------------------------------

func TestGetLocalAPKFailure(t *testing.T) {
	src := NewSource(SourceOptions{LocalAPK: "/nonexistent/path.apk"})
	rel := src.Get()
	if rel != nil {
		t.Fatalf("expected nil for missing LocalAPK, got %+v", rel)
	}
}

// --- loadCache edge cases -------------------------------------------------

func TestLoadCacheVariants(t *testing.T) {
	t.Run("missing meta file is a no-op", func(t *testing.T) {
		dir := t.TempDir()
		apkPath := filepath.Join(dir, "agent.apk")
		if err := os.WriteFile(apkPath, buildTestApkBytes(t, 1, "1.0.0"), 0o644); err != nil {
			t.Fatalf("write apk: %v", err)
		}
		src := NewSource(SourceOptions{CacheDir: dir})
		src.loadCache()
		if rel := src.Get(); rel != nil {
			t.Fatal("expected nil when meta file is missing")
		}
	})

	t.Run("corrupt meta JSON is a no-op", func(t *testing.T) {
		dir := t.TempDir()
		apkPath := filepath.Join(dir, "agent.apk")
		if err := os.WriteFile(apkPath, buildTestApkBytes(t, 1, "1.0.0"), 0o644); err != nil {
			t.Fatalf("write apk: %v", err)
		}
		metaPath := filepath.Join(dir, "release.json")
		if err := os.WriteFile(metaPath, []byte("not json"), 0o644); err != nil {
			t.Fatalf("write meta: %v", err)
		}
		src := NewSource(SourceOptions{CacheDir: dir})
		src.loadCache()
		if rel := src.Get(); rel != nil {
			t.Fatal("expected nil when meta is corrupt")
		}
	})

	t.Run("valid meta but unusable APK logs warning", func(t *testing.T) {
		var buf bytes.Buffer
		old := log.Writer()
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(old) })

		dir := t.TempDir()
		apkPath := filepath.Join(dir, "agent.apk")
		if err := os.WriteFile(apkPath, []byte("not an apk"), 0o644); err != nil {
			t.Fatalf("write apk: %v", err)
		}
		metaPath := filepath.Join(dir, "release.json")
		metaBytes, _ := json.Marshal(map[string]string{"tag": "v1.0.0"})
		if err := os.WriteFile(metaPath, metaBytes, 0o644); err != nil {
			t.Fatalf("write meta: %v", err)
		}

		src := NewSource(SourceOptions{CacheDir: dir})
		src.loadCache()
		if rel := src.Get(); rel != nil {
			t.Fatal("expected nil when APK is unusable")
		}
		if !strings.Contains(buf.String(), "ignoring unusable cache") {
			t.Fatalf("expected log about unusable cache, got: %s", buf.String())
		}
	})

	t.Run("tag empty defaults to unknown", func(t *testing.T) {
		dir := t.TempDir()
		apkPath := filepath.Join(dir, "agent.apk")
		if err := os.WriteFile(apkPath, buildTestApkBytes(t, 2, "2.0.0"), 0o644); err != nil {
			t.Fatalf("write apk: %v", err)
		}
		metaPath := filepath.Join(dir, "release.json")
		metaBytes, _ := json.Marshal(map[string]string{"tag": ""})
		if err := os.WriteFile(metaPath, metaBytes, 0o644); err != nil {
			t.Fatalf("write meta: %v", err)
		}

		src := NewSource(SourceOptions{CacheDir: dir})
		src.loadCache()
		rel := src.Get()
		if rel == nil {
			t.Fatal("expected a release, got nil")
		}
		if rel.Tag != "unknown" {
			t.Errorf("Tag = %q, want unknown", rel.Tag)
		}
	})
}

// --- refresh failure paths ------------------------------------------------

func TestRefreshFailurePaths(t *testing.T) {
	t.Run("empty tag_name", func(t *testing.T) {
		var buf bytes.Buffer
		old := log.Writer()
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(old) })

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "",
				"assets": []map[string]string{
					{"name": "agent.apk", "browser_download_url": "http://example.com/agent.apk"},
				},
			})
		}))
		defer srv.Close()

		src := NewSource(SourceOptions{Repo: "test/repo", CacheDir: t.TempDir(), APIBase: srv.URL, PollEvery: time.Hour})
		src.refresh(context.Background())
		if !strings.Contains(buf.String(), "release has no tag_name") {
			t.Fatalf("expected 'no tag_name' log, got: %s", buf.String())
		}
	})

	t.Run("no apk asset", func(t *testing.T) {
		var buf bytes.Buffer
		old := log.Writer()
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(old) })

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v1.0.0",
				"assets": []map[string]string{
					{"name": "agent.exe", "browser_download_url": "http://example.com/agent.exe"},
				},
			})
		}))
		defer srv.Close()

		src := NewSource(SourceOptions{Repo: "test/repo", CacheDir: t.TempDir(), APIBase: srv.URL, PollEvery: time.Hour})
		src.refresh(context.Background())
		if !strings.Contains(buf.String(), "no .apk asset") {
			t.Fatalf("expected 'no .apk asset' log, got: %s", buf.String())
		}
	})

	t.Run("http error from API", func(t *testing.T) {
		var buf bytes.Buffer
		old := log.Writer()
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(old) })

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		src := NewSource(SourceOptions{Repo: "test/repo", CacheDir: t.TempDir(), APIBase: srv.URL, PollEvery: time.Hour})
		src.refresh(context.Background())
		if !strings.Contains(buf.String(), "refresh failed") {
			t.Fatalf("expected 'refresh failed' log, got: %s", buf.String())
		}
	})

	t.Run("json decode error", func(t *testing.T) {
		var buf bytes.Buffer
		old := log.Writer()
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(old) })

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("not json"))
		}))
		defer srv.Close()

		src := NewSource(SourceOptions{Repo: "test/repo", CacheDir: t.TempDir(), APIBase: srv.URL, PollEvery: time.Hour})
		src.refresh(context.Background())
		if !strings.Contains(buf.String(), "refresh failed") {
			t.Fatalf("expected 'refresh failed' log, got: %s", buf.String())
		}
	})
}

// --- fetchJSON error paths -------------------------------------------------

func TestFetchJSONErrors(t *testing.T) {
	src := NewSource(SourceOptions{})

	t.Run("bad url", func(t *testing.T) {
		_, err := src.fetchJSON(context.Background(), "://bad", time.Second)
		if err == nil {
			t.Fatal("expected error for malformed URL")
		}
	})

	t.Run("connection refused", func(t *testing.T) {
		_, err := src.fetchJSON(context.Background(), "http://127.0.0.1:1/repos/test/repo/releases/latest", time.Second)
		if err == nil {
			t.Fatal("expected error when connection is refused")
		}
	})
}

// --- downloadAndPublish error paths -----------------------------------------

func TestDownloadAndPublishErrors(t *testing.T) {
	t.Run("bad url", func(t *testing.T) {
		src := NewSource(SourceOptions{})
		if err := src.downloadAndPublish(context.Background(), "://bad", "v1.0.0"); err == nil {
			t.Fatal("expected error for malformed URL")
		}
	})

	t.Run("connection refused", func(t *testing.T) {
		src := NewSource(SourceOptions{})
		if err := src.downloadAndPublish(context.Background(), "http://127.0.0.1:1/agent.apk", "v1.0.0"); err == nil {
			t.Fatal("expected error when connection is refused")
		}
	})

	t.Run("non-200 status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer srv.Close()

		src := NewSource(SourceOptions{})
		err := src.downloadAndPublish(context.Background(), srv.URL+"/agent.apk", "v1.0.0")
		if err == nil {
			t.Fatal("expected error for non-200 download")
		}
		if !strings.Contains(err.Error(), "download -> 404") {
			t.Fatalf("expected 'download -> 404' in error, got: %v", err)
		}
	})

	t.Run("truncated body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1000")
			_, _ = w.Write([]byte("short body"))
		}))
		defer srv.Close()

		src := NewSource(SourceOptions{})
		err := src.downloadAndPublish(context.Background(), srv.URL+"/agent.apk", "v1.0.0")
		if err == nil {
			t.Fatal("expected error when body is truncated")
		}
	})

	t.Run("cache dir is a file", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(buildTestApkBytes(t, 3, "1.0.0"))
		}))
		defer srv.Close()

		src := NewSource(SourceOptions{CacheDir: blocker})
		if err := src.downloadAndPublish(context.Background(), srv.URL+"/agent.apk", "v1.0.0"); err == nil {
			t.Fatal("expected error when the cache dir cannot be created")
		}
	})

	t.Run("tmp path is a directory", func(t *testing.T) {
		cacheDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(cacheDir, "agent.apk.tmp"), 0o755); err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(buildTestApkBytes(t, 3, "1.0.0"))
		}))
		defer srv.Close()

		src := NewSource(SourceOptions{CacheDir: cacheDir})
		if err := src.downloadAndPublish(context.Background(), srv.URL+"/agent.apk", "v1.0.0"); err == nil {
			t.Fatal("expected error when the tmp path is an existing directory")
		}
	})
}

func TestDownloadAndPublishVersionMismatch(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })

	apkBytes := buildTestApkBytes(t, 3, "1.0.0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(apkBytes)
	}))
	defer srv.Close()

	src := NewSource(SourceOptions{CacheDir: t.TempDir()})
	if err := src.downloadAndPublish(context.Background(), srv.URL+"/agent.apk", "v2.0.0"); err != nil {
		t.Fatalf("downloadAndPublish: %v", err)
	}
	if !strings.Contains(buf.String(), "does not match APK versionName") {
		t.Fatalf("expected version-mismatch log, got: %s", buf.String())
	}
	rel := src.Get()
	if rel == nil {
		t.Fatal("expected a release after download")
	}
	if rel.Tag != "v2.0.0" {
		t.Errorf("Tag = %q, want v2.0.0", rel.Tag)
	}
	if rel.VersionName != "1.0.0" {
		t.Errorf("VersionName = %q, want 1.0.0", rel.VersionName)
	}
}