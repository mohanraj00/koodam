/*
Copyright 2026 Mohan Raj.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package harness downloads a coding harness binary from its vendor and checks its sha256.
// Koodam does not keep or redistribute a harness binary. Each pod downloads its own copy.
package harness

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The harness names.
const (
	ClaudeCode = "claude-code"
	Codex      = "codex"
)

// The vendor addresses. Rekor is the public Sigstore transparency log. It is not on GitHub.
const (
	ClaudeBase    = "https://downloads.claude.ai/claude-code-releases"
	CodexLatest   = "https://github.com/openai/codex/releases/latest"
	CodexDownload = "https://github.com/openai/codex/releases/download/"
	Rekor         = "https://rekor.sigstore.dev"
)

// The GOARCH values with a harness build.
const (
	amd64 = "amd64"
	arm64 = "arm64"
)

// maxBinary is the size limit of a download and of an extracted binary. The emptyDir of the run
// pod also has a size limit. It is a variable for the tests.
var maxBinary int64 = 1 << 30

// retries is the number of extra tries after a 429 or a 5xx response. The first wait is firstWait,
// and each next wait is 2 times longer. A Retry-After header replaces the wait. No wait is longer
// than maxWait. They are variables for the tests.
var (
	retries   = 4
	firstWait = time.Second
	maxWait   = 30 * time.Second
)

var versionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// ValidVersion is true for a version of the form 1.2.3.
func ValidVersion(v string) bool { return versionRE.MatchString(v) }

// Installer downloads a harness binary. Arch is a GOARCH value.
type Installer struct {
	HTTP          *http.Client
	ClaudeBase    string
	CodexLatest   string
	CodexDownload string
	Rekor         string
	Arch          string
}

// Install puts the binary of the harness in dir and returns the version. An empty version is
// the latest version. libc is "glibc" or "musl" and applies to Claude Code only. The binary has a
// dot name (see realName). The wrapper with the usual name runs koodam-project first (see wrap).
func (in *Installer) Install(ctx context.Context, name, version, libc, dir string) (string, error) {
	if version != "" && !ValidVersion(version) {
		return "", fmt.Errorf("the version %q is not of the form 1.2.3", version)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	var (
		got, bin string
		err      error
	)
	switch name {
	case ClaudeCode:
		bin = "claude"
		got, err = in.claude(ctx, version, libc, dir)
	case Codex:
		bin = "codex"
		got, err = in.codex(ctx, version, dir)
	default:
		return "", fmt.Errorf("the harness %q is not known", name)
	}
	if err != nil {
		return "", err
	}
	return got, wrap(dir, bin)
}

func (in *Installer) claude(ctx context.Context, version, libc, dir string) (string, error) {
	arch := map[string]string{amd64: "x64", arm64: "arm64"}[in.Arch]
	if arch == "" {
		return "", fmt.Errorf("claude-code has no build for %s", in.Arch)
	}
	plat := "linux-" + arch
	switch libc {
	case "", "glibc":
	case "musl":
		plat += "-musl"
	default:
		return "", fmt.Errorf("the libc %q is not glibc or musl", libc)
	}
	if version == "" {
		b, err := in.get(ctx, in.ClaudeBase+"/latest", 64)
		if err != nil {
			return "", err
		}
		version = strings.TrimSpace(string(b))
		if !ValidVersion(version) {
			return "", fmt.Errorf("the latest claude-code version %q is not of the form 1.2.3", version)
		}
	}
	b, err := in.get(ctx, in.ClaudeBase+"/"+version+"/manifest.json", 1<<20)
	if err != nil {
		return "", err
	}
	var m struct {
		Platforms map[string]struct {
			Checksum string `json:"checksum"`
		} `json:"platforms"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return "", fmt.Errorf("read the claude-code manifest: %w", err)
	}
	sum := m.Platforms[plat].Checksum
	if sum == "" {
		return "", fmt.Errorf("the claude-code %s manifest has no checksum for %s", version, plat)
	}
	url := in.ClaudeBase + "/" + version + "/" + plat + "/claude"
	if err := in.download(ctx, url, sum, filepath.Join(dir, realName("claude"))); err != nil {
		return "", err
	}
	return version, nil
}

func (in *Installer) codex(ctx context.Context, version, dir string) (string, error) {
	arch := map[string]string{amd64: "x86_64", arm64: "aarch64"}[in.Arch]
	if arch == "" {
		return "", fmt.Errorf("codex has no build for %s", in.Arch)
	}
	if version == "" {
		v, err := in.codexLatest(ctx)
		if err != nil {
			return "", err
		}
		version = v
	}
	bin := "codex-" + arch + "-unknown-linux-musl"
	rel := in.CodexDownload + "rust-v" + version + "/" + bin
	sum, err := in.codexSum(ctx, rel+".sigstore", version)
	if err != nil {
		return "", fmt.Errorf("codex %s %s: %w", version, bin, err)
	}
	return version, in.codexArchive(ctx, rel+".tar.gz", sum, bin, dir)
}

// codexLatest reads the latest version from the redirect of the releases/latest page. The page is
// not the GitHub API, so the API rate limit of 60 requests each hour does not apply.
func (in *Installer) codexLatest(ctx context.Context) (string, error) {
	hc := *in.client()
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := in.do(ctx, &hc, in.CodexLatest)
	if err != nil {
		return "", err
	}
	_ = resp.Body.Close()
	loc, err := neturl.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode/100 != 3 || path.Dir(loc.Path) != "/openai/codex/releases/tag" {
		return "", fmt.Errorf("GET %s: %s is not a redirect to a codex release tag", in.CodexLatest, resp.Status)
	}
	tag := path.Base(loc.Path)
	version, ok := strings.CutPrefix(tag, "rust-v")
	if !ok || !ValidVersion(version) {
		return "", fmt.Errorf("the latest codex release tag %q is not of the form rust-v1.2.3", tag)
	}
	return version, nil
}

// codexSum reads the sha256 of the codex binary from the cosign bundle at url and verifies the
// bundle (see verifyBundle). The bundle has the Rekor log entry of the signature. codexSum also
// reads the entry from the Rekor log at in.Rekor and requires the same entry. This shows that the
// entry is in the log now, not only that Rekor signed a promise to add it.
// Trust boundary: codexSum trusts the embedded Sigstore Fulcio and Rekor keys, the GitHub Actions
// OIDC issuer, and the openai/codex rust-release.yml workflow on the release tag. A party that
// controls the bundle and the archive, but not one of these, cannot pass the check.
func (in *Installer) codexSum(ctx context.Context, url, version string) (string, error) {
	b, err := in.get(ctx, url, 1<<20)
	if err != nil {
		return "", err
	}
	sum, p, err := verifyBundle(b, version)
	if err != nil {
		return "", err
	}
	b, err = in.get(ctx, in.Rekor+"/api/v1/log/entries?logIndex="+strconv.FormatInt(*p.LogIndex, 10), 1<<20)
	if err != nil {
		return "", err
	}
	var entries map[string]struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(b, &entries); err != nil {
		return "", fmt.Errorf("read the Rekor entry %d: %w", *p.LogIndex, err)
	}
	if len(entries) != 1 {
		return "", fmt.Errorf("the Rekor log has %d entries for the log index %d, not 1", len(entries), *p.LogIndex)
	}
	for _, e := range entries {
		if e.Body != p.Body {
			return "", fmt.Errorf("the Rekor entry %d is not the entry in the bundle", *p.LogIndex)
		}
	}
	return sum, nil
}

var sha256RE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// codexArchive downloads the archive and extracts the binary. The sum applies to the binary, not
// to the archive. It removes the archive.
func (in *Installer) codexArchive(ctx context.Context, url, sum, bin, dir string) error {
	tmp, err := os.CreateTemp(dir, ".codex-*.tar.gz")
	if err != nil {
		return err
	}
	archive := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(archive) }()
	if err := in.download(ctx, url, "", archive); err != nil {
		return err
	}
	return extract(archive, bin, sum, filepath.Join(dir, realName("codex")))
}

// get reads a small response body. A body larger than the limit is an error.
func (in *Installer) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	body, err := in.open(ctx, url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	b, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = fmt.Errorf("GET %s: the response is larger than %d bytes", url, limit)
	}
	return b, err
}

func (in *Installer) open(ctx context.Context, url string) (io.ReadCloser, error) {
	resp, err := in.do(ctx, in.client(), url)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp.Body, nil
}

func (in *Installer) client() *http.Client {
	if in.HTTP == nil {
		return http.DefaultClient
	}
	return in.HTTP
}

// do sends a GET. It tries again after a 429 or a 5xx response (see retries).
func (in *Installer) do(ctx context.Context, hc *http.Client, url string) (*http.Response, error) {
	wait := firstWait
	for try := 0; ; try++ {
		resp, err := in.doOnce(ctx, hc, url)
		if err != nil || try == retries || !retryable(resp.StatusCode) {
			return resp, err
		}
		d := retryAfter(resp.Header.Get("Retry-After"), wait, time.Now())
		_ = resp.Body.Close()
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, fmt.Errorf("GET %s: %w", url, ctx.Err())
		case <-t.C:
		}
		wait *= 2
	}
}

func (in *Installer) doOnce(ctx context.Context, hc *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "koodam-harness-install")
	resp, err := hc.Do(req)
	if err != nil {
		// A url.Error has the URL of the last redirect, which can have a signed query. Show the
		// first URL only.
		if ue, ok := errors.AsType[*neturl.Error](err); ok {
			err = ue.Err
		}
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	return resp, nil
}

func retryable(code int) bool { return code == http.StatusTooManyRequests || code/100 == 5 }

// retryAfter reads a Retry-After value in seconds or as an HTTP date. If the value is empty or
// not valid, it returns def. The result is from 0 to maxWait.
func retryAfter(v string, def time.Duration, now time.Time) time.Duration {
	d := def
	if n, err := strconv.Atoi(v); err == nil {
		d = time.Duration(n) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = t.Sub(now)
	}
	return min(max(d, 0), maxWait)
}

// download writes the body to dst only if its sha256 is sum. An empty sum skips the check. The file
// is executable.
func (in *Installer) download(ctx context.Context, url, sum, dst string) error {
	body, err := in.open(ctx, url)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	return writeChecked(io.LimitReader(body, maxBinary+1), sum, -1, dst)
}

// writeChecked writes r to a temporary file. It renames the file to dst only after the check.
// An empty sum skips the check. Use it only for an archive with a binary that has a checked sum.
// If size is not -1, the file must have that size.
func writeChecked(r io.Reader, sum string, size int64, dst string) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".download-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), r)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > maxBinary {
		return fmt.Errorf("the download for %s is larger than %d bytes", filepath.Base(dst), maxBinary)
	}
	if size != -1 && n != size {
		return fmt.Errorf("%s has %d bytes, not %d", filepath.Base(dst), n, size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); sum != "" && !strings.EqualFold(got, sum) {
		return fmt.Errorf("the sha256 of %s is %s, not %s", filepath.Base(dst), got, sum)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// extract writes the regular file with the base name bin from the tar.gz archive to dst, only if
// its sha256 is sum.
func extract(archive, bin, sum, dst string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()
	// The limit stops an archive that expands to much more than its binary.
	tr := tar.NewReader(io.LimitReader(zr, 2*maxBinary))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("the archive has no file %s", bin)
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg || path.Base(hdr.Name) != bin {
			continue
		}
		if hdr.Size <= 0 || hdr.Size > maxBinary {
			return fmt.Errorf("the file %s has %d bytes", bin, hdr.Size)
		}
		return writeChecked(tr, sum, hdr.Size, dst)
	}
}
