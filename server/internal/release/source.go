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

const (
	defaultReleaseTagPrefix = "agent-v"
	defaultReleasePollEvery = 15 * time.Minute
	githubReleasePageSize   = 100
	githubReleaseScanLimit  = 1000
	maxAPKDownloadBytes     = int64(128 << 20)
)

type releaseVersion struct {
	major uint64
	minor uint64
	patch uint64
}

func parseReleaseVersion(tag, prefix string) (releaseVersion, bool) {
	if !strings.HasPrefix(tag, prefix) {
		return releaseVersion{}, false
	}
	raw := strings.TrimPrefix(tag, prefix)
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return releaseVersion{}, false
	}
	values := [3]uint64{}
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return releaseVersion{}, false
		}
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return releaseVersion{}, false
		}
		values[i] = value
	}
	return releaseVersion{major: values[0], minor: values[1], patch: values[2]}, true
}

func (v releaseVersion) compare(other releaseVersion) int {
	left := [...]uint64{v.major, v.minor, v.patch}
	right := [...]uint64{other.major, other.minor, other.patch}
	for i := range left {
		if left[i] < right[i] {
			return -1
		}
		if left[i] > right[i] {
			return 1
		}
	}
	return 0
}

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
	// TagPrefix is the release channel. Only GitHub releases whose tag starts
	// with this prefix can be downloaded or restored from the cache.
	TagPrefix string
	// LocalAPK is a dev/test escape hatch: serve this file and never call GitHub.
	LocalAPK string
}

func SourceOptionsFromEnv() SourceOptions {
	pollMs := int64(defaultReleasePollEvery / time.Millisecond)
	if v := os.Getenv("RELEASE_POLL_MS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			pollMs = n
		}
	}
	return SourceOptions{
		Repo:      envOr("GITHUB_REPO", "turin-dev/rish-mcp"),
		CacheDir:  envOr("RELEASE_CACHE_DIR", "/var/cache/rish-mcp"),
		APIBase:   envOr("GITHUB_API_BASE", "https://api.github.com"),
		PollEvery: time.Duration(pollMs) * time.Millisecond,
		TagPrefix: envOr("RELEASE_TAG_PREFIX", defaultReleaseTagPrefix),
		LocalAPK:  os.Getenv("APK_PATH"),
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Source polls GitHub for the highest published release in its configured tag
// channel, downloads and caches its .apk asset, and serves whatever compatible
// release it has — even a stale cache — rather than going empty because of a
// transient network failure.
type Source struct {
	opts SourceOptions

	mu      sync.RWMutex
	current *Release
}

func NewSource(opts SourceOptions) *Source {
	if opts.TagPrefix == "" {
		opts.TagPrefix = defaultReleaseTagPrefix
	}
	if opts.PollEvery <= 0 {
		opts.PollEvery = defaultReleasePollEvery
	}
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
	s.recoverInterruptedCacheSwap()
	s.removeStaleTmp()
	s.loadCache()
	s.cleanupOrphanedImmutableAPKs()
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
	rel, err := s.readCachedRelease()
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[release] ignoring unusable cache: %v", err)
		}
		return
	}
	s.mu.Lock()
	s.current = rel
	s.mu.Unlock()
	log.Printf("[release] cached %s (%s) restored", rel.VersionName, rel.Tag)
}

type cacheMetadata struct {
	Tag       string `json:"tag"`
	APK       string `json:"apk,omitempty"`
	FetchedAt string `json:"fetchedAt,omitempty"`
}

