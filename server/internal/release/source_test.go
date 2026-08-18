package release

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func writeTestCachePair(t *testing.T, src *Source, versionCode int, versionName string) {
	t.Helper()
	if err := os.MkdirAll(src.opts.CacheDir, 0o755); err != nil {
		t.Fatalf("create cache dir: %v", err)
	}
	if err := os.WriteFile(src.apkFile(), buildTestApkBytes(t, versionCode, versionName), 0o644); err != nil {
		t.Fatalf("write cached APK: %v", err)
	}
	meta, err := json.Marshal(map[string]string{"tag": src.opts.TagPrefix + versionName})
	if err != nil {
		t.Fatalf("encode cache metadata: %v", err)
	}
	if err := os.WriteFile(src.metaFile(), meta, 0o644); err != nil {
		t.Fatalf("write cache metadata: %v", err)
	}
}

func writeTestImmutableAPK(t *testing.T, src *Source, versionCode int, versionName string) string {
	t.Helper()
	if err := os.MkdirAll(src.opts.CacheDir, 0o755); err != nil {
		t.Fatalf("create cache dir: %v", err)
	}
	data := buildTestApkBytes(t, versionCode, versionName)
	info, err := parseApk(data)
	if err != nil {
		t.Fatalf("parse test APK: %v", err)
	}
	path := filepath.Join(src.opts.CacheDir, immutableAPKName(info))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write immutable test APK: %v", err)
	}
	return path
}

func writeTestImmutableCachePair(t *testing.T, src *Source, versionCode int, versionName string) string {
	t.Helper()
	apkPath := writeTestImmutableAPK(t, src, versionCode, versionName)
	meta, err := json.Marshal(cacheMetadata{
		Tag: src.opts.TagPrefix + versionName,
		APK: filepath.Base(apkPath),
	})
	if err != nil {
		t.Fatalf("encode cache metadata: %v", err)
	}
	if err := os.WriteFile(src.metaFile(), meta, 0o644); err != nil {
		t.Fatalf("write cache metadata: %v", err)
	}
	return apkPath
}

// fakeGitHub stands in for the GitHub Releases API + asset CDN: one handler
// serves /repos/<repo>/releases, another serves the .apk bytes at a URL that
// handler points to.
func fakeGitHub(t *testing.T, tag string, apkBytes []byte) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/test/repo/releases", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("per_page"); got != "100" {
			t.Errorf("per_page = %q, want 100", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"tag_name": tag,
			"assets": []map[string]string{
				{"name": "agent.apk", "browser_download_url": srv.URL + "/download/agent.apk"},
			},
		}})
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
	srv := fakeGitHub(t, "agent-v1.0.0", apkBytes)

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
	if rel.Tag != "agent-v1.0.0" {
		t.Errorf("Tag = %q, want agent-v1.0.0", rel.Tag)
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

func TestSourceUpgradesExistingCache(t *testing.T) {
	cacheDir := t.TempDir()
	v1Server := fakeGitHub(t, "agent-v1.0.0", buildTestApkBytes(t, 1, "1.0.0"))
	src := NewSource(SourceOptions{Repo: "test/repo", CacheDir: cacheDir, APIBase: v1Server.URL, PollEvery: time.Hour})
	src.refresh(context.Background())
	v1 := src.Get()
	if v1 == nil || v1.Tag != "agent-v1.0.0" {
		t.Fatalf("setup: expected agent-v1.0.0, got %+v", v1)
	}

	v2Server := fakeGitHub(t, "agent-v2.0.0", buildTestApkBytes(t, 2, "2.0.0"))
	src.opts.APIBase = v2Server.URL
	src.refresh(context.Background())

	v2 := src.Get()
	if v2 == nil || v2.Tag != "agent-v2.0.0" || v2.VersionName != "2.0.0" || v2.VersionCode != 2 {
		t.Fatalf("upgraded release = %+v, want agent-v2.0.0", v2)
	}
	if v2.SHA256 == v1.SHA256 {
		t.Fatal("upgrade retained the previous APK bytes")
	}
	if v2.Path == v1.Path {
		t.Fatal("upgrade reused a mutable APK path")
	}
	for _, want := range []*Release{v1, v2} {
		diskInfo, err := ReadApkInfo(want.Path)
		if err != nil {
			t.Fatalf("read immutable APK %s: %v", want.Path, err)
		}
		if diskInfo.VersionName != want.VersionName || diskInfo.VersionCode != want.VersionCode || diskInfo.SHA256 != want.SHA256 {
			t.Fatalf("immutable APK disagrees with release pointer: disk=%+v release=%+v", diskInfo, want)
		}
	}
	metaBytes, err := os.ReadFile(filepath.Join(cacheDir, "release.json"))
	if err != nil {
		t.Fatalf("read upgraded metadata: %v", err)
	}
	var meta cacheMetadata
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatalf("decode upgraded metadata: %v", err)
	}
	if meta.Tag != "agent-v2.0.0" {
		t.Fatalf("metadata tag = %q, want agent-v2.0.0", meta.Tag)
	}
	if meta.APK != filepath.Base(v2.Path) {
		t.Fatalf("metadata APK = %q, want %q", meta.APK, filepath.Base(v2.Path))
	}
	for _, path := range []string{
		filepath.Join(cacheDir, "agent.apk.tmp"),
		filepath.Join(cacheDir, "release.json.tmp"),
		filepath.Join(cacheDir, "release.json.bak"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("transaction artifact %s remains after upgrade: %v", path, err)
		}
	}
}

