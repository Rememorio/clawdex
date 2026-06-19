package updater

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunCheckOnlyReportsAvailableUpdate(t *testing.T) {
	server := newReleaseServer(t, "new-binary", true)
	var out strings.Builder

	err := Run(context.Background(), Options{
		CurrentVersion: "0.9.0",
		CheckOnly:      true,
		Repository:     "owner/repo",
		APIBaseURL:     server.URL,
		GOOS:           "linux",
		GOARCH:         "amd64",
		HTTPClient:     server.Client(),
		Stdout:         &out,
	})

	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := out.String(); !strings.Contains(got, "update available: 0.9.0 -> 1.0.0") {
		t.Fatalf("output = %q", got)
	}
}

func TestRunSkipsWhenUpToDate(t *testing.T) {
	server := newReleaseServer(t, "new-binary", true)
	exe := writeExecutable(t, "old-binary")
	var out strings.Builder

	err := Run(context.Background(), Options{
		CurrentVersion: "1.0.0",
		Repository:     "owner/repo",
		APIBaseURL:     server.URL,
		ExecutablePath: exe,
		GOOS:           "linux",
		GOARCH:         "amd64",
		HTTPClient:     server.Client(),
		Stdout:         &out,
	})

	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := readFile(t, exe); got != "old-binary" {
		t.Fatalf("executable content = %q", got)
	}
	if got := out.String(); !strings.Contains(got, "clawdex is up to date (1.0.0)") {
		t.Fatalf("output = %q", got)
	}
}

func TestRunCheckOnlyIgnoresForceWhenUpToDate(t *testing.T) {
	server := newReleaseServer(t, "new-binary", true)
	var out strings.Builder

	err := Run(context.Background(), Options{
		CurrentVersion: "1.0.0",
		CheckOnly:      true,
		Force:          true,
		Repository:     "owner/repo",
		APIBaseURL:     server.URL,
		GOOS:           "linux",
		GOARCH:         "amd64",
		HTTPClient:     server.Client(),
		Stdout:         &out,
	})

	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := out.String(); !strings.Contains(got, "clawdex is up to date (1.0.0)") {
		t.Fatalf("output = %q", got)
	}
}

func TestRunInstallsLatestAsset(t *testing.T) {
	server := newReleaseServer(t, "new-binary", true)
	exe := writeExecutable(t, "old-binary")
	var out strings.Builder

	err := Run(context.Background(), Options{
		CurrentVersion: "0.9.0",
		Repository:     "owner/repo",
		APIBaseURL:     server.URL,
		ExecutablePath: exe,
		GOOS:           "linux",
		GOARCH:         "amd64",
		HTTPClient:     server.Client(),
		Stdout:         &out,
	})

	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := readFile(t, exe); got != "new-binary" {
		t.Fatalf("executable content = %q", got)
	}
	info, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("installed executable mode = %v, want executable bit", info.Mode().Perm())
	}
	if got := out.String(); !strings.Contains(got, "installed clawdex 1.0.0") {
		t.Fatalf("output = %q", got)
	}
}