func (s *Source) readCachedRelease() (*Release, error) {
	metaBytes, err := os.ReadFile(s.metaFile())
	if err != nil {
		return nil, err
	}
	var meta cacheMetadata
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return nil, fmt.Errorf("decode release metadata: %w", err)
	}
	if !s.isCompatibleTag(meta.Tag) {
		return nil, fmt.Errorf("cached release tag %q does not match RELEASE_TAG_PREFIX=%q", meta.Tag, s.opts.TagPrefix)
	}
	if _, ok := parseReleaseVersion(meta.Tag, s.opts.TagPrefix); !ok {
		return nil, fmt.Errorf("cached release tag %q is not a valid %sMAJOR.MINOR.PATCH tag", meta.Tag, s.opts.TagPrefix)
	}
	apkName := meta.APK
	if apkName == "" {
		// Backward compatibility for caches written before immutable artifact
		// names were introduced.
		apkName = filepath.Base(s.apkFile())
	}
	if apkName != filepath.Base(apkName) || apkName == "." || apkName == ".." {
		return nil, fmt.Errorf("cached APK filename %q is not a safe basename", apkName)
	}
	apkPath := filepath.Join(s.opts.CacheDir, apkName)
	info, err := ReadApkInfo(apkPath)
	if err != nil {
		return nil, fmt.Errorf("read cached APK: %w", err)
	}
	if meta.APK != "" && apkName != immutableAPKName(info) {
		return nil, fmt.Errorf("cached APK filename %q does not match its SHA-256", apkName)
	}
	wantVersion := strings.TrimPrefix(meta.Tag, s.opts.TagPrefix)
	if info.VersionName != wantVersion {
		return nil, fmt.Errorf("cached release tag %q expected APK versionName %q, got %q", meta.Tag, wantVersion, info.VersionName)
	}
	return &Release{ApkInfo: info, Tag: meta.Tag, Path: apkPath}, nil
}

type githubRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func (s *Source) isCompatibleTag(tag string) bool {
	return strings.HasPrefix(tag, s.opts.TagPrefix) && len(tag) > len(s.opts.TagPrefix)
}

// refresh checks GitHub for the highest stable release in the configured tag
// channel and downloads it. Errors are swallowed: a failed refresh just means
// "keep serving whatever compatible release we already have".
func (s *Source) refresh(ctx context.Context) {
	releases, err := s.fetchReleases(ctx)
	if err != nil {
		s.warnFailed(err)
		return
	}

	// GitHub returns releases newest-first. Ignore drafts and prereleases to
	// preserve the old implicit-latest endpoint's stable-channel behaviour,
	// then choose the highest compatible semantic version that has an APK.
	// Selecting by tag version, rather than API creation order, prevents a
	// later-created lower tag from downgrading the published agent.
	var (
		selectedTag     string
		selectedVersion releaseVersion
		assetURL        string
		assetName       string
		haveSelection   bool
	)
	for _, candidate := range releases {
		if candidate.Draft || candidate.Prerelease || !s.isCompatibleTag(candidate.TagName) {
			continue
		}
		candidateVersion, ok := parseReleaseVersion(candidate.TagName, s.opts.TagPrefix)
		if !ok {
			continue
		}
		var candidateAssetURL, candidateAssetName string
		for _, asset := range candidate.Assets {
			if strings.HasSuffix(asset.Name, ".apk") && asset.BrowserDownloadURL != "" {
				candidateAssetURL = asset.BrowserDownloadURL
				candidateAssetName = asset.Name
				break
			}
		}
		if candidateAssetURL == "" {
			continue
		}
		if !haveSelection || candidateVersion.compare(selectedVersion) > 0 {
			selectedTag = candidate.TagName
			selectedVersion = candidateVersion
			assetURL = candidateAssetURL
			assetName = candidateAssetName
			haveSelection = true
		}
	}
	if !haveSelection {
		s.warnFailed(fmt.Errorf("no stable release with tag prefix %q and a .apk asset", s.opts.TagPrefix))
		return
	}

	s.mu.RLock()
	current := s.current
	s.mu.RUnlock()
	if current != nil {
		currentVersion, ok := parseReleaseVersion(current.Tag, s.opts.TagPrefix)
		if !ok {
			s.warnFailed(fmt.Errorf("current release tag %q is not a valid %sMAJOR.MINOR.PATCH tag", current.Tag, s.opts.TagPrefix))
			return
		}
		if selectedVersion.compare(currentVersion) <= 0 {
			return
		}
	}

	log.Printf("[release] fetching %s from %s", assetName, selectedTag)
	if err := s.downloadAndPublish(ctx, assetURL, selectedTag); err != nil {
		s.warnFailed(err)
	}
}