func TestSourceNeverDowngradesByReleaseCreationOrder(t *testing.T) {
	cacheDir := t.TempDir()
	v2Server := fakeGitHub(t, "agent-v2.0.0", buildTestApkBytes(t, 20, "2.0.0"))
	src := NewSource(SourceOptions{Repo: "test/repo", CacheDir: cacheDir, APIBase: v2Server.URL, PollEvery: time.Hour})
	src.refresh(context.Background())
	good := src.Get()
	if good == nil {
		t.Fatal("setup: expected agent-v2.0.0")
	}

	var (
		srv           *httptest.Server
		downloadMu    sync.Mutex
		downloadCount int
	)
	lowerAPK := buildTestApkBytes(t, 19, "1.9.9")
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/test/repo/releases", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"tag_name": "agent-v1.9.9",
			"assets":   []map[string]string{{"name": "agent.apk", "browser_download_url": srv.URL + "/download/agent.apk"}},
		}})
	})
	mux.HandleFunc("/download/agent.apk", func(w http.ResponseWriter, r *http.Request) {
		downloadMu.Lock()
		downloadCount++
		downloadMu.Unlock()
		_, _ = w.Write(lowerAPK)
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	src.opts.APIBase = srv.URL
	src.refresh(context.Background())
	after := src.Get()
	if after == nil || after.Tag != good.Tag || after.SHA256 != good.SHA256 {
		t.Fatalf("lower tag replaced current release: had %+v, now %+v", good, after)
	}
	downloadMu.Lock()
	defer downloadMu.Unlock()
	if downloadCount != 0 {
		t.Fatalf("downgrade asset was downloaded %d time(s)", downloadCount)
	}
}

func TestSourceRejectsVersionCodeRegression(t *testing.T) {
	cacheDir := t.TempDir()
	v1Server := fakeGitHub(t, "agent-v1.0.0", buildTestApkBytes(t, 10, "1.0.0"))
	src := NewSource(SourceOptions{Repo: "test/repo", CacheDir: cacheDir, APIBase: v1Server.URL, PollEvery: time.Hour})
	src.refresh(context.Background())
	good := src.Get()
	if good == nil {
		t.Fatal("setup: expected agent-v1.0.0")
	}

	// The semantic version advances, but Android would see this as a downgrade
	// because versionCode decreased.
	v2Server := fakeGitHub(t, "agent-v2.0.0", buildTestApkBytes(t, 9, "2.0.0"))
	src.opts.APIBase = v2Server.URL
	src.refresh(context.Background())
	after := src.Get()
	if after == nil || after.Tag != good.Tag || after.SHA256 != good.SHA256 {
		t.Fatalf("versionCode regression replaced current release: had %+v, now %+v", good, after)
	}
}

func TestSourceSelectsHighestStableReleaseInTagChannel(t *testing.T) {
	compatibleAPK := buildTestApkBytes(t, 1, "0.1.0")
	var (
		srv       *httptest.Server
		mu        sync.Mutex
		downloads []string
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/test/repo/releases", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("per_page"); got != "100" {
			t.Errorf("per_page = %q, want 100", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				// GitHub's implicit latest endpoint would return this newer
				// legacy release. It must never enter the rewrite channel.
				"tag_name": "v0.5",
				"assets":   []map[string]string{{"name": "legacy.apk", "browser_download_url": srv.URL + "/download/legacy.apk"}},
			},
			{
				// A lower compatible version created later may appear first in
				// the API. Semantic version order must win over list order.
				"tag_name": "agent-v0.0.9",
				"assets":   []map[string]string{{"name": "lower.apk", "browser_download_url": srv.URL + "/download/lower.apk"}},
			},
			{
				"tag_name": "agent-v0.3.0",
				"draft":    true,
				"assets":   []map[string]string{{"name": "draft.apk", "browser_download_url": srv.URL + "/download/draft.apk"}},
			},
			{
				"tag_name":   "agent-v0.2.0",
				"prerelease": true,
				"assets":     []map[string]string{{"name": "prerelease.apk", "browser_download_url": srv.URL + "/download/prerelease.apk"}},
			},
			{
				// A compatible release without an APK must not prevent a
				// last-good compatible release farther down the list.
				"tag_name": "agent-v0.1.1",
				"assets":   []map[string]string{{"name": "checksums.txt", "browser_download_url": srv.URL + "/download/checksums.txt"}},
			},
			{
				"tag_name": "agent-v0.1.0",
				"assets":   []map[string]string{{"name": "agent.apk", "browser_download_url": srv.URL + "/download/agent.apk"}},
			},
		})
	})
	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		downloads = append(downloads, r.URL.Path)
		mu.Unlock()
		if r.URL.Path != "/download/agent.apk" {
			http.Error(w, "unexpected download", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(compatibleAPK)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	src := NewSource(SourceOptions{
		Repo:      "test/repo",
		CacheDir:  t.TempDir(),
		APIBase:   srv.URL,
		PollEvery: time.Hour,
	})
	src.refresh(context.Background())

	rel := src.Get()
	if rel == nil {
		t.Fatal("expected a compatible release after refresh, got nil")
	}
	if rel.Tag != "agent-v0.1.0" || rel.VersionName != "0.1.0" {
		t.Fatalf("selected release = %+v, want agent-v0.1.0", rel)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(downloads) != 1 || downloads[0] != "/download/agent.apk" {
		t.Fatalf("downloads = %v, want only the compatible stable APK", downloads)
	}
}

func TestSourceScansLaterReleasePagesBeforeSelectingHighest(t *testing.T) {
	var (
		srv          *httptest.Server
		pageRequests int
		downloads    int
	)
	fullFirstPage := make([]map[string]any, githubReleasePageSize)
	for i := range fullFirstPage {
		// These creation-newer legacy entries fill page 1 but are outside the
		// rewrite channel. The compatible maximum exists only on page 2.
		fullFirstPage[i] = map[string]any{"tag_name": "v0.5.0"}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/test/repo/releases", func(w http.ResponseWriter, r *http.Request) {
		pageRequests++
		if got := r.URL.Query().Get("per_page"); got != "100" {
			t.Errorf("per_page = %q, want 100", got)
		}
		switch r.URL.Query().Get("page") {
		case "1":
			_ = json.NewEncoder(w).Encode(fullFirstPage)
		case "2":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"tag_name": "agent-v2.0.0",
				"assets": []map[string]string{{
					"name":                 "agent.apk",
					"browser_download_url": srv.URL + "/download/agent.apk",
				}},
			}})
		default:
			t.Errorf("unexpected release page %q", r.URL.Query().Get("page"))
			_ = json.NewEncoder(w).Encode([]any{})
		}
	})
	mux.HandleFunc("/download/agent.apk", func(w http.ResponseWriter, r *http.Request) {
		downloads++
		_, _ = w.Write(buildTestApkBytes(t, 20, "2.0.0"))
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	src := NewSource(SourceOptions{Repo: "test/repo", CacheDir: t.TempDir(), APIBase: srv.URL, PollEvery: time.Hour})
	src.refresh(context.Background())
	rel := src.Get()
	if rel == nil || rel.Tag != "agent-v2.0.0" {
		t.Fatalf("selected release = %+v, want page-2 agent-v2.0.0", rel)
	}
	if pageRequests != 2 || downloads != 1 {
		t.Fatalf("page requests/downloads = %d/%d, want 2/1", pageRequests, downloads)
	}
}

func TestSourceRefusesPartialReleaseScanAtSafetyLimit(t *testing.T) {
	fullPage := make([]map[string]any, githubReleasePageSize)
	for i := range fullPage {
		fullPage[i] = map[string]any{"tag_name": "agent-v1.0.0"}
	}
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_ = json.NewEncoder(w).Encode(fullPage)
	}))
	defer srv.Close()

	src := NewSource(SourceOptions{Repo: "test/repo", CacheDir: t.TempDir(), APIBase: srv.URL, PollEvery: time.Hour})
	src.refresh(context.Background())
	if rel := src.Get(); rel != nil {
		t.Fatalf("partial release scan must not publish a result: %+v", rel)
	}
	if requests != githubReleaseScanLimit/githubReleasePageSize {
		t.Fatalf("release page requests = %d, want %d", requests, githubReleaseScanLimit/githubReleasePageSize)
	}
}

