// Package update checks GitHub releases and atomically replaces the current
// dyna executable with a verified newer build.
package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const (
	DefaultRepo         = "arismoko/dyna-agent"
	DefaultAPIBase      = "https://api.github.com"
	DefaultInterval     = 24 * time.Hour
	defaultHTTPTimeout  = 2 * time.Minute
	defaultCheckTimeout = 5 * time.Second
	maxReleaseBytes     = 2 << 20
	maxChecksumBytes    = 1 << 20
	maxBinaryBytes      = 256 << 20
)

var (
	ErrDevelopmentBuild = errors.New("development builds are not updated automatically")
	ErrUpdateInProgress = errors.New("another dyna update is already in progress")
	repoPattern         = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
)

// Asset is the subset of a GitHub release asset used by the updater.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Release is the subset of a GitHub release used by the updater.
type Release struct {
	TagName    string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

// Result describes an update check or completed update.
type Result struct {
	Current   string
	Latest    string
	Available bool
	Updated   bool
	Target    string
}

type state struct {
	LastChecked time.Time `json:"lastChecked"`
	ETag        string    `json:"etag,omitempty"`
	Release     Release   `json:"release,omitempty"`
	LastError   string    `json:"lastError,omitempty"`
}

// Config makes release lookups and filesystem effects explicit and testable.
type Config struct {
	Version    string
	Repo       string
	APIBase    string
	OS         string
	Arch       string
	Executable string
	StatePath  string
	Interval   time.Duration
	Client     *http.Client
	Now        func() time.Time
}

func (c Config) normalized() (Config, error) {
	if c.Repo == "" {
		c.Repo = DefaultRepo
	}
	if !repoPattern.MatchString(c.Repo) {
		return c, fmt.Errorf("invalid GitHub repository %q", c.Repo)
	}
	if c.APIBase == "" {
		c.APIBase = DefaultAPIBase
	}
	c.APIBase = strings.TrimRight(c.APIBase, "/")
	if c.OS == "" {
		c.OS = runtime.GOOS
	}
	if c.Arch == "" {
		c.Arch = runtime.GOARCH
	}
	if c.Executable == "" {
		exe, err := os.Executable()
		if err != nil {
			return c, fmt.Errorf("locate dyna executable: %w", err)
		}
		c.Executable = exe
	}
	if resolved, err := filepath.EvalSymlinks(c.Executable); err == nil {
		c.Executable = resolved
	}
	if c.StatePath == "" {
		cacheDir, err := os.UserCacheDir()
		if err != nil {
			return c, fmt.Errorf("locate update cache: %w", err)
		}
		c.StatePath = filepath.Join(cacheDir, "dyna", "update-check.json")
	}
	if c.Interval <= 0 {
		c.Interval = DefaultInterval
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Client == nil {
		c.Client = &http.Client{
			Timeout: defaultHTTPTimeout,
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				if req.URL.Scheme != "https" || !isGitHubDownloadHost(req.URL.Hostname()) {
					return fmt.Errorf("refusing release redirect to %s", req.URL.Redacted())
				}
				return nil
			},
		}
	}
	return c, nil
}

// Check returns the newest stable release. When cached is true, a successful
// lookup is reused for Interval and refreshed conditionally with its ETag.
func (c Config) Check(ctx context.Context, cached bool) (Result, error) {
	c, err := c.normalized()
	if err != nil {
		return Result{}, err
	}
	release, _, err := c.latest(ctx, cached)
	if err != nil {
		return Result{}, err
	}
	return compare(c.Version, release.TagName), nil
}

// Apply downloads and installs the newest stable release. force permits an
// explicit replacement of a development, equal, or newer local build.
func (c Config) Apply(ctx context.Context, force, cached bool) (Result, error) {
	c, err := c.normalized()
	if err != nil {
		return Result{}, err
	}
	unlock, err := c.acquireLock()
	if err != nil {
		return Result{}, err
	}
	defer unlock()

	release, st, err := c.latest(ctx, cached)
	if err != nil {
		return Result{}, err
	}
	result := compare(c.Version, release.TagName)
	if release.TagName == "" {
		return result, nil
	}
	if !force {
		if !semver.IsValid(c.Version) {
			return result, ErrDevelopmentBuild
		}
		if semver.Compare(release.TagName, c.Version) <= 0 {
			return result, nil
		}
	}
	if c.OS == "windows" {
		return result, fmt.Errorf("automatic replacement is not supported on Windows yet")
	}

	assetName := binaryAssetName(c.OS, c.Arch)
	binary, ok := findAsset(release, assetName)
	if !ok {
		return result, fmt.Errorf("release %s has no asset %s", release.TagName, assetName)
	}
	checksums, ok := findAsset(release, "checksums.txt")
	if !ok {
		return result, fmt.Errorf("release %s has no checksums.txt", release.TagName)
	}
	if err := c.replace(ctx, release.TagName, binary, checksums); err != nil {
		return result, err
	}

	result.Updated = true
	result.Available = false
	result.Target = c.Executable
	st.LastChecked = c.Now().UTC()
	st.Release = release
	_ = c.saveState(st)
	return result, nil
}

func compare(current, latest string) Result {
	r := Result{Current: current, Latest: latest}
	if semver.IsValid(current) && semver.IsValid(latest) {
		r.Available = semver.Compare(latest, current) > 0
	}
	return r
}

func (c Config) latest(ctx context.Context, cached bool) (Release, state, error) {
	st, _ := c.loadState()
	cacheFor := c.Interval
	if st.LastError != "" && cacheFor > time.Hour {
		cacheFor = time.Hour
	}
	age := c.Now().Sub(st.LastChecked)
	if cached && !st.LastChecked.IsZero() && age >= 0 && age < cacheFor {
		if st.LastError != "" {
			return Release{}, st, errors.New(st.LastError)
		}
		return st.Release, st, nil
	}

	endpoint := fmt.Sprintf("%s/repos/%s/releases/latest", c.APIBase, c.Repo)
	checkCtx, cancel := context.WithTimeout(ctx, defaultCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Release{}, st, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "dyna/"+displayVersion(c.Version))
	if st.ETag != "" {
		req.Header.Set("If-None-Match", st.ETag)
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return c.cacheFailure(st, fmt.Errorf("check GitHub release: %w", err))
	}
	defer resp.Body.Close()

	st.LastChecked = c.Now().UTC()
	switch resp.StatusCode {
	case http.StatusNotModified:
		if st.Release.TagName == "" {
			return c.cacheFailure(st, fmt.Errorf("GitHub returned 304 without a cached release"))
		}
		st.LastError = ""
		_ = c.saveState(st)
		return st.Release, st, nil
	case http.StatusNotFound:
		st.ETag = ""
		st.Release = Release{}
		st.LastError = ""
		_ = c.saveState(st)
		return Release{}, st, nil
	case http.StatusOK:
		// Continue below.
	default:
		return c.cacheFailure(st, fmt.Errorf("check GitHub release: HTTP %s", resp.Status))
	}

	body, err := readLimited(resp.Body, maxReleaseBytes)
	if err != nil {
		return c.cacheFailure(st, fmt.Errorf("read GitHub release: %w", err))
	}
	var release Release
	if err := json.Unmarshal(body, &release); err != nil {
		return c.cacheFailure(st, fmt.Errorf("decode GitHub release: %w", err))
	}
	if release.Draft || release.Prerelease {
		return c.cacheFailure(st, fmt.Errorf("GitHub latest release %q is not stable", release.TagName))
	}
	if !semver.IsValid(release.TagName) || semver.Prerelease(release.TagName) != "" {
		return c.cacheFailure(st, fmt.Errorf("GitHub latest release has invalid stable version %q", release.TagName))
	}
	st.ETag = resp.Header.Get("ETag")
	st.Release = release
	st.LastError = ""
	_ = c.saveState(st)
	return release, st, nil
}

func (c Config) cacheFailure(st state, err error) (Release, state, error) {
	st.LastChecked = c.Now().UTC()
	st.LastError = err.Error()
	_ = c.saveState(st)
	return Release{}, st, err
}

func (c Config) replace(ctx context.Context, tag string, binary, checksums Asset) error {
	if err := c.validateDownloadURL(binary.BrowserDownloadURL); err != nil {
		return fmt.Errorf("binary asset URL: %w", err)
	}
	if err := c.validateDownloadURL(checksums.BrowserDownloadURL); err != nil {
		return fmt.Errorf("checksum asset URL: %w", err)
	}

	checksumBody, err := c.download(ctx, checksums.BrowserDownloadURL, maxChecksumBytes)
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	expected, err := checksumFor(checksumBody, binary.Name)
	if err != nil {
		return err
	}

	oldInfo, err := os.Stat(c.Executable)
	if err != nil {
		return fmt.Errorf("inspect current executable: %w", err)
	}
	if !oldInfo.Mode().IsRegular() {
		return fmt.Errorf("refusing to replace non-regular executable %s", c.Executable)
	}
	dir := filepath.Dir(c.Executable)
	tmp, err := os.CreateTemp(dir, ".dyna-update-*")
	if err != nil {
		return fmt.Errorf("stage update beside %s: %w", c.Executable, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	resp, err := c.getDownload(ctx, binary.BrowserDownloadURL)
	if err != nil {
		tmp.Close()
		return fmt.Errorf("download %s: %w", binary.Name, err)
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(resp.Body, maxBinaryBytes+1))
	closeBodyErr := resp.Body.Close()
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if copyErr != nil {
		return fmt.Errorf("download %s: %w", binary.Name, copyErr)
	}
	if closeBodyErr != nil {
		return fmt.Errorf("download %s: %w", binary.Name, closeBodyErr)
	}
	if n > maxBinaryBytes {
		return fmt.Errorf("download %s exceeds %d bytes", binary.Name, maxBinaryBytes)
	}
	if syncErr != nil || closeErr != nil {
		return fmt.Errorf("flush staged update: %v %v", syncErr, closeErr)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", binary.Name, actual, expected)
	}
	mode := oldInfo.Mode().Perm()
	if mode&0o111 == 0 {
		mode |= 0o755
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return fmt.Errorf("make staged update executable: %w", err)
	}
	if err := verifyBinary(ctx, tmpPath, tag); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, c.Executable); err != nil {
		return fmt.Errorf("atomically replace %s: %w", c.Executable, err)
	}
	return nil
}

func verifyBinary(ctx context.Context, path, tag string) error {
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = append(os.Environ(), "DYNA_NO_AUTO_UPDATE=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("staged binary failed its version check: %w: %s", err, strings.TrimSpace(string(out)))
	}
	reported := strings.TrimSpace(string(out))
	if reported != "dyna "+tag {
		return fmt.Errorf("staged binary reports %q, expected version %s", reported, tag)
	}
	return nil
}

func (c Config) download(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	resp, err := c.getDownload(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return readLimited(resp.Body, limit)
}

func (c Config) getDownload(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "dyna/"+displayVersion(c.Version))
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	return resp, nil
}

func (c Config) validateDownloadURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	api, _ := url.Parse(c.APIBase)
	if api != nil && u.Scheme == api.Scheme && u.Host == api.Host {
		return nil // httptest and GitHub Enterprise-style endpoints.
	}
	if u.Scheme != "https" || !isGitHubDownloadHost(u.Hostname()) {
		return fmt.Errorf("refusing non-GitHub URL %s", u.Redacted())
	}
	return nil
}

func isGitHubDownloadHost(host string) bool {
	host = strings.ToLower(host)
	return host == "github.com" || strings.HasSuffix(host, ".github.com") || strings.HasSuffix(host, ".githubusercontent.com")
}

func binaryAssetName(goos, arch string) string {
	name := "dyna_" + goos + "_" + arch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

func findAsset(release Release, name string) (Asset, bool) {
	for _, asset := range release.Assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return Asset{}, false
}

func checksumFor(body []byte, name string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		candidate := strings.TrimPrefix(fields[1], "*")
		if candidate != name {
			continue
		}
		if len(fields[0]) != sha256.Size*2 {
			return "", fmt.Errorf("invalid checksum for %s", name)
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return "", fmt.Errorf("invalid checksum for %s", name)
		}
		return strings.ToLower(fields[0]), nil
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s", name)
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return b, nil
}

func displayVersion(v string) string {
	if v == "" {
		return "dev"
	}
	return v
}

func (c Config) loadState() (state, error) {
	b, err := os.ReadFile(c.StatePath)
	if err != nil {
		if os.IsNotExist(err) {
			return state{}, nil
		}
		return state{}, err
	}
	var st state
	if err := json.Unmarshal(b, &st); err != nil {
		return state{}, err
	}
	return st, nil
}

func (c Config) saveState(st state) error {
	if err := os.MkdirAll(filepath.Dir(c.StatePath), 0o755); err != nil {
		return fmt.Errorf("create update cache: %w", err)
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.StatePath), ".update-check-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, c.StatePath)
}

func (c Config) acquireLock() (func(), error) {
	if err := os.MkdirAll(filepath.Dir(c.StatePath), 0o755); err != nil {
		return nil, err
	}
	path := c.StatePath + ".lock"
	create := func() (*os.File, error) {
		return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	}
	f, err := create()
	if errors.Is(err, os.ErrExist) {
		if info, statErr := os.Stat(path); statErr == nil && c.Now().Sub(info.ModTime()) > 10*time.Minute {
			_ = os.Remove(path)
			f, err = create()
		}
	}
	if errors.Is(err, os.ErrExist) {
		return nil, ErrUpdateInProgress
	}
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Close()
	return func() { _ = os.Remove(path) }, nil
}