// fetchReleases scans complete GitHub API pages up to a hard total-entry
// bound. Reaching the bound on a full page is an error: publishing the best
// version from a partial creation-ordered list could miss a higher tag on the
// next page and is therefore less safe than keeping the last-good release.
func (s *Source) fetchReleases(ctx context.Context) ([]githubRelease, error) {
	releases := make([]githubRelease, 0, githubReleasePageSize)
	maxPages := githubReleaseScanLimit / githubReleasePageSize
	for page := 1; page <= maxPages; page++ {
		metaURL := fmt.Sprintf("%s/repos/%s/releases?per_page=%d&page=%d", s.opts.APIBase, s.opts.Repo, githubReleasePageSize, page)
		batch, err := s.fetchJSON(ctx, metaURL, 20*time.Second)
		if err != nil {
			return nil, err
		}
		if len(batch) > githubReleasePageSize {
			return nil, fmt.Errorf("GitHub release page %d returned %d entries; maximum is %d", page, len(batch), githubReleasePageSize)
		}
		releases = append(releases, batch...)
		if len(batch) < githubReleasePageSize {
			return releases, nil
		}
	}
	return nil, fmt.Errorf("GitHub release scan reached the safe limit of %d entries before the final page", githubReleaseScanLimit)
}

func (s *Source) fetchJSON(ctx context.Context, url string, timeout time.Duration) ([]githubRelease, error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "rish-mcp")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s -> %d", url, resp.StatusCode)
	}
	var body []githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body, nil
}