func TestSourceUsesCustomTagPrefix(t *testing.T) {
	srv := fakeGitHub(t, "android-v3.2.1", buildTestApkBytes(t, 12, "3.2.1"))
	src := NewSource(SourceOptions{
		Repo:      "test/repo",
		CacheDir:  t.TempDir(),
		APIBase:   srv.URL,
		PollEvery: time.Hour,
		TagPrefix: "android-v",
	})

	src.refresh(context.Background())
	rel := src.Get()
	if rel == nil {
		t.Fatal("expected a release from the custom tag channel")
	}
	if rel.Tag != "android-v3.2.1" || rel.VersionName != "3.2.1" {
		t.Fatalf("custom-channel release = %+v, want android-v3.2.1", rel)
	}
}

func TestSourceKeepsPreviousReleaseOnBadDownload(t *testing.T) {
	goodAPK := buildTestApkBytes(t, 1, "1.0.0")
	srv := fakeGitHub(t, "agent-v1.0.0", goodAPK)

	src := NewSource(SourceOptions{Repo: "test/repo", CacheDir: t.TempDir(), APIBase: srv.URL, PollEvery: time.Hour})
	src.refresh(context.Background())
	good := src.Get()
	if good == nil || good.VersionName != "1.0.0" {
		t.Fatalf("setup: expected the good release to be cached first, got %+v", good)
	}

	// Now GitHub reports a new tag whose "apk" is garbage.
	badSrv := fakeGitHub(t, "agent-v2.0.0", []byte("not an apk"))
	src.opts.APIBase = badSrv.URL
	src.refresh(context.Background())

	after := src.Get()
	if after.Tag != good.Tag || after.VersionName != good.VersionName {
		t.Fatalf("a bad download must not replace the working release: had %+v, now %+v", good, after)
	}
}

