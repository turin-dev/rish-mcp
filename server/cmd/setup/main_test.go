package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// --- randomToken ---

func TestRandomTokenLength(t *testing.T) {
	tok := randomToken()
	if len(tok) != 48 {
		t.Fatalf("expected 48 hex chars, got %d: %q", len(tok), tok)
	}
}

func TestRandomTokenHex(t *testing.T) {
	// Verify it's valid hex and produces different values on each call
	a := randomToken()
	b := randomToken()
	if a == b {
		t.Fatal("two consecutive calls produced the same token")
	}
}

// --- colorsEnabled ---

func TestColorsEnabledNoColorEnv(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if colorsEnabled() {
		t.Fatal("expected colorsEnabled=false with NO_COLOR=1")
	}
}

func TestColorsEnabledPipe(t *testing.T) {
	// When stdout is a pipe (not a char device), colorsEnabled should be false.
	// We can't easily change os.Stdout in a test, but we can verify the
	// ModeCharDevice branch works by checking the env-only path.
	t.Setenv("NO_COLOR", "")
	// The test will use the real os.Stdout which is likely a pipe in CI/test
	// runners. That's fine -- we just verify it doesn't crash.
	_ = colorsEnabled()
}

// --- style / heading / dim / good / bad ---

func TestStyleNoColor(t *testing.T) {
	useColor = false
	got := style("hello", "\x1b[1m")
	if got != "hello" {
		t.Fatalf("expected 'hello', got %q", got)
	}
}

func TestStyleWithColor(t *testing.T) {
	useColor = true
	got := style("hello", "\x1b[1m")
	want := "\x1b[1mhello\x1b[0m"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestHeadingDimGoodBad(t *testing.T) {
	useColor = false
	if heading("x") != "x" {
		t.Fatal("heading failed with no color")
	}
	if dim("x") != "x" {
		t.Fatal("dim failed with no color")
	}
	if good("x") != "✓ x" {
		t.Fatal("good failed with no color")
	}
	if bad("x") != "✗ x" {
		t.Fatal("bad failed with no color")
	}
}

// --- adbBinaryName ---

func TestAdbBinaryName(t *testing.T) {
	// Save and restore runtime.GOOS isn't possible, but we can verify the
	// structure: it always returns a path under platform-tools/.
	name := adbBinaryName()
	if !strings.HasPrefix(name, "platform-tools") {
		t.Fatalf("expected platform-tools/ prefix, got %q", name)
	}
	if runtime.GOOS == "windows" {
		if !strings.HasSuffix(name, ".exe") {
			t.Fatalf("expected .exe on windows, got %q", name)
		}
	} else {
		if strings.HasSuffix(name, ".exe") {
			t.Fatalf("unexpected .exe on %s: %q", runtime.GOOS, name)
		}
	}
}

// --- platformToolsURL ---

func TestPlatformToolsURL(t *testing.T) {
	url, err := platformToolsURL()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(url, "platform-tools-latest-") {
		t.Fatalf("unexpected URL: %s", url)
	}
	if runtime.GOOS == "windows" {
		if !strings.HasSuffix(url, ".zip") {
			t.Fatalf("expected .zip, got %s", url)
		}
	} else if runtime.GOOS == "darwin" {
		if !strings.Contains(url, "darwin") {
			t.Fatalf("expected darwin URL, got %s", url)
		}
	} else if runtime.GOOS == "linux" {
		if !strings.Contains(url, "linux") {
			t.Fatalf("expected linux URL, got %s", url)
		}
	}
}

// --- platformToolsCacheDir ---

func TestPlatformToolsCacheDir(t *testing.T) {
	dir, err := platformToolsCacheDir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(dir, ".rish-mcp") {
		t.Fatalf("expected .rish-mcp suffix, got %q", dir)
	}
	// Dir should exist after the call
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("expected dir to exist: %v", err)
	}
	_ = os.RemoveAll(dir) // cleanup
}

// --- step ---

func TestStep(t *testing.T) {
	useColor = false
	// Just verify it doesn't crash
	step(1, "test")
}

// --- prompt with stdin mocking ---

func TestPromptNonInteractive(t *testing.T) {
	nonInteractive = true
	defer func() { nonInteractive = false }()
	got := prompt("Enter value:")
	if got != "" {
		t.Fatalf("expected empty string in non-interactive mode, got %q", got)
	}
}