func (s *Source) downloadAndPublish(ctx context.Context, assetURL, tag string) error {
	version, ok := parseReleaseVersion(tag, s.opts.TagPrefix)
	if !ok {
		return fmt.Errorf("release tag %q is not a valid %sMAJOR.MINOR.PATCH tag", tag, s.opts.TagPrefix)
	}
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
	if resp.ContentLength > maxAPKDownloadBytes {
		return fmt.Errorf("download is too large: %d bytes exceeds %d", resp.ContentLength, maxAPKDownloadBytes)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAPKDownloadBytes+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > maxAPKDownloadBytes {
		return fmt.Errorf("download exceeds %d bytes", maxAPKDownloadBytes)
	}

	if err := os.MkdirAll(s.opts.CacheDir, 0o755); err != nil {
		return err
	}
	apkTmp := s.apkFile() + ".tmp"
	metaTmp := s.metaFile() + ".tmp"
	defer func() {
		_ = os.Remove(apkTmp)
		_ = os.Remove(metaTmp)
	}()
	if err := os.WriteFile(apkTmp, data, 0o644); err != nil {
		return err
	}

	// Parse before publishing: a truncated or wrong-content-type download
	// must not replace a working APK.
	info, err := ReadApkInfo(apkTmp)
	if err != nil {
		return fmt.Errorf("downloaded file is not a usable APK: %w", err)
	}
	wantVersion := strings.TrimPrefix(tag, s.opts.TagPrefix)
	if info.VersionName != wantVersion {
		return fmt.Errorf("release tag %q expects APK versionName %q, got %q", tag, wantVersion, info.VersionName)
	}
	s.mu.RLock()
	current := s.current
	s.mu.RUnlock()
	if current != nil {
		currentVersion, ok := parseReleaseVersion(current.Tag, s.opts.TagPrefix)
		if !ok {
			return fmt.Errorf("current release tag %q is not a valid %sMAJOR.MINOR.PATCH tag", current.Tag, s.opts.TagPrefix)
		}
		if version.compare(currentVersion) <= 0 {
			return fmt.Errorf("refusing non-monotonic release %s after %s", tag, current.Tag)
		}
		if info.VersionCode <= current.VersionCode {
			return fmt.Errorf("release %s versionCode %d must be greater than current %d", tag, info.VersionCode, current.VersionCode)
		}
	}

	apkName := immutableAPKName(info)
	apkPath := filepath.Join(s.opts.CacheDir, apkName)
	metaBytes, err := json.Marshal(cacheMetadata{
		Tag:       tag,
		APK:       apkName,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("encode release metadata: %w", err)
	}
	if err := os.WriteFile(metaTmp, metaBytes, 0o644); err != nil {
		return fmt.Errorf("write release metadata: %w", err)
	}

	createdAPK, err := installPreparedAPK(apkTmp, apkPath, info)
	if err != nil {
		return err
	}
	if err := s.swapPreparedMetadata(metaTmp); err != nil {
		if createdAPK {
			if removeErr := os.Remove(apkPath); removeErr != nil && !os.IsNotExist(removeErr) {
				return errors.Join(err, fmt.Errorf("remove unpublished APK: %w", removeErr))
			}
		}
		return err
	}
	s.mu.Lock()
	s.current = &Release{ApkInfo: info, Tag: tag, Path: apkPath}
	s.mu.Unlock()
	log.Printf("[release] now serving %s (%s, sha256 %s…)", info.VersionName, tag, info.SHA256[:12])
	return nil
}

func immutableAPKName(info ApkInfo) string {
	return "agent-" + info.SHA256 + ".apk"
}

func isImmutableAPKName(name string) bool {
	const (
		prefix = "agent-"
		suffix = ".apk"
	)
	if len(name) != len(prefix)+64+len(suffix) || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return false
	}
	for _, c := range name[len(prefix) : len(name)-len(suffix)] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// installPreparedAPK gives each validated APK an immutable content-addressed
// path. Old Release pointers therefore continue to serve the exact bytes that
// match their headers while a new release is being published.
func installPreparedAPK(tmp, final string, expected ApkInfo) (bool, error) {
	if existing, err := ReadApkInfo(final); err == nil {
		if existing.SHA256 != expected.SHA256 || existing.VersionName != expected.VersionName || existing.VersionCode != expected.VersionCode {
			return false, fmt.Errorf("immutable APK path %s contains unexpected content", final)
		}
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("inspect immutable APK path %s: %w", final, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return false, fmt.Errorf("install immutable APK: %w", err)
	}
	return true, nil
}

// swapPreparedMetadata is the only mutable pointer in the cache. The current
// APK is never moved; if replacing release.json fails, restoring its small
// backup leaves the previous immutable APK and metadata pair intact.
func (s *Source) swapPreparedMetadata(metaTmp string) error {
	metaPath, metaBackup := s.metaFile(), s.metaFile()+".bak"
	if exists, err := pathExists(metaBackup); err != nil {
		return fmt.Errorf("inspect release metadata backup: %w", err)
	} else if exists {
		// A crash or failed backup cleanup can leave both the valid live pointer
		// and its old backup behind. Reconcile that state here as well as during
		// startup so polling can recover without a process restart.
		s.recoverInterruptedCacheSwap()
		if stillExists, err := pathExists(metaBackup); err != nil {
			return fmt.Errorf("inspect reconciled release metadata backup: %w", err)
		} else if stillExists {
			return fmt.Errorf("refusing metadata swap while stale backup exists: %s", metaBackup)
		}
	}

	hadOld := false
	if err := os.Rename(metaPath, metaBackup); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("back up current release metadata: %w", err)
		}
	} else {
		hadOld = true
	}
	if err := os.Rename(metaTmp, metaPath); err != nil {
		installErr := fmt.Errorf("install release metadata: %w", err)
		if hadOld {
			if restoreErr := os.Rename(metaBackup, metaPath); restoreErr != nil {
				return errors.Join(installErr, fmt.Errorf("restore previous release metadata: %w", restoreErr))
			}
		}
		return installErr
	}
	if hadOld {
		if err := os.Remove(metaBackup); err != nil && !os.IsNotExist(err) {
			log.Printf("[release] failed to clean up release metadata backup %s: %v", metaBackup, err)
		}
	}
	return nil
}

// recoverInterruptedCacheSwap handles a crash after release.json was moved to
// its backup. A valid live metadata/artifact pair is complete and wins;
// otherwise the previous metadata pointer is restored. Immutable APKs never
// need rollback and remain safe for any in-flight download that already got a
// Release pointer.
func (s *Source) recoverInterruptedCacheSwap() {
	metaBackup := s.metaFile() + ".bak"
	hasBackup, err := pathExists(metaBackup)
	if err != nil {
		log.Printf("[release] failed to inspect release metadata backup: %v", err)
		return
	}
	if !hasBackup {
		return
	}
	if _, err := s.readCachedRelease(); err == nil {
		if err := os.Remove(metaBackup); err != nil && !os.IsNotExist(err) {
			log.Printf("[release] failed to finalize recovered metadata swap: %v", err)
			return
		}
		log.Printf("[release] finalized complete metadata swap after interrupted publish")
		return
	}
	if err := os.Remove(s.metaFile()); err != nil && !os.IsNotExist(err) {
		log.Printf("[release] failed to remove incomplete release metadata: %v", err)
		return
	}
	if err := os.Rename(metaBackup, s.metaFile()); err != nil {
		log.Printf("[release] failed to restore previous release metadata: %v", err)
		return
	}
	if _, err := s.readCachedRelease(); err != nil {
		log.Printf("[release] restored release metadata is unusable: %v", err)
		return
	}
	log.Printf("[release] restored last-good metadata after interrupted publish")
}

func pathExists(path string) (bool, error) {
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *Source) warnFailed(err error) {
	log.Printf("[release] refresh failed: %v", err)
}

// removeStaleTmp deletes any leftover *.tmp files from a previous crash.
// Called once at startup so an orphaned download doesn't accumulate on disk.
func (s *Source) removeStaleTmp() {
	for _, path := range []string{s.apkFile() + ".tmp", s.metaFile() + ".tmp"} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Printf("[release] failed to clean up stale tmp file %s: %v", path, err)
		}
	}
}