func TestRunErrorsWhenAssetMissing(t *testing.T) {
	server := newReleaseServer(t, "new-binary", false)
	exe := writeExecutable(t, "old-binary")

	err := Run(context.Background(), Options{
		CurrentVersion: "0.9.0",
		Repository:     "owner/repo",
		APIBaseURL:     server.URL,
		ExecutablePath: exe,
		GOOS:           "linux",
		GOARCH:         "amd64",
		HTTPClient:     server.Client(),
	})

	if err == nil {
		t.Fatal("Run() error = nil")
	}
	if !strings.Contains(err.Error(), `asset "clawdex-linux-amd64"`) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestDefaultDownloadClientHasNoTotalTimeout(t *testing.T) {
	r := newRunner(Options{})

	if got := r.client().Timeout; got != defaultRequestTimeout {
		t.Fatalf("client timeout = %v, want %v", got, defaultRequestTimeout)
	}
	if got := r.downloadClient().Timeout; got != 0 {
		t.Fatalf("download client timeout = %v, want 0", got)
	}
}

func TestDownloadAppliesLongRequestDeadline(t *testing.T) {
	var gotDeadline time.Time
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok {
			t.Fatal("download request has no deadline")
		}
		gotDeadline = deadline
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("new-binary")),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}
	r := newRunner(Options{
		CurrentVersion: "0.9.0",
		HTTPClient:     client,
	})

	var out strings.Builder
	n, err := r.download(context.Background(), asset{
		Name:               "clawdex-linux-amd64",
		BrowserDownloadURL: "https://example.test/clawdex-linux-amd64",
	}, &out)
	if err != nil {
		t.Fatalf("download() error = %v", err)
	}
	if n != int64(len("new-binary")) {
		t.Fatalf("download() bytes = %d", n)
	}
	remaining := time.Until(gotDeadline)
	if remaining < defaultDownloadTimeout-time.Minute || remaining > defaultDownloadTimeout {
		t.Fatalf("download deadline remaining = %v, want near %v", remaining, defaultDownloadTimeout)
	}
}

func TestLatestReleaseFromWebBuildsDownloadURLFromRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/owner/repo/releases/latest" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/owner/repo/releases/tag/v1.2.3", http.StatusFound)
	}))
	t.Cleanup(server.Close)

	r := newRunner(Options{
		CurrentVersion: "1.0.0",
		Repository:     "owner/repo",
		GitHubBaseURL:  server.URL,
		GOOS:           "linux",
		GOARCH:         "arm64",
		HTTPClient:     server.Client(),
	})

	rel, err := r.latestReleaseFromWeb(context.Background())
	if err != nil {
		t.Fatalf("latestReleaseFromWeb() error = %v", err)
	}
	if rel.TagName != "v1.2.3" {
		t.Fatalf("TagName = %q, want v1.2.3", rel.TagName)
	}
	if len(rel.Assets) != 1 {
		t.Fatalf("len(Assets) = %d, want 1", len(rel.Assets))
	}
	if rel.Assets[0].Name != "clawdex-linux-arm64" {
		t.Fatalf("asset name = %q", rel.Assets[0].Name)
	}
	wantURL := server.URL + "/owner/repo/releases/download/v1.2.3/clawdex-linux-arm64"
	if rel.Assets[0].BrowserDownloadURL != wantURL {
		t.Fatalf("download URL = %q, want %q", rel.Assets[0].BrowserDownloadURL, wantURL)
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want int
		ok   bool
	}{
		{name: "equal", a: "1.0.0", b: "1.0.0", want: 0, ok: true},
		{name: "newer", a: "1.2.0", b: "1.1.9", want: 1, ok: true},
		{name: "older", a: "0.9.9", b: "1.0.0", want: -1, ok: true},
		{name: "tag prefix", a: "v1.0.0", b: "1.0.0", want: 0, ok: true},
		{name: "unknown equal", a: "dev", b: "dev", want: 0, ok: true},
		{name: "unknown different", a: "dev", b: "1.0.0", want: 0, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := compareVersions(normalizeVersion(tt.a), normalizeVersion(tt.b))
			if got != tt.want || ok != tt.ok {
				t.Fatalf("compareVersions() = (%d, %v), want (%d, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func newReleaseServer(t *testing.T, binary string, includeTargetAsset bool) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/releases/latest":
			assetName := "clawdex-darwin-arm64"
			if includeTargetAsset {
				assetName = "clawdex-linux-amd64"
			}
			fmt.Fprintf(
				w,
				`{"tag_name":"v1.0.0","assets":[{"name":%q,"size":%d,"browser_download_url":%q}]}`,
				assetName,
				len(binary),
				server.URL+"/download/clawdex",
			)
		case "/download/clawdex":
			fmt.Fprint(w, binary)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func writeExecutable(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clawdex")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