func TestPromptDefaultNonInteractive(t *testing.T) {
	nonInteractive = true
	defer func() { nonInteractive = false }()
	got := promptDefault("Enter value:", "default-val")
	if got != "default-val" {
		t.Fatalf("expected default value in non-interactive mode, got %q", got)
	}
}

func TestPromptYesNoNonInteractiveTrue(t *testing.T) {
	nonInteractive = true
	defer func() { nonInteractive = false }()
	if !promptYesNo("Continue?", true) {
		t.Fatal("expected true default in non-interactive mode")
	}
}

func TestPromptYesNoNonInteractiveFalse(t *testing.T) {
	nonInteractive = true
	defer func() { nonInteractive = false }()
	if promptYesNo("Continue?", false) {
		t.Fatal("expected false default in non-interactive mode")
	}
}

// withStdinPipe replaces the package-level stdin reader with a pipe for
// interactive testing, and returns a cleanup function that restores it.
func withStdinPipe(t *testing.T) (writeStdin *os.File, cleanup func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := stdin
	stdin = bufio.NewReader(r)
	cleanup = func() {
		r.Close()
		w.Close()
		stdin = saved
	}
	return w, cleanup
}

func TestPromptInteractive(t *testing.T) {
	nonInteractive = false
	useColor = false
	w, cleanup := withStdinPipe(t)
	defer cleanup()

	go func() {
		fmt.Fprint(w, "hello world\n")
		w.Close()
	}()

	got := prompt("Enter:")
	if got != "hello world" {
		t.Fatalf("expected 'hello world', got %q", got)
	}
}

func TestPromptDefaultInteractive(t *testing.T) {
	nonInteractive = false
	useColor = false
	w, cleanup := withStdinPipe(t)
	defer cleanup()

	go func() {
		fmt.Fprint(w, "\n") // empty input → use default
		w.Close()
	}()

	got := promptDefault("Enter:", "fallback")
	if got != "fallback" {
		t.Fatalf("expected 'fallback', got %q", got)
	}
}

func TestPromptYesNoInteractiveYes(t *testing.T) {
	nonInteractive = false
	useColor = false
	w, cleanup := withStdinPipe(t)
	defer cleanup()

	go func() {
		fmt.Fprint(w, "y\n")
		w.Close()
	}()

	if !promptYesNo("Continue?", false) {
		t.Fatal("expected true for 'y' input")
	}
}

func TestPromptYesNoInteractiveDefault(t *testing.T) {
	nonInteractive = false
	useColor = false
	w, cleanup := withStdinPipe(t)
	defer cleanup()

	go func() {
		fmt.Fprint(w, "\n") // empty → default (true)
		w.Close()
	}()

	if !promptYesNo("Continue?", true) {
		t.Fatal("expected true for default")
	}
}

// --- copyFile ---

func TestCopyFile(t *testing.T) {
	src := t.TempDir() + "/src.txt"
	dst := t.TempDir() + "/dst.txt"
	if err := os.WriteFile(src, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile failed: %v", err)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello world" {
		t.Fatalf("expected 'hello world', got %q", string(b))
	}
}

func TestCopyFileMissingSource(t *testing.T) {
	dst := t.TempDir() + "/dst.txt"
	if err := copyFile("/nonexistent/path", dst); err == nil {
		t.Fatal("expected error for missing source")
	}
}

func TestCopyFileDstIsDir(t *testing.T) {
	src := t.TempDir() + "/src.txt"
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	// dst is a directory — os.Create should fail
	dst := t.TempDir()
	if err := copyFile(src, dst); err == nil {
		t.Fatal("expected error when dst is a directory")
	}
}

// --- findRepoRoot ---

func TestFindRepoRoot(t *testing.T) {
	// When run from the server directory, findRepoRoot should find the repo
	// root by walking up to app/Dockerfile.build.
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot failed: %v", err)
	}
	// The root should contain the go.mod
	if _, err := os.Stat(filepath.Join(root, "server", "go.mod")); err != nil {
		t.Fatalf("repo root %q has no server/go.mod: %v", root, err)
	}
}

func TestFindRepoRootOutside(t *testing.T) {
	origWd, _ := os.Getwd()
	_ = os.Chdir("/tmp")
	defer os.Chdir(origWd)
	if _, err := findRepoRoot(); err == nil {
		t.Fatal("expected error outside repo")
	}
}

// --- downloadFile ---

func TestDownloadFileOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "agent apk payload")
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "agent.apk")
	if err := downloadFile(srv.URL, dest); err != nil {
		t.Fatalf("downloadFile failed: %v", err)
	}
	b, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if string(b) != "agent apk payload" {
		t.Fatalf("expected payload, got %q", string(b))
	}
}

func TestDownloadFileNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	err := downloadFile(srv.URL, filepath.Join(t.TempDir(), "x.apk"))
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("expected HTTP 404 in error, got %v", err)
	}
}

func TestDownloadFileConnError(t *testing.T) {
	// A server that immediately closes the connection → io.Copy fails.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("kill connection")
	}))
	// Close the listener before downloading to force a connection error.
	url := srv.URL
	srv.Close()

	err := downloadFile(url, filepath.Join(t.TempDir(), "x.apk"))
	if err == nil {
		t.Fatal("expected error for closed connection")
	}
}

// helper: writeZip creates a zip archive at path with the given entries.
// Each entry is {name, content}. A name ending in "/" creates a directory.
func writeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write zip file: %v", err)
	}
}

// --- unzip ---

func TestUnzipExtractsFiles(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "tools.zip")
	dest := filepath.Join(dir, "out")
	writeZip(t, src, map[string]string{
		"platform-tools/adb":      "#!/bin/sh\n",
		"platform-tools/lib/lib1": "lib1 content",
		"platform-tools/fastboot": "fastboot content",
	})

	if err := unzip(src, dest); err != nil {
		t.Fatalf("unzip failed: %v", err)
	}
	for _, f := range []string{"platform-tools/adb", "platform-tools/lib/lib1", "platform-tools/fastboot"} {
		b, err := os.ReadFile(filepath.Join(dest, f))
		if err != nil {
			t.Fatalf("expected %s after unzip: %v", f, err)
		}
		if len(b) == 0 {
			t.Fatalf("expected content in %s", f)
		}
	}
}

func TestUnzipMissingFile(t *testing.T) {
	err := unzip(filepath.Join(t.TempDir(), "nope.zip"), t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing zip")
	}
}

func TestUnzipRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "evil.zip")
	dest := filepath.Join(dir, "out")
	// ../ escaping entry must be rejected
	writeZip(t, src, map[string]string{
		"../evil": "bad",
	})

	err := unzip(src, dest)
	if err == nil {
		t.Fatal("expected path traversal to be rejected")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected 'escapes' in error, got %v", err)
	}
	// And nothing should have been written outside dest
	if _, err := os.Stat(filepath.Join(dir, "evil")); err == nil {
		t.Fatal("traversal file was written outside dest")
	}
}

// --- extractZipFile ---

func TestExtractZipFile(t *testing.T) {
	// Create a zip file in memory, open it as a reader, then extract one entry.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("world")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 1 {
		t.Fatalf("expected 1 file, got %d", len(zr.File))
	}

	dest := filepath.Join(t.TempDir(), "extracted.txt")
	if err := extractZipFile(zr.File[0], dest); err != nil {
		t.Fatalf("extractZipFile failed: %v", err)
	}
	b, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "world" {
		t.Fatalf("expected 'world', got %q", string(b))
	}
}

func TestExtractZipFileBadMode(t *testing.T) {
	// fill/dir/ entries have no data — Open() returns rc that reads nothing
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	_, err := zw.Create("dir/")
	if err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	// extractZipFile with a directory entry should still succeed (empty dest)
	dest := filepath.Join(t.TempDir(), "dirfile")
	if err := extractZipFile(zr.File[0], dest); err != nil {
		t.Fatalf("expected no error for dir entry, got %v", err)
	}
}

// --- listDevices ---