func TestSourceKeepsPreviousReleaseOnTagVersionMismatch(t *testing.T) {
	goodSrv := fakeGitHub(t, "agent-v1.0.0", buildTestApkBytes(t, 1, "1.0.0"))
	cacheDir := t.TempDir()
	src := NewSource(SourceOptions{Repo: "test/repo", CacheDir: cacheDir, APIBase: goodSrv.URL, PollEvery: time.Hour})
	src.refresh(context.Background())
	good := src.Get()
	if good == nil {
		t.Fatal("setup: expected a compatible release")
	}

	// The newer tag claims 2.0.0, but the otherwise-valid APK embeds 1.5.0.
	// The published 1.0.0 cache must remain untouched.
	badSrv := fakeGitHub(t, "agent-v2.0.0", buildTestApkBytes(t, 2, "1.5.0"))
	src.opts.APIBase = badSrv.URL
	src.refresh(context.Background())

	after := src.Get()
	if after == nil || after.Tag != good.Tag || after.SHA256 != good.SHA256 {
		t.Fatalf("mismatched release replaced the last-good cache: had %+v, now %+v", good, after)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "agent.apk.tmp")); !os.IsNotExist(err) {
		t.Fatalf("temporary APK was not removed after mismatch: %v", err)
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
	srv := fakeGitHub(t, "agent-v5.0.0", apkBytes)
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

		err := s.downloadAndPublish(ctx, srv.URL+"/bad.apk", "agent-v1.0.0")
		if err == nil {
			t.Fatal("expected downloadAndPublish to fail with bad APK bytes")
		}

		// After the failure, the tmp file must be gone.
		if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
			t.Fatal("expected tmp file to be cleaned up after failed download")
		}
	})

	t.Run("stale backup prevents swap and preserves last-good", func(t *testing.T) {
		ctx := context.Background()
		dir := t.TempDir()
		s := NewSource(SourceOptions{CacheDir: dir})
		v1Bytes := buildTestApkBytes(t, 1, "1.0.0")
		v2Bytes := buildTestApkBytes(t, 2, "2.0.0")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1.apk" {
				_, _ = w.Write(v1Bytes)
				return
			}
			_, _ = w.Write(v2Bytes)
		}))
		defer srv.Close()

		if err := s.downloadAndPublish(ctx, srv.URL+"/v1.apk", "agent-v1.0.0"); err != nil {
			t.Fatalf("publish last-good release: %v", err)
		}
		good := s.Get()
		backupPath := filepath.Join(dir, "release.json.bak")
		if err := os.MkdirAll(filepath.Join(backupPath, "blocker"), 0o755); err != nil {
			t.Fatalf("create backup blocker: %v", err)
		}

		tmpPath := filepath.Join(dir, "agent.apk.tmp")
		err := s.downloadAndPublish(ctx, srv.URL+"/v2.apk", "agent-v2.0.0")
		if err == nil {
			t.Fatal("expected stale backup to block the cache swap")
		}
		if !strings.Contains(err.Error(), "stale backup exists") {
			t.Fatalf("unexpected cache-swap error: %v", err)
		}
		after := s.Get()
		if after == nil || after.Tag != good.Tag || after.SHA256 != good.SHA256 {
			t.Fatalf("failed swap replaced last-good release: had %+v, now %+v", good, after)
		}
		if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
			t.Fatal("expected APK tmp file to be cleaned up after failed swap")
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if isImmutableAPKName(entry.Name()) && filepath.Join(dir, entry.Name()) != good.Path {
				t.Fatalf("unpublished immutable APK remains after failed swap: %s", entry.Name())
			}
		}
	})
}

func TestDownloadAndPublishReconcilesStaleMetadataBackup(t *testing.T) {
	cacheDir := t.TempDir()
	v1Server := fakeGitHub(t, "agent-v1.0.0", buildTestApkBytes(t, 1, "1.0.0"))
	src := NewSource(SourceOptions{Repo: "test/repo", CacheDir: cacheDir, APIBase: v1Server.URL, PollEvery: time.Hour})
	src.refresh(context.Background())
	good := src.Get()
	if good == nil {
		t.Fatal("setup: expected a last-good release")
	}
	oldMeta, err := os.ReadFile(src.metaFile())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src.metaFile()+".bak", oldMeta, 0o644); err != nil {
		t.Fatal(err)
	}

	v2Server := fakeGitHub(t, "agent-v2.0.0", buildTestApkBytes(t, 2, "2.0.0"))
	src.opts.APIBase = v2Server.URL
	src.refresh(context.Background())

	upgraded := src.Get()
	if upgraded == nil || upgraded.Tag != "agent-v2.0.0" || upgraded.SHA256 == good.SHA256 {
		t.Fatalf("stale metadata backup blocked a valid upgrade: before=%+v after=%+v", good, upgraded)
	}
	if _, err := os.Stat(src.metaFile() + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("stale metadata backup remains after reconciliation: %v", err)
	}
}

