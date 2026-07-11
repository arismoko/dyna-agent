package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type releaseFixture struct {
	server       *httptest.Server
	binary       []byte
	checksum     string
	tag          string
	draft        bool
	prerelease   bool
	latestStatus int
	binaryURL    string
	checksumURL  string
	latestHits   atomic.Int32
	binaryHits   atomic.Int32
	checksumHits atomic.Int32
}

func newReleaseFixture(t *testing.T, tag string) *releaseFixture {
	t.Helper()
	f := &releaseFixture{
		tag:      tag,
		binary:   []byte("#!/bin/sh\necho 'dyna " + tag + "'\n"),
		checksum: "",
	}
	sum := sha256.Sum256(f.binary)
	f.checksum = hex.EncodeToString(sum[:])
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/arismoko/dyna-agent/releases/latest":
			f.latestHits.Add(1)
			if f.latestStatus != 0 {
				w.WriteHeader(f.latestStatus)
				return
			}
			if r.Header.Get("If-None-Match") == `"release-1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"release-1"`)
			binaryURL := f.binaryURL
			if binaryURL == "" {
				binaryURL = f.server.URL + "/download/dyna"
			}
			checksumURL := f.checksumURL
			if checksumURL == "" {
				checksumURL = f.server.URL + "/download/checksums.txt"
			}
			_ = json.NewEncoder(w).Encode(Release{
				TagName: f.tag, Draft: f.draft, Prerelease: f.prerelease,
				Assets: []Asset{
					{Name: binaryAssetName(runtime.GOOS, runtime.GOARCH), BrowserDownloadURL: binaryURL},
					{Name: "checksums.txt", BrowserDownloadURL: checksumURL},
				},
			})
		case "/download/dyna":
			f.binaryHits.Add(1)
			_, _ = w.Write(f.binary)
		case "/download/checksums.txt":
			f.checksumHits.Add(1)
			_, _ = io.WriteString(w, f.checksum+"  "+binaryAssetName(runtime.GOOS, runtime.GOARCH)+"\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *releaseFixture) config(t *testing.T, version string, now func() time.Time) Config {
	t.Helper()
	target := filepath.Join(t.TempDir(), "dyna")
	if err := os.WriteFile(target, []byte("#!/bin/sh\necho 'dyna "+version+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return Config{
		Version: version, Repo: DefaultRepo, APIBase: f.server.URL,
		OS: runtime.GOOS, Arch: runtime.GOARCH, Executable: target,
		StatePath: filepath.Join(t.TempDir(), "update-check.json"),
		Client:    f.server.Client(), Now: now,
	}
}

func TestCheckCachesReleaseAndUsesETag(t *testing.T) {
	f := newReleaseFixture(t, "v1.2.0")
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	cfg := f.config(t, "v1.1.0", func() time.Time { return now })

	got, err := cfg.Check(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Available || got.Latest != "v1.2.0" || f.latestHits.Load() != 1 {
		t.Fatalf("first check = %#v, latest hits = %d", got, f.latestHits.Load())
	}
	if _, err := cfg.Check(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if f.latestHits.Load() != 1 {
		t.Fatalf("fresh cache made %d latest requests, want 1", f.latestHits.Load())
	}

	now = now.Add(25 * time.Hour)
	got, err = cfg.Check(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Available || f.latestHits.Load() != 2 {
		t.Fatalf("conditional check = %#v, latest hits = %d", got, f.latestHits.Load())
	}
}

func TestApplyVerifiesAndAtomicallyReplacesExecutable(t *testing.T) {
	f := newReleaseFixture(t, "v1.2.0")
	cfg := f.config(t, "v1.1.0", time.Now)
	oldBody, err := os.ReadFile(cfg.Executable)
	if err != nil {
		t.Fatal(err)
	}
	oldHandle, err := os.Open(cfg.Executable)
	if err != nil {
		t.Fatal(err)
	}
	defer oldHandle.Close()

	got, err := cfg.Apply(context.Background(), false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Updated || got.Latest != "v1.2.0" || got.Target != cfg.Executable {
		t.Fatalf("apply = %#v", got)
	}
	newBody, err := os.ReadFile(cfg.Executable)
	if err != nil {
		t.Fatal(err)
	}
	if string(newBody) != string(f.binary) {
		t.Fatalf("installed body = %q, want %q", newBody, f.binary)
	}
	stillOld, err := io.ReadAll(oldHandle)
	if err != nil {
		t.Fatal(err)
	}
	if string(stillOld) != string(oldBody) {
		t.Fatalf("open old inode changed: got %q, want %q", stillOld, oldBody)
	}
	if f.binaryHits.Load() != 1 || f.checksumHits.Load() != 1 {
		t.Fatalf("download hits binary=%d checksums=%d", f.binaryHits.Load(), f.checksumHits.Load())
	}
}

func TestApplyChecksumMismatchLeavesOriginalUntouched(t *testing.T) {
	f := newReleaseFixture(t, "v1.2.0")
	f.checksum = strings.Repeat("0", sha256.Size*2)
	cfg := f.config(t, "v1.1.0", time.Now)
	want, _ := os.ReadFile(cfg.Executable)

	_, err := cfg.Apply(context.Background(), false, false)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("apply error = %v", err)
	}
	got, readErr := os.ReadFile(cfg.Executable)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(want) {
		t.Fatalf("original executable changed after mismatch: got %q want %q", got, want)
	}
}

func TestApplyRejectsUntrustedAssetURL(t *testing.T) {
	f := newReleaseFixture(t, "v1.2.0")
	f.binaryURL = "https://example.com/not-a-github-release"
	cfg := f.config(t, "v1.1.0", time.Now)
	want, _ := os.ReadFile(cfg.Executable)

	_, err := cfg.Apply(context.Background(), false, false)
	if err == nil || !strings.Contains(err.Error(), "refusing non-GitHub URL") {
		t.Fatalf("apply error = %v", err)
	}
	got, _ := os.ReadFile(cfg.Executable)
	if string(got) != string(want) || f.binaryHits.Load() != 0 {
		t.Fatalf("untrusted URL changed executable or downloaded: body=%q hits=%d", got, f.binaryHits.Load())
	}
}

func TestApplyVersionMismatchLeavesOriginalUntouched(t *testing.T) {
	f := newReleaseFixture(t, "v1.2.0")
	f.binary = []byte("#!/bin/sh\necho 'dyna v9.9.9'\n")
	sum := sha256.Sum256(f.binary)
	f.checksum = hex.EncodeToString(sum[:])
	cfg := f.config(t, "v1.1.0", time.Now)
	want, _ := os.ReadFile(cfg.Executable)

	_, err := cfg.Apply(context.Background(), false, false)
	if err == nil || !strings.Contains(err.Error(), "expected version v1.2.0") {
		t.Fatalf("apply error = %v", err)
	}
	got, _ := os.ReadFile(cfg.Executable)
	if string(got) != string(want) {
		t.Fatalf("version mismatch changed executable: got %q want %q", got, want)
	}
}

func TestApplyRefusesDevelopmentAndNonNewerBuilds(t *testing.T) {
	t.Run("development", func(t *testing.T) {
		f := newReleaseFixture(t, "v1.2.0")
		cfg := f.config(t, "dev", time.Now)
		_, err := cfg.Apply(context.Background(), false, false)
		if !errors.Is(err, ErrDevelopmentBuild) {
			t.Fatalf("apply error = %v, want ErrDevelopmentBuild", err)
		}
		if f.binaryHits.Load() != 0 {
			t.Fatalf("development build downloaded a binary")
		}
	})

	t.Run("equal", func(t *testing.T) {
		f := newReleaseFixture(t, "v1.2.0")
		cfg := f.config(t, "v1.2.0", time.Now)
		got, err := cfg.Apply(context.Background(), false, false)
		if err != nil {
			t.Fatal(err)
		}
		if got.Updated || got.Available || f.binaryHits.Load() != 0 {
			t.Fatalf("equal apply = %#v, downloads = %d", got, f.binaryHits.Load())
		}
	})

	t.Run("newer", func(t *testing.T) {
		f := newReleaseFixture(t, "v1.2.0")
		cfg := f.config(t, "v2.0.0", time.Now)
		got, err := cfg.Apply(context.Background(), false, false)
		if err != nil {
			t.Fatal(err)
		}
		if got.Updated || got.Available || f.binaryHits.Load() != 0 {
			t.Fatalf("newer apply = %#v, downloads = %d", got, f.binaryHits.Load())
		}
	})
}

func TestCheckRejectsPrereleaseAsLatest(t *testing.T) {
	f := newReleaseFixture(t, "v1.3.0-rc.1")
	f.prerelease = true
	cfg := f.config(t, "v1.2.0", time.Now)
	if _, err := cfg.Check(context.Background(), false); err == nil || !strings.Contains(err.Error(), "not stable") {
		t.Fatalf("check error = %v", err)
	}
}

func TestCachedCheckThrottlesTransientFailures(t *testing.T) {
	f := newReleaseFixture(t, "v1.2.0")
	f.latestStatus = http.StatusServiceUnavailable
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	cfg := f.config(t, "v1.1.0", func() time.Time { return now })

	if _, err := cfg.Check(context.Background(), true); err == nil {
		t.Fatal("first failed check returned nil error")
	}
	if _, err := cfg.Check(context.Background(), true); err == nil {
		t.Fatal("cached failed check returned nil error")
	}
	if f.latestHits.Load() != 1 {
		t.Fatalf("cached failure made %d requests, want 1", f.latestHits.Load())
	}

	now = now.Add(61 * time.Minute)
	if _, err := cfg.Check(context.Background(), true); err == nil {
		t.Fatal("retried failed check returned nil error")
	}
	if f.latestHits.Load() != 2 {
		t.Fatalf("expired failure cache made %d requests, want 2", f.latestHits.Load())
	}
}

func TestChecksumForRequiresExactAsset(t *testing.T) {
	body := []byte(strings.Repeat("a", 64) + "  dyna_linux_arm64\n" + strings.Repeat("b", 64) + " *dyna_linux_amd64\n")
	got, err := checksumFor(body, "dyna_linux_amd64")
	if err != nil {
		t.Fatal(err)
	}
	if got != strings.Repeat("b", 64) {
		t.Fatalf("checksum = %q", got)
	}
	if _, err := checksumFor(body, "dyna_linux_amd"); err == nil {
		t.Fatal("partial asset name unexpectedly matched")
	}
}
