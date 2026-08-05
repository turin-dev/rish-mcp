package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Release is one fetched/cached APK plus the GitHub release tag it came from.
type Release struct {
	ApkInfo
	Tag  string // release tag the bytes came from, or "local" for an APK_PATH override
	Path string
}

type SourceOptions struct {
	Repo      string
	CacheDir  string
	APIBase   string
	PollEvery time.Duration
	// LocalAPK is a dev/test escape hatch: serve this file and never call GitHub.
	LocalAPK string
}

func SourceOptionsFromEnv() SourceOptions {
	pollMs := int64(15 * time.Minute / time.Millisecond)
	if v := os.Getenv("RELEASE_POLL_MS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			pollMs = n
		}
	}
	return SourceOptions{
		Repo:      envOr("GITHUB_REPO", "turin-dev/rish-mcp"),
		CacheDir:  envOr("RELEASE_CACHE_DIR", "/var/cache/rish-mcp"),
		APIBase:   envOr("GITHUB_API_BASE", "https://api.github.com"),
		PollEvery: time.Duration(pollMs) * time.Millisecond,
		LocalAPK:  os.Getenv("APK_PATH"),
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Source polls GitHub for the newest release, downloads and caches its .apk
// asset, and serves whatever it has — even a stale cache — rather than ever
// going empty because of a transient network failure.
type Source struct {
	opts SourceOptions

	mu      sync.RWMutex
	current *Release
}

func NewSource(opts SourceOptions) *Source {
	return &Source{opts: opts}
}

// Get returns the current release, or nil if none has been fetched/cached
// yet. A LocalAPK override is re-read on every call so a rebuilt APK is
// picked up without restarting the process.
func (s *Source) Get() *Release {
	if s.opts.LocalAPK != "" {
		info, err := ReadApkInfo(s.opts.LocalAPK)
		if err != nil {
			return nil
		}
		return &Release{ApkInfo: info, Tag: "local", Path: s.opts.LocalAPK}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// Start loads any cached copy, then polls for new releases until ctx is
// cancelled. Never blocks the caller — polling runs in its own goroutine.
func (s *Source) Start(ctx context.Context) {
	if s.opts.LocalAPK != "" {
		log.Printf("[release] serving local APK_PATH=%s (GitHub polling disabled)", s.opts.LocalAPK)
		return
	}
	s.loadCache()
	go func() {
		s.refresh(ctx)
		ticker := time.NewTicker(s.opts.PollEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.refresh(ctx)
			}
		}
	}()
}

func (s *Source) apkFile() string  { return filepath.Join(s.opts.CacheDir, "agent.apk") }
func (s *Source) metaFile() string { return filepath.Join(s.opts.CacheDir, "release.json") }

func (s *Source) loadCache() {
	apkPath := s.apkFile()
	if _, err := os.Stat(apkPath); err != nil {
		return
	}
	metaBytes, err := os.ReadFile(s.metaFile())
	if err != nil {
		return
	}
	var meta struct {
		Tag string `json:"tag"`
	}
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return
	}
	info, err := ReadApkInfo(apkPath)
	if err != nil {
		log.Printf("[release] ignoring unusable cache: %v", err)
		return
	}
	tag := meta.Tag
	if tag == "" {
		tag = "unknown"
	}
	s.mu.Lock()
	s.current = &Release{ApkInfo: info, Tag: tag, Path: apkPath}
	s.mu.Unlock()
	log.Printf("[release] cached %s (%s) restored", info.VersionName, tag)
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// refresh checks GitHub for a newer release and downloads it. Swallows all
// errors — a failed refresh just means "keep serving whatever we already have".
func (s *Source) refresh(ctx context.Context) {
	metaURL := s.opts.APIBase + "/repos/" + s.opts.Repo + "/releases/latest"
	body, err := s.fetchJSON(ctx, metaURL, 20*time.Second)
	if err != nil {
		s.warnFailed(err)
		return
	}
	if body.TagName == "" {
		s.warnFailed(errors.New("release has no tag_name"))
		return
	}

	s.mu.RLock()
	alreadyHaveIt := s.current != nil && s.current.Tag == body.TagName
	s.mu.RUnlock()
	if alreadyHaveIt {
		return
	}

	var assetURL, assetName string
	for _, a := range body.Assets {
		if strings.HasSuffix(a.Name, ".apk") {
			assetURL, assetName = a.BrowserDownloadURL, a.Name
			break
		}
	}
	if assetURL == "" {
		s.warnFailed(fmt.Errorf("release %s has no .apk asset", body.TagName))
		return
	}

	log.Printf("[release] fetching %s from %s", assetName, body.TagName)
	if err := s.downloadAndPublish(ctx, assetURL, body.TagName); err != nil {
		s.warnFailed(err)
	}
}

func (s *Source) fetchJSON(ctx context.Context, url string, timeout time.Duration) (githubRelease, error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return githubRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "rish-mcp")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return githubRelease{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return githubRelease{}, fmt.Errorf("GET %s -> %d", url, resp.StatusCode)
	}
	var body githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return githubRelease{}, err
	}
	return body, nil
}

func (s *Source) downloadAndPublish(ctx context.Context, assetURL, tag string) error {
	dlCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, assetURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "rish-mcp")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download -> %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(s.opts.CacheDir, 0o755); err != nil {
		return err
	}
	tmp := s.apkFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}

	// Parse before publishing: a truncated or wrong-content-type download
	// must not replace a working APK.
	info, err := ReadApkInfo(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("downloaded file is not a usable APK: %w", err)
	}
	wantVersion := strings.TrimPrefix(tag, "v")
	if info.VersionName != wantVersion {
		log.Printf("[release] tag %s does not match APK versionName %s; trusting the APK", tag, info.VersionName)
	}

	if err := os.Rename(tmp, s.apkFile()); err != nil {
		return err
	}
	metaBytes, _ := json.Marshal(map[string]string{"tag": tag, "fetchedAt": time.Now().UTC().Format(time.RFC3339)})
	_ = os.WriteFile(s.metaFile(), metaBytes, 0o644)

	fresh, err := ReadApkInfo(s.apkFile())
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.current = &Release{ApkInfo: fresh, Tag: tag, Path: s.apkFile()}
	s.mu.Unlock()
	log.Printf("[release] now serving %s (%s, sha256 %s…)", fresh.VersionName, tag, fresh.SHA256[:12])
	return nil
}

func (s *Source) warnFailed(err error) {
	log.Printf("[release] refresh failed: %v", err)
}