func TestDownloadAndPublishMetadataWriteFailurePreservesLastGood(t *testing.T) {
	cacheDir := t.TempDir()
	v1Server := fakeGitHub(t, "agent-v1.0.0", buildTestApkBytes(t, 1, "1.0.0"))
	src := NewSource(SourceOptions{Repo: "test/repo", CacheDir: cacheDir, APIBase: v1Server.URL, PollEvery: time.Hour})
	src.refresh(context.Background())
	good := src.Get()
	if good == nil {
		t.Fatal("setup: expected a last-good release")
	}
	oldAPK, err := os.ReadFile(good.Path)
	if err != nil {
		t.Fatal(err)
	}
	oldMeta, err := os.ReadFile(filepath.Join(cacheDir, "release.json"))
	if err != nil {
		t.Fatal(err)
	}

	// A directory at the metadata temp path makes os.WriteFile fail on every
	// supported platform, after the new APK has been downloaded and validated.
	if err := os.Mkdir(filepath.Join(cacheDir, "release.json.tmp"), 0o755); err != nil {
		t.Fatalf("create metadata write blocker: %v", err)
	}
	v2Bytes := buildTestApkBytes(t, 2, "2.0.0")
	v2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(v2Bytes)
	}))
	defer v2Server.Close()

	err = src.downloadAndPublish(context.Background(), v2Server.URL+"/agent.apk", "agent-v2.0.0")
	if err == nil || !strings.Contains(err.Error(), "write release metadata") {
		t.Fatalf("expected metadata write error, got %v", err)
	}
	after := src.Get()
	if after == nil || after.Tag != good.Tag || after.SHA256 != good.SHA256 {
		t.Fatalf("metadata failure replaced last-good release: had %+v, now %+v", good, after)
	}
	newAPK, err := os.ReadFile(good.Path)
	if err != nil {
		t.Fatal(err)
	}
	newMeta, err := os.ReadFile(filepath.Join(cacheDir, "release.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(newAPK, oldAPK) || !bytes.Equal(newMeta, oldMeta) {
		t.Fatal("metadata write failure changed the on-disk last-good cache")
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "agent.apk.tmp")); !os.IsNotExist(err) {
		t.Fatalf("APK tmp remains after metadata write failure: %v", err)
	}
}

func TestSwapPreparedMetadataRestoresPreviousPointer(t *testing.T) {
	cacheDir := t.TempDir()
	src := NewSource(SourceOptions{CacheDir: cacheDir})
	oldMeta := []byte(`{"tag":"agent-v1.0.0"}`)
	if err := os.WriteFile(src.metaFile(), oldMeta, 0o644); err != nil {
		t.Fatal(err)
	}
	missingMetaTmp := src.metaFile() + ".missing"

	err := src.swapPreparedMetadata(missingMetaTmp)
	if err == nil || !strings.Contains(err.Error(), "install release metadata") {
		t.Fatalf("expected metadata install error, got %v", err)
	}
	gotMeta, err := os.ReadFile(src.metaFile())
	if err != nil {
		t.Fatalf("read restored metadata: %v", err)
	}
	if !bytes.Equal(gotMeta, oldMeta) {
		t.Fatalf("rollback did not restore last-good metadata: got %q", gotMeta)
	}
	if _, err := os.Stat(src.metaFile() + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("backup remains after rollback: %v", err)
	}
}

func TestStartRecoversInterruptedCacheSwap(t *testing.T) {
	startWithCancelledContext := func(src *Source) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		src.Start(ctx)
	}
	assertNoTransactionArtifacts := func(t *testing.T, src *Source) {
		t.Helper()
		for _, path := range []string{
			src.apkFile() + ".tmp",
			src.metaFile() + ".tmp",
			src.metaFile() + ".bak",
		} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("transaction artifact remains after recovery: %s (%v)", path, err)
			}
		}
	}

	t.Run("restores previous metadata when the live pointer is missing", func(t *testing.T) {
		src := NewSource(SourceOptions{
			Repo:      "test/repo",
			CacheDir:  t.TempDir(),
			APIBase:   "http://127.0.0.1:0",
			PollEvery: time.Hour,
		})
		writeTestCachePair(t, src, 1, "1.0.0")
		if err := os.Rename(src.metaFile(), src.metaFile()+".bak"); err != nil {
			t.Fatal(err)
		}
		orphanPath := writeTestImmutableAPK(t, src, 2, "2.0.0")
		pendingMeta, err := json.Marshal(cacheMetadata{Tag: "agent-v2.0.0", APK: filepath.Base(orphanPath)})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(src.metaFile()+".tmp", pendingMeta, 0o644); err != nil {
			t.Fatal(err)
		}

		startWithCancelledContext(src)
		rel := src.Get()
		if rel == nil || rel.Tag != "agent-v1.0.0" || rel.VersionName != "1.0.0" {
			t.Fatalf("recovered release = %+v, want last-good agent-v1.0.0", rel)
		}
		assertNoTransactionArtifacts(t, src)
		if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
			t.Fatalf("orphaned immutable APK remains after startup recovery: %v", err)
		}
	})

	t.Run("finalizes a complete new metadata pointer", func(t *testing.T) {
		src := NewSource(SourceOptions{
			Repo:      "test/repo",
			CacheDir:  t.TempDir(),
			APIBase:   "http://127.0.0.1:0",
			PollEvery: time.Hour,
		})
		writeTestCachePair(t, src, 1, "1.0.0")
		if err := os.Rename(src.metaFile(), src.metaFile()+".bak"); err != nil {
			t.Fatal(err)
		}
		v2Path := writeTestImmutableCachePair(t, src, 2, "2.0.0")

		startWithCancelledContext(src)
		rel := src.Get()
		if rel == nil || rel.Tag != "agent-v2.0.0" || rel.VersionName != "2.0.0" || rel.Path != v2Path {
			t.Fatalf("recovered release = %+v, want complete agent-v2.0.0", rel)
		}
		assertNoTransactionArtifacts(t, src)
	})
}