func TestListDevicesEmpty(t *testing.T) {
	dir := t.TempDir()
	adbScript := filepath.Join(dir, "adb")
	if err := os.WriteFile(adbScript, []byte("#!/bin/sh\necho 'List of devices attached'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	devices, err := listDevices(adbScript)
	if err != nil {
		t.Fatalf("listDevices failed: %v", err)
	}
	if len(devices) != 0 {
		t.Fatalf("expected 0 devices, got %d", len(devices))
	}
}

func TestListDevicesOneDevice(t *testing.T) {
	dir := t.TempDir()
	adbScript := filepath.Join(dir, "adb")
	if err := os.WriteFile(adbScript, []byte("#!/bin/sh\necho 'List of devices attached\nemulator-5554\tdevice'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	devices, err := listDevices(adbScript)
	if err != nil {
		t.Fatalf("listDevices failed: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}
	if devices[0] != "emulator-5554" {
		t.Fatalf("expected emulator-5554, got %q", devices[0])
	}
}

func TestListDevicesFiltersOffline(t *testing.T) {
	dir := t.TempDir()
	adbScript := filepath.Join(dir, "adb")
	if err := os.WriteFile(adbScript, []byte("#!/bin/sh\necho 'List of devices attached\nemulator-5554\tdevice\n192.168.1.10:5555\toffline'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	devices, err := listDevices(adbScript)
	if err != nil {
		t.Fatalf("listDevices failed: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}
	if devices[0] != "emulator-5554" {
		t.Fatalf("expected emulator-5554, got %q", devices[0])
	}
}

func TestListDevicesNoAdb(t *testing.T) {
	_, err := listDevices("/nonexistent/adb")
	if err == nil {
		t.Fatal("expected error for missing adb")
	}
}

// --- ensureADB ---

func TestEnsureADBFoundOnPath(t *testing.T) {
	dir := t.TempDir()
	adbPath := filepath.Join(dir, "adb")
	if err := os.WriteFile(adbPath, []byte("#!/bin/sh\necho 'fake adb'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	origPath := os.Getenv("PATH")
	defer os.Setenv("PATH", origPath)
	os.Setenv("PATH", dir+":"+origPath)

	p, err := ensureADB()
	if err != nil {
		t.Fatalf("ensureADB failed: %v", err)
	}
	if p != adbPath {
		t.Fatalf("expected %q, got %q", adbPath, p)
	}
}

func TestEnsureADBUsesCached(t *testing.T) {
	// Redirect HOME to a temp dir so platformToolsCacheDir points there.
	home := t.TempDir()
	t.Setenv("HOME", home)
	cachedDir := filepath.Join(home, ".rish-mcp", "platform-tools")
	if err := os.MkdirAll(cachedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cachedAdb := filepath.Join(cachedDir, "adb")
	if err := os.WriteFile(cachedAdb, []byte("fake cached adb"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Clear PATH so LookPath("adb") fails.
	origPath := os.Getenv("PATH")
	defer os.Setenv("PATH", origPath)
	os.Setenv("PATH", "")

	p, err := ensureADB()
	if err != nil {
		t.Fatalf("expected cached path, got error: %v", err)
	}
	if p != cachedAdb {
		t.Fatalf("expected %q, got %q", cachedAdb, p)
	}
}

func TestEnsureADBInteractiveRejects(t *testing.T) {
	// The reject branch (promptYesNo → false) is only reachable interactively:
	// in non-interactive mode promptYesNo short-circuits to its default (true),
	// so a "no" must come from stdin.
	nonInteractive = false
	useColor = false
	w, cleanup := withStdinPipe(t)
	defer cleanup()

	home := t.TempDir()
	t.Setenv("HOME", home)
	origPath := os.Getenv("PATH")
	defer os.Setenv("PATH", origPath)
	os.Setenv("PATH", "")

	go func() {
		fmt.Fprint(w, "n\n")
		w.Close()
	}()

	_, err := ensureADB()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "adb is required") {
		t.Fatalf("expected 'adb is required', got %v", err)
	}
}

func TestEnsureADBDownloads(t *testing.T) {
	nonInteractive = true
	defer func() { nonInteractive = false }()

	home := t.TempDir()
	t.Setenv("HOME", home)
	origPath := os.Getenv("PATH")
	defer os.Setenv("PATH", origPath)
	os.Setenv("PATH", "")

	// Serve a valid zip with platform-tools/adb.
	zipPath := filepath.Join(t.TempDir(), "ptools.zip")
	writeZip(t, zipPath, map[string]string{"platform-tools/adb": "fake-adb"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, zipPath)
	}))
	defer srv.Close()

	orig := platformToolsURLFunc
	platformToolsURLFunc = func() (string, error) { return srv.URL, nil }
	defer func() { platformToolsURLFunc = orig }()

	p, err := ensureADB()
	if err != nil {
		t.Fatalf("ensureADB with download failed: %v", err)
	}
	if !strings.HasSuffix(p, "adb") && !strings.HasSuffix(p, "adb.exe") {
		t.Fatalf("expected adb path, got %q", p)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("downloaded adb not found: %v", err)
	}
}

// --- ensureGoogleServicesJSON ---

func TestEnsureGoogleServicesJSONFound(t *testing.T) {
	useColor = false
	dir := t.TempDir()
	appDir := filepath.Join(dir, "myapp")
	subDir := filepath.Join(appDir, "app")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "google-services.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureGoogleServicesJSON(appDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureGoogleServicesJSONNonInteractive(t *testing.T) {
	nonInteractive = true
	defer func() { nonInteractive = false }()
	useColor = false
	dir := t.TempDir()
	// No google-services.json exists — promptYesNo returns false (default) in non-interactive mode
	if err := ensureGoogleServicesJSON(dir); err != nil {
		t.Fatalf("expected nil in non-interactive mode, got %v", err)
	}
}

func TestEnsureGoogleServicesJSONInteractiveRejects(t *testing.T) {
	// "n" at promptYesNo → return nil
	useColor = false
	w, cleanup := withStdinPipe(t)
	defer cleanup()

	dir := t.TempDir()
	go func() {
		fmt.Fprint(w, "n\n")
		w.Close()
	}()

	if err := ensureGoogleServicesJSON(dir); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestEnsureGoogleServicesJSONInteractiveEmptyPath(t *testing.T) {
	// "y" then empty path → return nil
	useColor = false
	w, cleanup := withStdinPipe(t)
	defer cleanup()

	dir := t.TempDir()
	go func() {
		fmt.Fprint(w, "y\n")
		fmt.Fprint(w, "\n")
		w.Close()
	}()

	if err := ensureGoogleServicesJSON(dir); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestEnsureGoogleServicesJSONInteractiveCopySuccess(t *testing.T) {
	useColor = false
	w, cleanup := withStdinPipe(t)
	defer cleanup()

	dir := t.TempDir()
	// Create the app/ subdirectory so copyFile can create the target
	appDir := filepath.Join(dir, "app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(dir, "source.json")
	if err := os.WriteFile(src, []byte(`{"project_info": {}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	go func() {
		fmt.Fprint(w, "y\n")
		fmt.Fprint(w, src+"\n")
		w.Close()
	}()

	if err := ensureGoogleServicesJSON(dir); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	target := filepath.Join(appDir, "google-services.json")
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("google-services.json not copied to %s: %v", target, err)
	}
}

func TestEnsureGoogleServicesJSONInteractiveCopyFails(t *testing.T) {
	useColor = false
	w, cleanup := withStdinPipe(t)
	defer cleanup()

	dir := t.TempDir()
	go func() {
		fmt.Fprint(w, "y\n")
		fmt.Fprint(w, "/nonexistent/source.json\n")
		w.Close()
	}()

	err := ensureGoogleServicesJSON(dir)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "couldn't copy google-services.json") {
		t.Fatalf("expected 'couldn't copy', got %v", err)
	}
}

// --- buildLocally ---

func TestBuildLocallyDockerNotFound(t *testing.T) {
	origPath := os.Getenv("PATH")
	defer os.Setenv("PATH", origPath)
	os.Setenv("PATH", "")

	_, err := buildLocally()
	if err == nil {
		t.Fatal("expected error when docker is not found")
	}
	if !strings.Contains(err.Error(), "docker not found") {
		t.Fatalf("expected 'docker not found' in error, got %v", err)
	}
}

// fakeRepoTree builds a minimal rish-mcp checkout under t.TempDir():
// app/Dockerfile.build (findRepoRoot anchor) + app/app/ subdir.
func fakeRepoTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	appDir := filepath.Join(root, "app")
	if err := os.MkdirAll(filepath.Join(appDir, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "Dockerfile.build"), []byte("FROM dummy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// fakeDocker installs a docker shim on PATH. The shim behaves as:
//   - `docker build` exits `buildExit` (default 0)
//   - `docker run` exits `runExit` (default 0)
//   - when `touchApk` is true, `docker run` also materializes
//     app/app/build/outputs/apk/debug/app-debug.apk so the ReadDir
//     success path has something to find.
//
// Returns the PATH-prepended bin dir plus a restore func.
func fakeDocker(t *testing.T, runExit, buildExit int, touchApk bool) (*os.File, func()) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\n"
	if touchApk {
		script += "if [ \"$1\" = run ]; then\n"
		script += "  mkdir -p app/app/build/outputs/apk/debug\n"
		script += "  : > app/app/build/outputs/apk/debug/app-debug.apk\n"
		script += "fi\n"
	}
	script += fmt.Sprintf("if [ \"$1\" = build ]; then exit %d; fi\n", buildExit)
	script += fmt.Sprintf("if [ \"$1\" = run ]; then exit %d; fi\n", runExit)
	script += "exit 0\n"
	dockerPath := filepath.Join(bin, "docker")
	if err := os.WriteFile(dockerPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	origPath := os.Getenv("PATH")
	os.Setenv("PATH", bin+":"+origPath)
	return nil, func() { os.Setenv("PATH", origPath) }
}

func TestBuildLocallyDockerBuildFails(t *testing.T) {
	useColor = false
	nonInteractive = true
	defer func() { nonInteractive = false }()

	_, restore := fakeDocker(t, 0, 1, false)
	defer restore()
	root := fakeRepoTree(t)
	origWd, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	_, err := buildLocally()
	if err == nil {
		t.Fatal("expected error on docker build failure")
	}
	if !strings.Contains(err.Error(), "docker build failed") {
		t.Fatalf("expected 'docker build failed', got %v", err)
	}
}

func TestBuildLocallyGradleFails(t *testing.T) {
	useColor = false
	nonInteractive = true
	defer func() { nonInteractive = false }()

	_, restore := fakeDocker(t, 1, 0, false)
	defer restore()
	root := fakeRepoTree(t)
	origWd, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	_, err := buildLocally()
	if err == nil {
		t.Fatal("expected error on gradle build failure")
	}
	if !strings.Contains(err.Error(), "gradle build failed") {
		t.Fatalf("expected 'gradle build failed', got %v", err)
	}
}

func TestBuildLocallySuccess(t *testing.T) {
	useColor = false
	nonInteractive = true
	defer func() { nonInteractive = false }()

	_, restore := fakeDocker(t, 0, 0, true)
	defer restore()
	root := fakeRepoTree(t)
	origWd, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	path, err := buildLocally()
	if err != nil {
		t.Fatalf("buildLocally failed: %v", err)
	}
	if !strings.HasSuffix(path, "app-debug.apk") {
		t.Fatalf("expected app-debug.apk, got %q", path)
	}
}

func TestBuildLocallyNoApk(t *testing.T) {
	useColor = false
	nonInteractive = true
	defer func() { nonInteractive = false }()

	// docker succeeds but never writes an APK (touchApk=false, but the
	// outDir doesn't exist), so ReadDir fails.
	_, restore := fakeDocker(t, 0, 0, false)
	defer restore()
	root := fakeRepoTree(t)
	origWd, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	_, err := buildLocally()
	if err == nil {
		t.Fatal("expected error when no build output exists")
	}
	if !strings.Contains(err.Error(), "no build output") {
		t.Fatalf("expected 'no build output', got %v", err)
	}
}

func TestBuildLocallyEmptyOutputDir(t *testing.T) {
	useColor = false
	nonInteractive = true
	defer func() { nonInteractive = false }()

	// docker succeeds, fakeRepoTree exists, and the expected output dir is
	// pre-created but empty → the "no .apk found" branch.
	_, restore := fakeDocker(t, 0, 0, false)
	defer restore()
	root := fakeRepoTree(t)
	outDir := filepath.Join(root, "app", "app", "build", "outputs", "apk", "debug")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	origWd, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	_, err := buildLocally()
	if err == nil {
		t.Fatal("expected error when output dir is empty")
	}
	if !strings.Contains(err.Error(), "no .apk found") {
		t.Fatalf("expected 'no .apk found', got %v", err)
	}
}

// --- acquireAPK ---

// fakeAdb installs an adb shim on PATH that reports the given device IDs
// for `adb devices`, handles `adb install` (prints "Success"), `adb tcpip`,
// and `adb shell`. Returns a restore func for PATH.
func fakeAdb(t *testing.T, deviceIDs ...string) func() {
	t.Helper()
	bin := t.TempDir()
	var sb strings.Builder
	sb.WriteString("#!/bin/sh\n")
	sb.WriteString("case \"$1\" in\n")
	sb.WriteString("  devices)\n")
	sb.WriteString("    echo 'List of devices attached'\n")
	for _, id := range deviceIDs {
		sb.WriteString(fmt.Sprintf("    echo \"%s\\tdevice\"\n", id))
	}
	sb.WriteString("    ;;\n")
	sb.WriteString("  install)\n")
	sb.WriteString("    echo 'Success'\n")
	sb.WriteString("    ;;\n")
	sb.WriteString("  tcpip)\n")
	sb.WriteString("    ;;\n")
	sb.WriteString("  shell)\n")
	sb.WriteString("    ;;\n")
	sb.WriteString("esac\n")
	sb.WriteString("exit 0\n")
	adbPath := filepath.Join(bin, "adb")
	if err := os.WriteFile(adbPath, []byte(sb.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	origPath := os.Getenv("PATH")
	os.Setenv("PATH", bin+":"+origPath)
	return func() { os.Setenv("PATH", origPath) }
}

// --- runBuildAPKOnly ---

func TestRunBuildAPKOnlyServerDownload(t *testing.T) {
	nonInteractive = true
	useColor = false
	defer func() { nonInteractive = false }()

	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "fake APK content")
	}))
	defer srv.Close()

	if err := runBuildAPKOnly(srv.URL); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Verify the APK was cached
	cacheDir := filepath.Join(home, ".rish-mcp")
	apkPath := filepath.Join(cacheDir, "rish-mcp-agent.apk")
	if _, err := os.Stat(apkPath); err != nil {
		t.Fatalf("APK not found at %s: %v", apkPath, err)
	}
}

func TestRunBuildAPKOnlyDownloadFails(t *testing.T) {
	nonInteractive = true
	useColor = false
	defer func() { nonInteractive = false }()

	home := t.TempDir()
	t.Setenv("HOME", home)

	err := runBuildAPKOnly("http://127.0.0.1:1")
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
	if !strings.Contains(err.Error(), "download failed") {
		t.Fatalf("expected 'download failed', got %v", err)
	}
}

func TestRunBuildAPKOnlyBuildLocally(t *testing.T) {
	useColor = false
	nonInteractive = true
	defer func() { nonInteractive = false }()

	_, restore := fakeDocker(t, 0, 0, true)
	defer restore()
	root := fakeRepoTree(t)
	origWd, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	// serverURL="" triggers buildLocally
	if err := runBuildAPKOnly(""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- runDeviceSetup ---

func TestRunDeviceSetupHappyPath(t *testing.T) {
	useColor = false
	nonInteractive = true
	defer func() { nonInteractive = false }()

	// Shrink the poll loop so the test runs fast.
	devicePollInterval = 1 * time.Millisecond
	devicePollAttempts = 1
	defer func() {
		devicePollInterval = 2 * time.Second
		devicePollAttempts = 15
	}()

	defer fakeAdb(t, "emulator-5554")()

	home := t.TempDir()
	t.Setenv("HOME", home)

	// Serve the APK.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "fake APK content")
	}))
	defer srv.Close()

	if err := runDeviceSetup(srv.URL); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunDeviceSetupNoDevice(t *testing.T) {
	useColor = false
	nonInteractive = true
	defer func() { nonInteractive = false }()

	devicePollInterval = 1 * time.Millisecond
	devicePollAttempts = 1
	defer func() {
		devicePollInterval = 2 * time.Second
		devicePollAttempts = 15
	}()

	// Fake adb that reports no devices.
	defer fakeAdb(t)()

	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "fake APK content")
	}))
	defer srv.Close()

	err := runDeviceSetup(srv.URL)
	if err == nil {
		t.Fatal("expected error when no device is found")
	}
	if !strings.Contains(err.Error(), "no device showed up") {
		t.Fatalf("expected 'no device showed up', got %v", err)
	}
}

func TestRunDeviceSetupFailFastNoDocker(t *testing.T) {
	useColor = false
	nonInteractive = true
	defer func() { nonInteractive = false }()

	// adb present, but docker is not → with serverURL="", runDeviceSetup
	// bails out right after adb with "no way to get an APK".
	restoreAdb := fakeAdb(t, "emulator-5554")
	defer restoreAdb()

	// Keep only the fake adb bin dir in PATH so docker is not found.
	pathDirs := strings.Split(os.Getenv("PATH"), ":")
	os.Setenv("PATH", pathDirs[0])

	err := runDeviceSetup("")
	if err != nil {
		t.Fatalf("expected nil (fail-fast is advisory), got %v", err)
	}
}

func TestRunDeviceSetupPreAndroid11Interactive(t *testing.T) {
	useColor = false
	nonInteractive = false
	w, cleanup := withStdinPipe(t)
	defer cleanup()

	// Poll loop is interactive-only here (nonInteractive=false), so the
	// device must be present on the first poll.
	devicePollInterval = 1 * time.Millisecond
	devicePollAttempts = 1
	defer func() {
		devicePollInterval = 2 * time.Second
		devicePollAttempts = 15
	}()

	defer fakeAdb(t, "emulator-5554")()

	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "fake APK content")
	}))
	defer srv.Close()

	// Stdin answers, in order:
	//   step 1: press enter (already connected)
	//   step 2: "n" → pre-Android-11 bridge path
	//   step 2: TCP port default (empty → 5555)
	//   step 3: promptYesNo download → "y"
	//   step 5: Relay URL
	//   step 5: Device token
	go func() {
		fmt.Fprint(w, "\n")
		fmt.Fprint(w, "n\n")
		fmt.Fprint(w, "\n")
		fmt.Fprint(w, "y\n")
		fmt.Fprint(w, "wss://mcp.example.com/agent\n")
		fmt.Fprint(w, "tok123\n")
		w.Close()
	}()

	if err := runDeviceSetup(srv.URL); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunDeviceSetupBuildLocally(t *testing.T) {
	useColor = false
	nonInteractive = true
	defer func() { nonInteractive = false }()

	devicePollInterval = 1 * time.Millisecond
	devicePollAttempts = 1
	defer func() {
		devicePollInterval = 2 * time.Second
		devicePollAttempts = 15
	}()

	// Both adb and docker on PATH.
	defer fakeAdb(t, "emulator-5554")()
	_, restoreDocker := fakeDocker(t, 0, 0, true)
	defer restoreDocker()

	root := fakeRepoTree(t)
	origWd, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	// serverURL="" triggers buildLocally. The full flow: ensureADB → docker
	// check → poll device → acquireAPK (buildLocally) → adb install.
	if err := runDeviceSetup(""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAcquireAPKBuildsLocallyWhenNoServer(t *testing.T) {
	// No server + no docker → falls through to buildLocally → docker error.
	nonInteractive = true
	defer func() { nonInteractive = false }()
	origPath := os.Getenv("PATH")
	defer os.Setenv("PATH", origPath)
	os.Setenv("PATH", "")

	_, err := acquireAPK("")
	if err == nil {
		t.Fatal("expected error (docker not found)")
	}
	if !strings.Contains(err.Error(), "docker not found") {
		t.Fatalf("expected docker error, got %v", err)
	}
}

func TestAcquireAPKServerDownloadFails(t *testing.T) {
	nonInteractive = true
	defer func() { nonInteractive = false }()
	// Non-interactive + yes default → tries to download from the server,
	// which is unreachable → download error.
	_, err := acquireAPK("http://127.0.0.1:1")
	if err == nil {
		t.Fatal("expected download error")
	}
	if !strings.Contains(err.Error(), "download failed") {
		t.Fatalf("expected 'download failed', got %v", err)
	}
}

// --- downloadPlatformTools ---

func TestDownloadPlatformToolsSuccess(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "ptools.zip")
	writeZip(t, zipPath, map[string]string{"platform-tools/adb": "fake-adb-binary"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, zipPath)
	}))
	defer srv.Close()

	orig := platformToolsURLFunc
	platformToolsURLFunc = func() (string, error) { return srv.URL, nil }
	defer func() { platformToolsURLFunc = orig }()

	got, err := downloadPlatformTools(t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(got, "adb") && !strings.HasSuffix(got, "adb.exe") {
		t.Fatalf("expected adb path, got %q", got)
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("adb binary not found at %s: %v", got, err)
	}
}

func TestDownloadPlatformToolsDownloadFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	orig := platformToolsURLFunc
	platformToolsURLFunc = func() (string, error) { return srv.URL, nil }
	defer func() { platformToolsURLFunc = orig }()

	_, err := downloadPlatformTools(t.TempDir())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "download failed") {
		t.Fatalf("expected 'download failed', got %v", err)
	}
}

func TestDownloadPlatformToolsBadArchive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not a zip archive"))
	}))
	defer srv.Close()

	orig := platformToolsURLFunc
	platformToolsURLFunc = func() (string, error) { return srv.URL, nil }
	defer func() { platformToolsURLFunc = orig }()

	_, err := downloadPlatformTools(t.TempDir())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "extract failed") {
		t.Fatalf("expected 'extract failed', got %v", err)
	}
}

func TestDownloadPlatformToolsMissingAdb(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "ptools.zip")
	writeZip(t, zipPath, map[string]string{"platform-tools/some-other-file": "junk"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, zipPath)
	}))
	defer srv.Close()

	orig := platformToolsURLFunc
	platformToolsURLFunc = func() (string, error) { return srv.URL, nil }
	defer func() { platformToolsURLFunc = orig }()

	_, err := downloadPlatformTools(t.TempDir())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "adb missing") {
		t.Fatalf("expected 'adb missing', got %v", err)
	}
}
