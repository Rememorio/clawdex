// Package updater downloads and installs clawdex release binaries.
package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRepository = "Rememorio/clawdex"
	defaultAPIBaseURL = "https://api.github.com"
	defaultGitHubURL  = "https://github.com"
)

// Options controls the self-update workflow.
type Options struct {
	CurrentVersion string
	CheckOnly      bool
	Force          bool

	Repository     string
	APIBaseURL     string
	GitHubBaseURL  string
	ExecutablePath string
	GOOS           string
	GOARCH         string

	HTTPClient *http.Client
	Stdout     io.Writer
}

type release struct {
	TagName string  `json:"tag_name"`
	Assets  []asset `json:"assets"`
}

type asset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Run checks GitHub Releases for the latest clawdex binary and installs it.
func Run(ctx context.Context, opts Options) error {
	r := newRunner(opts)
	rel, err := r.latestRelease(ctx)
	if err != nil {
		return err
	}

	current := normalizeVersion(r.opts.CurrentVersion)
	latest := normalizeVersion(rel.TagName)
	cmp, comparable := compareVersions(current, latest)

	if r.opts.CheckOnly {
		if comparable && cmp == 0 {
			fmt.Fprintf(r.stdout(), "clawdex is up to date (%s)\n", current)
			return nil
		}
		if comparable && cmp > 0 {
			fmt.Fprintf(r.stdout(), "current version %s is newer than latest release %s\n", current, latest)
			return nil
		}
		fmt.Fprintf(r.stdout(), "update available: %s -> %s\n", current, latest)
		return nil
	}

	if comparable && cmp == 0 && !r.opts.Force {
		fmt.Fprintf(r.stdout(), "clawdex is up to date (%s)\n", current)
		return nil
	}
	if comparable && cmp > 0 && !r.opts.Force {
		fmt.Fprintf(r.stdout(), "current version %s is newer than latest release %s\n", current, latest)
		return nil
	}

	target, err := r.executablePath()
	if err != nil {
		return err
	}

	a, err := findAsset(rel.Assets, assetName(r.goos(), r.goarch()))
	if err != nil {
		return err
	}

	fmt.Fprintf(r.stdout(), "updating clawdex: %s -> %s\n", current, latest)
	if err := r.install(ctx, a, target); err != nil {
		return err
	}
	fmt.Fprintf(r.stdout(), "installed clawdex %s to %s\n", latest, target)
	fmt.Fprintln(r.stdout(), "restart the gateway to use the new binary.")
	return nil
}

type runner struct {
	opts Options
}

func newRunner(opts Options) *runner {
	if opts.CurrentVersion == "" {
		opts.CurrentVersion = "unknown"
	}
	return &runner{opts: opts}
}