// cleanupOrphanedImmutableAPKs runs only during startup, before this Source is
// exposed to requests. It bounds persistent cache growth without deleting an
// old immutable path that an in-flight request from a live process may still
// be opening or streaming.
func (s *Source) cleanupOrphanedImmutableAPKs() {
	// A missing current release can mean startup recovery was unable to restore
	// release.json yet. Likewise, a remaining backup is unresolved recovery
	// state. In either case every immutable artifact may still be the last-good
	// target, so preserve all of them for a later retry.
	s.mu.RLock()
	if s.current == nil {
		s.mu.RUnlock()
		return
	}
	currentPath := filepath.Clean(s.current.Path)
	s.mu.RUnlock()
	if hasBackup, err := pathExists(s.metaFile() + ".bak"); err != nil {
		log.Printf("[release] skipping immutable APK cleanup: cannot inspect metadata backup: %v", err)
		return
	} else if hasBackup {
		log.Printf("[release] skipping immutable APK cleanup while metadata backup is unresolved")
		return
	}

	entries, err := os.ReadDir(s.opts.CacheDir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[release] failed to inspect immutable APK cache: %v", err)
		}
		return
	}
	for _, entry := range entries {
		if !isImmutableAPKName(entry.Name()) {
			continue
		}
		path := filepath.Join(s.opts.CacheDir, entry.Name())
		if filepath.Clean(path) == currentPath {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Printf("[release] failed to remove orphaned immutable APK %s: %v", path, err)
		}
	}
}