func TestStartRemovesOnlyOrphanedImmutableAPKs(t *testing.T) {
	src := NewSource(SourceOptions{
		Repo:      "test/repo",
		CacheDir:  t.TempDir(),
		APIBase:   "http://127.0.0.1:0",
		PollEvery: time.Hour,
	})
	currentPath := writeTestImmutableCachePair(t, src, 2, "2.0.0")
	orphanPath := writeTestImmutableAPK(t, src, 1, "1.0.0")
	decoyPath := filepath.Join(src.opts.CacheDir, "agent-not-a-sha.apk")
	if err := os.WriteFile(decoyPath, []byte("operator-owned decoy"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src.Start(ctx)

	if rel := src.Get(); rel == nil || rel.Path != currentPath {
		t.Fatalf("current immutable APK was not restored: %+v", rel)
	}
	if _, err := os.Stat(currentPath); err != nil {
		t.Fatalf("current immutable APK was removed: %v", err)
	}
	if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
		t.Fatalf("orphaned immutable APK was not removed: %v", err)
	}
	if _, err := os.Stat(decoyPath); err != nil {
		t.Fatalf("non-content-addressed file was removed: %v", err)
	}
}

func TestCleanupOrphanedImmutableAPKsPreservesUnresolvedRecoveryState(t *testing.T) {
	t.Run("no valid current release", func(t *testing.T) {
		src := NewSource(SourceOptions{CacheDir: t.TempDir()})
		lastGoodPath := writeTestImmutableAPK(t, src, 1, "1.0.0")
		backupMeta, err := json.Marshal(cacheMetadata{Tag: "agent-v1.0.0", APK: filepath.Base(lastGoodPath)})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(src.metaFile()+".bak", backupMeta, 0o644); err != nil {
			t.Fatal(err)
		}

		src.cleanupOrphanedImmutableAPKs()

		if _, err := os.Stat(lastGoodPath); err != nil {
			t.Fatalf("artifact needed by a future recovery was removed: %v", err)
		}
	})

	t.Run("metadata backup remains unresolved", func(t *testing.T) {
		src := NewSource(SourceOptions{CacheDir: t.TempDir()})
		currentPath := writeTestImmutableCachePair(t, src, 2, "2.0.0")
		src.loadCache()
		if src.Get() == nil {
			t.Fatal("setup: expected valid current release")
		}
		lastGoodBackupPath := writeTestImmutableAPK(t, src, 1, "1.0.0")
		if err := os.MkdirAll(filepath.Join(src.metaFile()+".bak", "blocker"), 0o755); err != nil {
			t.Fatal(err)
		}

		src.cleanupOrphanedImmutableAPKs()

		for _, path := range []string{currentPath, lastGoodBackupPath} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("artifact %s was removed while backup is unresolved: %v", path, err)
			}
		}
	})
}

// --- SourceOptionsFromEnv / envOr coverage ---------------------------------

func TestParseReleaseVersion(t *testing.T) {
	tests := []struct {
		tag  string
		want releaseVersion
		ok   bool
	}{
		{tag: "agent-v0.1.0", want: releaseVersion{minor: 1}, ok: true},
		{tag: "agent-v12.34.56", want: releaseVersion{major: 12, minor: 34, patch: 56}, ok: true},
		{tag: "v12.34.56"},
		{tag: "agent-v1.2"},
		{tag: "agent-v1.2.3.4"},
		{tag: "agent-v01.2.3"},
		{tag: "agent-v1.two.3"},
		{tag: "agent-v18446744073709551616.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			got, ok := parseReleaseVersion(tt.tag, "agent-v")
			if ok != tt.ok || got != tt.want {
				t.Fatalf("parseReleaseVersion(%q) = (%+v, %v), want (%+v, %v)", tt.tag, got, ok, tt.want, tt.ok)
			}
		})
	}

	if (releaseVersion{major: 2}).compare(releaseVersion{major: 1, minor: 99, patch: 99}) <= 0 {
		t.Fatal("major version comparison is not monotonic")
	}
	if (releaseVersion{major: 2, minor: 1}).compare(releaseVersion{major: 2, minor: 1, patch: 1}) >= 0 {
		t.Fatal("patch version comparison is not monotonic")
	}
}

