package release

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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