func (r *runner) client() *http.Client {
	if r.opts.HTTPClient != nil {
		return r.opts.HTTPClient
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (r *runner) stdout() io.Writer {
	if r.opts.Stdout != nil {
		return r.opts.Stdout
	}
	return os.Stdout
}

func (r *runner) repository() string {
	if r.opts.Repository != "" {
		return r.opts.Repository
	}
	return defaultRepository
}

func (r *runner) apiBaseURL() string {
	if r.opts.APIBaseURL != "" {
		return strings.TrimRight(r.opts.APIBaseURL, "/")
	}
	return defaultAPIBaseURL
}

func (r *runner) githubBaseURL() string {
	if r.opts.GitHubBaseURL != "" {
		return strings.TrimRight(r.opts.GitHubBaseURL, "/")
	}
	return defaultGitHubURL
}

func (r *runner) goos() string {
	if r.opts.GOOS != "" {
		return r.opts.GOOS
	}
	return runtime.GOOS
}

func (r *runner) goarch() string {
	if r.opts.GOARCH != "" {
		return r.opts.GOARCH
	}
	return runtime.GOARCH
}

func (r *runner) executablePath() (string, error) {
	if r.opts.ExecutablePath != "" {
		return filepath.Abs(r.opts.ExecutablePath)
	}
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path, nil
	}
	return resolved, nil
}

func (r *runner) latestRelease(ctx context.Context) (release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", r.apiBaseURL(), r.repository())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return release{}, fmt.Errorf("create release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "clawdex/"+normalizeVersion(r.opts.CurrentVersion))
	if token := firstNonEmpty(os.Getenv("GH_TOKEN"), os.Getenv("GITHUB_TOKEN")); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := r.client().Do(req)
	if err != nil {
		return release{}, fmt.Errorf("check latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if r.opts.APIBaseURL == "" {
			if rel, fallbackErr := r.latestReleaseFromWeb(ctx); fallbackErr == nil {
				return rel, nil
			}
		}
		return release{}, fmt.Errorf("check latest release: GitHub returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return release{}, fmt.Errorf("parse latest release: %w", err)
	}
	if rel.TagName == "" {
		return release{}, errors.New("latest release response did not include tag_name")
	}
	return rel, nil
}

func (r *runner) latestReleaseFromWeb(ctx context.Context) (release, error) {
	url := fmt.Sprintf("%s/%s/releases/latest", r.githubBaseURL(), r.repository())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return release{}, fmt.Errorf("create release redirect request: %w", err)
	}
	req.Header.Set("User-Agent", "clawdex/"+normalizeVersion(r.opts.CurrentVersion))

	client := *r.client()
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := client.Do(req)
	if err != nil {
		return release{}, fmt.Errorf("check latest release redirect: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return release{}, fmt.Errorf("check latest release redirect: GitHub returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	location, err := resp.Location()
	if err != nil {
		return release{}, fmt.Errorf("parse latest release redirect: %w", err)
	}
	tag := path.Base(strings.TrimRight(location.Path, "/"))
	if tag == "." || tag == "/" || tag == "" {
		return release{}, fmt.Errorf("latest release redirect did not include a tag: %s", location.String())
	}

	name := assetName(r.goos(), r.goarch())
	downloadURL := fmt.Sprintf("%s/%s/releases/download/%s/%s", r.githubBaseURL(), r.repository(), tag, name)
	return release{
		TagName: tag,
		Assets: []asset{{
			Name:               name,
			BrowserDownloadURL: downloadURL,
		}},
	}, nil
}

func (r *runner) install(ctx context.Context, a asset, target string) error {
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("stat executable: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("executable path is a directory: %s", target)
	}

	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".clawdex-update-*")
	if err != nil {
		return fmt.Errorf("create temporary binary: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		tmp.Close()
		if cleanup {
			os.Remove(tmpPath)
		}
	}()

	n, err := r.download(ctx, a, tmp)
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("downloaded binary is empty")
	}
	if a.Size > 0 && n != a.Size {
		return fmt.Errorf("downloaded binary size mismatch: got %d bytes, want %d", n, a.Size)
	}

	mode := info.Mode().Perm()
	if mode&0o111 == 0 {
		mode |= 0o755
	}
	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("make temporary binary executable: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary binary: %w", err)
	}

	if err := os.Rename(tmpPath, target); err != nil {
		return fmt.Errorf("replace executable: %w", err)
	}
	cleanup = false
	return nil
}

func (r *runner) download(ctx context.Context, a asset, w io.Writer) (int64, error) {
	if a.BrowserDownloadURL == "" {
		return 0, fmt.Errorf("release asset %q has no download URL", a.Name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.BrowserDownloadURL, nil)
	if err != nil {
		return 0, fmt.Errorf("create download request: %w", err)
	}
	req.Header.Set("User-Agent", "clawdex/"+normalizeVersion(r.opts.CurrentVersion))

	resp, err := r.client().Do(req)
	if err != nil {
		return 0, fmt.Errorf("download %s: %w", a.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return 0, fmt.Errorf("download %s: server returned %s: %s", a.Name, resp.Status, strings.TrimSpace(string(body)))
	}

	n, err := io.Copy(w, resp.Body)
	if err != nil {
		return n, fmt.Errorf("write downloaded binary: %w", err)
	}
	return n, nil
}

func assetName(goos, goarch string) string {
	return fmt.Sprintf("clawdex-%s-%s", goos, goarch)
}

func findAsset(assets []asset, name string) (asset, error) {
	for _, a := range assets {
		if a.Name == name {
			return a, nil
		}
	}
	return asset{}, fmt.Errorf("latest release does not include asset %q", name)
}

func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "clawdex ")
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return "unknown"
	}
	return v
}

func compareVersions(a, b string) (int, bool) {
	av, okA := parseVersion(a)
	bv, okB := parseVersion(b)
	if !okA || !okB {
		if a == b {
			return 0, true
		}
		return 0, false
	}
	for i := range av {
		switch {
		case av[i] < bv[i]:
			return -1, true
		case av[i] > bv[i]:
			return 1, true
		}
	}
	return 0, true
}

func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	core := strings.SplitN(v, "-", 2)[0]
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