func TestSourceOptionsFromEnvDefaults(t *testing.T) {
	t.Setenv("RELEASE_TAG_PREFIX", "")
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
	if opts.TagPrefix != "agent-v" {
		t.Errorf("TagPrefix = %q, want agent-v", opts.TagPrefix)
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
	t.Setenv("RELEASE_TAG_PREFIX", "android-v")
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
	if opts.TagPrefix != "android-v" {
		t.Errorf("TagPrefix = %q", opts.TagPrefix)
	}
	if opts.LocalAPK != "/tmp/test.apk" {
		t.Errorf("LocalAPK = %q", opts.LocalAPK)
	}
}

func TestSourceOptionsFromEnvInvalidPollMs(t *testing.T) {
	for _, value := range []string{"not-a-number", "0", "-1"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("RELEASE_POLL_MS", value)
			opts := SourceOptionsFromEnv()
			if opts.PollEvery != defaultReleasePollEvery {
				t.Errorf("PollEvery = %v, want %v for %q", opts.PollEvery, defaultReleasePollEvery, value)
			}
		})
	}
	if got := NewSource(SourceOptions{PollEvery: -time.Second}).opts.PollEvery; got != defaultReleasePollEvery {
		t.Errorf("NewSource PollEvery = %v, want %v for non-positive direct option", got, defaultReleasePollEvery)
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
		mu       sync.Mutex
		reqCount int
	)
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/test/repo/releases", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqCount++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"tag_name": "agent-v7.0.0",
			"assets": []map[string]string{
				{"name": "agent.apk", "browser_download_url": srv.URL + "/download/agent.apk"},
			},
		}})
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
	if rel.Tag != "agent-v7.0.0" {
		t.Errorf("Tag = %q, want agent-v7.0.0", rel.Tag)
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
		metaBytes, _ := json.Marshal(map[string]string{"tag": "agent-v1.0.0"})
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

	t.Run("empty tag is not restored outside the release channel", func(t *testing.T) {
		var buf bytes.Buffer
		old := log.Writer()
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(old) })

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
		if rel := src.Get(); rel != nil {
			t.Fatalf("expected an untagged cache to be ignored, got %+v", rel)
		}
		if !strings.Contains(buf.String(), "does not match RELEASE_TAG_PREFIX") {
			t.Fatalf("expected release-channel warning, got: %s", buf.String())
		}
	})

	t.Run("legacy tag is not restored into the rewrite channel", func(t *testing.T) {
		dir := t.TempDir()
		apkPath := filepath.Join(dir, "agent.apk")
		if err := os.WriteFile(apkPath, buildTestApkBytes(t, 5, "0.5"), 0o644); err != nil {
			t.Fatalf("write apk: %v", err)
		}
		metaBytes, _ := json.Marshal(map[string]string{"tag": "v0.5"})
		if err := os.WriteFile(filepath.Join(dir, "release.json"), metaBytes, 0o644); err != nil {
			t.Fatalf("write metadata: %v", err)
		}

		src := NewSource(SourceOptions{CacheDir: dir})
		src.loadCache()
		if rel := src.Get(); rel != nil {
			t.Fatalf("expected legacy cache v0.5 to be ignored, got %+v", rel)
		}
	})

	t.Run("tag version must match cached APK versionName", func(t *testing.T) {
		var buf bytes.Buffer
		old := log.Writer()
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(old) })

		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "agent.apk"), buildTestApkBytes(t, 1, "0.1.0"), 0o644); err != nil {
			t.Fatalf("write apk: %v", err)
		}
		metaBytes, _ := json.Marshal(map[string]string{"tag": "agent-v9.9.9"})
		if err := os.WriteFile(filepath.Join(dir, "release.json"), metaBytes, 0o644); err != nil {
			t.Fatalf("write metadata: %v", err)
		}

		src := NewSource(SourceOptions{CacheDir: dir})
		src.loadCache()
		if rel := src.Get(); rel != nil {
			t.Fatalf("expected mismatched cached APK to be ignored, got %+v", rel)
		}
		if !strings.Contains(buf.String(), `expected APK versionName "9.9.9", got "0.1.0"`) {
			t.Fatalf("expected version-mismatch warning, got: %s", buf.String())
		}
	})

	t.Run("metadata APK must be a safe basename", func(t *testing.T) {
		for _, apkName := range []string{"..", "../outside.apk", "/absolute.apk"} {
			t.Run(apkName, func(t *testing.T) {
				dir := t.TempDir()
				metaBytes, err := json.Marshal(cacheMetadata{Tag: "agent-v1.0.0", APK: apkName})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "release.json"), metaBytes, 0o644); err != nil {
					t.Fatal(err)
				}
				src := NewSource(SourceOptions{CacheDir: dir})
				_, err = src.readCachedRelease()
				if err == nil || !strings.Contains(err.Error(), "not a safe basename") {
					t.Fatalf("expected unsafe metadata path rejection for %q, got %v", apkName, err)
				}
			})
		}
	})
}

// --- refresh failure paths ------------------------------------------------

func TestRefreshFailurePaths(t *testing.T) {
	t.Run("empty tag_name is outside the release channel", func(t *testing.T) {
		var buf bytes.Buffer
		old := log.Writer()
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(old) })

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"tag_name": "",
				"assets": []map[string]string{
					{"name": "agent.apk", "browser_download_url": "http://example.com/agent.apk"},
				},
			}})
		}))
		defer srv.Close()

		src := NewSource(SourceOptions{Repo: "test/repo", CacheDir: t.TempDir(), APIBase: srv.URL, PollEvery: time.Hour})
		src.refresh(context.Background())
		if !strings.Contains(buf.String(), `no stable release with tag prefix "agent-v"`) {
			t.Fatalf("expected release-channel warning, got: %s", buf.String())
		}
	})

	t.Run("no apk asset", func(t *testing.T) {
		var buf bytes.Buffer
		old := log.Writer()
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(old) })

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"tag_name": "agent-v1.0.0",
				"assets": []map[string]string{
					{"name": "agent.exe", "browser_download_url": "http://example.com/agent.exe"},
				},
			}})
		}))
		defer srv.Close()

		src := NewSource(SourceOptions{Repo: "test/repo", CacheDir: t.TempDir(), APIBase: srv.URL, PollEvery: time.Hour})
		src.refresh(context.Background())
		if !strings.Contains(buf.String(), "and a .apk asset") {
			t.Fatalf("expected missing-APK warning, got: %s", buf.String())
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
		_, err := src.fetchJSON(context.Background(), "http://127.0.0.1:1/repos/test/repo/releases", time.Second)
		if err == nil {
			t.Fatal("expected error when connection is refused")
		}
	})
}

// --- downloadAndPublish error paths -----------------------------------------

func TestDownloadAndPublishErrors(t *testing.T) {
	t.Run("tag outside configured channel", func(t *testing.T) {
		requests := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			_, _ = w.Write(buildTestApkBytes(t, 1, "0.5.0"))
		}))
		defer srv.Close()

		src := NewSource(SourceOptions{CacheDir: t.TempDir()})
		err := src.downloadAndPublish(context.Background(), srv.URL+"/legacy.apk", "v0.5.0")
		if err == nil || !strings.Contains(err.Error(), "is not a valid agent-vMAJOR.MINOR.PATCH tag") {
			t.Fatalf("expected release-channel error, got %v", err)
		}
		if requests != 0 {
			t.Fatalf("incompatible release was downloaded %d time(s)", requests)
		}
	})

	t.Run("bad url", func(t *testing.T) {
		src := NewSource(SourceOptions{})
		if err := src.downloadAndPublish(context.Background(), "://bad", "agent-v1.0.0"); err == nil {
			t.Fatal("expected error for malformed URL")
		}
	})

	t.Run("connection refused", func(t *testing.T) {
		src := NewSource(SourceOptions{})
		if err := src.downloadAndPublish(context.Background(), "http://127.0.0.1:1/agent.apk", "agent-v1.0.0"); err == nil {
			t.Fatal("expected error when connection is refused")
		}
	})

	t.Run("non-200 status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer srv.Close()

		src := NewSource(SourceOptions{})
		err := src.downloadAndPublish(context.Background(), srv.URL+"/agent.apk", "agent-v1.0.0")
		if err == nil {
			t.Fatal("expected error for non-200 download")
		}
		if !strings.Contains(err.Error(), "download -> 404") {
			t.Fatalf("expected 'download -> 404' in error, got: %v", err)
		}
	})

	t.Run("declared APK larger than the download cap", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", fmt.Sprint(maxAPKDownloadBytes+1))
		}))
		defer srv.Close()

		src := NewSource(SourceOptions{CacheDir: t.TempDir()})
		err := src.downloadAndPublish(context.Background(), srv.URL+"/agent.apk", "agent-v1.0.0")
		if err == nil || !strings.Contains(err.Error(), "too large") {
			t.Fatalf("expected oversized download rejection, got %v", err)
		}
	})

	t.Run("truncated body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1000")
			_, _ = w.Write([]byte("short body"))
		}))
		defer srv.Close()

		src := NewSource(SourceOptions{})
		err := src.downloadAndPublish(context.Background(), srv.URL+"/agent.apk", "agent-v1.0.0")
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
		if err := src.downloadAndPublish(context.Background(), srv.URL+"/agent.apk", "agent-v1.0.0"); err == nil {
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
		if err := src.downloadAndPublish(context.Background(), srv.URL+"/agent.apk", "agent-v1.0.0"); err == nil {
			t.Fatal("expected error when the tmp path is an existing directory")
		}
	})
}

func TestDownloadAndPublishVersionMismatch(t *testing.T) {
	apkBytes := buildTestApkBytes(t, 3, "1.0.0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(apkBytes)
	}))
	defer srv.Close()

	cacheDir := t.TempDir()
	src := NewSource(SourceOptions{CacheDir: cacheDir})
	err := src.downloadAndPublish(context.Background(), srv.URL+"/agent.apk", "agent-v2.0.0")
	if err == nil {
		t.Fatal("expected tag/APK version mismatch to be rejected")
	}
	if !strings.Contains(err.Error(), `expects APK versionName "2.0.0", got "1.0.0"`) {
		t.Fatalf("unexpected mismatch error: %v", err)
	}
	if rel := src.Get(); rel != nil {
		t.Fatalf("mismatched APK must not be published, got %+v", rel)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "agent.apk.tmp")); !os.IsNotExist(err) {
		t.Fatalf("temporary APK was not removed after mismatch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "agent.apk")); !os.IsNotExist(err) {
		t.Fatalf("mismatched APK reached the published cache path: %v", err)
	}
}
