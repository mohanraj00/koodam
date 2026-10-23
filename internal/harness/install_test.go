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

package harness

import (
	"archive/tar"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const pinned = "2.1.3"

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func tgz(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, f := range []struct {
		name string
		body []byte
	}{{"README.md", []byte("readme")}, {name, body}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body)),
			Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// vendor serves a Claude Code release 2.1.3, a Codex release rust-v0.9.1 and a Rekor log.
type vendor struct {
	arch      string
	helpers   map[string]*helperAsset
	latest    string
	claude    []byte
	claudeSum string
	codex     []byte
	codexBin  []byte
	location  string
	bundle    string
	noBundle  bool
	bundleSum string
	kind      string
	sigstore  *fakeSigstore
	status    int
	// The server sends failStatus and the Retry-After header retryAfter to the first fails requests.
	fails      int
	failStatus int
	retryAfter string
	// latestStatus and archiveStatus replace the usual status of /gh/latest and the archive.
	latestStatus  int
	archiveStatus int
	// rekor replaces the Rekor response. %[1]s in it is the entry in the bundle.
	rekor      string
	requests   []string
	srv        *httptest.Server
	installDir string
}

func newVendor(t *testing.T) *vendor {
	v := &vendor{arch: "x86_64", latest: "2.1.3\n", claude: []byte("claude binary"), codexBin: []byte("codex binary"),
		location: "https://github.com/openai/codex/releases/tag/rust-v0.9.1", kind: "hashedrekord"}
	v.claudeSum = sum(v.claude)
	v.codex = tgz(t, "codex-x86_64-unknown-linux-musl", v.codexBin)
	v.bundleSum, v.sigstore = sum(v.codexBin), newFakeSigstore(t)
	v.helpers = newHelperAssets(t, v.arch)
	var body string
	v.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.requests = append(v.requests, r.URL.Path)
		if v.fails > 0 {
			v.fails--
			if v.retryAfter != "" {
				w.Header().Set("Retry-After", v.retryAfter)
			}
			w.WriteHeader(v.failStatus)
			return
		}
		if v.status != 0 {
			w.WriteHeader(v.status)
			return
		}
		switch r.URL.Path {
		case "/claude/latest":
			_, _ = fmt.Fprint(w, v.latest)
		case "/claude/2.1.3/manifest.json":
			_, _ = fmt.Fprintf(w, `{"version":"2.1.3","platforms":{"linux-x64":{"checksum":%q},`+
				`"linux-arm64-musl":{"checksum":%q}}}`, v.claudeSum, v.claudeSum)
		case "/claude/2.1.3/linux-x64/claude", "/claude/2.1.3/linux-arm64-musl/claude":
			_, _ = w.Write(v.claude)
		case "/gh/latest":
			if v.location != "" {
				w.Header().Set("Location", v.location)
			}
			w.WriteHeader(cmp.Or(v.latestStatus, http.StatusFound))
		case "/dl/rust-v0.9.1/codex-" + v.arch + "-unknown-linux-musl.sigstore":
			if v.noBundle {
				http.NotFound(w, r)
				return
			}
			if v.bundle == "" {
				v.bundle, body = v.sigstore.bundle(t, v.bundleSum, func(o *fakeOpts) { o.kind = v.kind })
			}
			_, _ = fmt.Fprint(w, v.bundle)
		case "/dl/rust-v0.9.1/codex-" + v.arch + "-unknown-linux-musl.tar.gz":
			if v.archiveStatus != 0 {
				w.WriteHeader(v.archiveStatus)
				return
			}
			_, _ = w.Write(v.codex)
		case "/rekor/api/v1/log/entries":
			if r.URL.Query().Get("logIndex") != "42" {
				http.NotFound(w, r)
				return
			}
			_, _ = fmt.Fprintf(w, cmp.Or(v.rekor, `{"108e9186":{"body":%[1]q,"logIndex":42}}`), body)
		default:
			if !v.serveHelper(t, w, r, &body) {
				http.NotFound(w, r)
			}
		}
	}))
	t.Cleanup(v.srv.Close)
	v.installDir = t.TempDir()
	return v
}

func (v *vendor) install(arch, name, version, libc string) (string, error) {
	in := &Installer{HTTP: v.srv.Client(), ClaudeBase: v.srv.URL + "/claude", CodexLatest: v.srv.URL + "/gh/latest",
		CodexDownload: v.srv.URL + "/dl/", Rekor: v.srv.URL + "/rekor", Arch: arch}
	return in.Install(context.Background(), name, version, libc, v.installDir)
}

// files lists the names in the install folder, to find a temporary file that stays.
func (v *vendor) files(t *testing.T) []string {
	t.Helper()
	es, err := os.ReadDir(v.installDir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(es))
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

func expectBinary(t *testing.T, path string, want []byte) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, want) {
		t.Fatalf("%s has %q, want %q", path, b, want)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("%s has mode %v, want 0755", path, fi.Mode().Perm())
	}
}

func TestClaudeLatest(t *testing.T) {
	v := newVendor(t)
	got, err := v.install(amd64, ClaudeCode, "", "")
	if err != nil || got != pinned {
		t.Fatalf("install = %q, %v", got, err)
	}
	expectBinary(t, filepath.Join(v.installDir, ".claude-real"), v.claude)
	expectInstalled(t, v, "claude")
}

func TestClaudePinnedMusl(t *testing.T) {
	v := newVendor(t)
	got, err := v.install(arm64, ClaudeCode, pinned, "musl")
	if err != nil || got != pinned {
		t.Fatalf("install = %q, %v", got, err)
	}
	for _, p := range v.requests {
		if p == "/claude/latest" {
			t.Fatal("a pinned version read /latest")
		}
	}
	expectBinary(t, filepath.Join(v.installDir, ".claude-real"), v.claude)
	expectInstalled(t, v, "claude")
}

func TestClaudeChecksumMismatch(t *testing.T) {
	v := newVendor(t)
	v.claudeSum = sum([]byte("a different binary"))
	if _, err := v.install(amd64, ClaudeCode, "", ""); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("install error = %v, want a sha256 error", err)
	}
	if f := v.files(t); len(f) != 0 {
		t.Fatalf("a failed check left %v", f)
	}
}

func TestClaudeBadInput(t *testing.T) {
	v := newVendor(t)
	v.latest = "2.1.3/../../x"
	for _, c := range []struct{ arch, version, libc, want string }{
		{amd64, "", "", "latest claude-code version"},
		{amd64, "1.2.3;rm", "", "not of the form"},
		{"386", pinned, "", "no build"},
		{amd64, pinned, "uclibc", "libc"},
		{amd64, "2.1.4", "", "404"},
		{arm64, pinned, "", "no checksum for linux-arm64"},
	} {
		if _, err := v.install(c.arch, ClaudeCode, c.version, c.libc); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("install(%s, %q, %q) error = %v, want %q", c.arch, c.version, c.libc, err, c.want)
		}
	}
}

func TestCodex(t *testing.T) {
	for _, version := range []string{"", "0.9.1"} {
		v := newVendor(t)
		got, err := v.install(amd64, Codex, version, "")
		if err != nil || got != "0.9.1" {
			t.Fatalf("install(%q) = %q, %v", version, got, err)
		}
		expectBinary(t, filepath.Join(v.installDir, ".codex-real"), v.codexBin)
		expectInstalled(t, v, "codex")
		if version != "" && slices.Contains(v.requests, "/gh/latest") {
			t.Fatal("a pinned version read /latest")
		}
	}
}

func TestCodexBadRelease(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*vendor)
		want string
	}{
		{"wrong binary sha256", func(v *vendor) { v.bundleSum = sum([]byte("x")) }, "the sha256 of .codex-real is"},
		{"bundle entry mismatch with Rekor", func(v *vendor) { v.rekor = `{"a":{"body":"e30="}}%.0[1]s` },
			"the Rekor entry 42 is not the entry in the bundle"},
		{"bad bundle signature", func(v *vendor) {
			v.bundle, _ = v.sigstore.bundle(t, v.bundleSum, func(o *fakeOpts) { o.badSig = true })
		}, "signature is not valid"},
		{"missing bundle", func(v *vendor) { v.noBundle = true }, "codex 0.9.1 codex-x86_64-unknown-linux-musl"},
		{"no log index", func(v *vendor) { v.bundle = `{"rekorBundle":{"Payload":{"body":""}}}` },
			"no Rekor log index"},
		{"an intoto entry", func(v *vendor) { v.kind = "intoto" }, `kind "intoto"`},
		{"a short sha256", func(v *vendor) { v.bundleSum = "abc" }, "not a hashedrekord"},
		{"no Rekor entry", func(v *vendor) { v.rekor = `{}%.0[1]s` }, "0 entries for the log index 42"},
		{"2 Rekor entries", func(v *vendor) { v.rekor = `{"a":{"body":%[1]q},"b":{"body":%[1]q}}` },
			"2 entries for the log index 42"},
		{"latest gives 200", func(v *vendor) { v.latestStatus = http.StatusOK }, "200 OK is not"},
		{"latest gives 500", func(v *vendor) { v.latestStatus = http.StatusInternalServerError },
			"500 Internal Server Error is not"},
		{"archive gives 404", func(v *vendor) { v.archiveStatus = http.StatusNotFound }, "404"},
		{"archive gives 500", func(v *vendor) { v.archiveStatus = http.StatusInternalServerError }, "500"},
		{"bad tag", func(v *vendor) { v.location += "-alpha" },
			"not of the form rust-v1.2.3"},
		{"bad Location path", func(v *vendor) { v.location = "https://github.com/openai/codex/releases" },
			"not a redirect to a codex release tag"},
		{"no Location", func(v *vendor) { v.location = "" }, "not a redirect"},
		{"no binary in the archive", func(v *vendor) { v.codex = tgz(t, "other", []byte("x")) },
			"no file codex-x86_64-unknown-linux-musl"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fastRetry(t)
			v := newVendor(t)
			c.edit(v)
			if _, err := v.install(amd64, Codex, "", ""); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("install error = %v, want %q", err, c.want)
			}
			if f := v.files(t); len(f) != 0 {
				t.Fatalf("a failed install left %v", f)
			}
		})
	}
	v := newVendor(t)
	if _, err := v.install(arm64, Codex, "", ""); err == nil || !strings.Contains(err.Error(), "aarch64") {
		t.Fatalf("install error = %v, want no aarch64 bundle", err)
	}
	if _, err := v.install(amd64, "aider", "", ""); err == nil {
		t.Fatal("an unknown harness did not fail")
	}
}

func TestBadResponses(t *testing.T) {
	for _, c := range []struct {
		name    string
		harness string
		edit    func(*vendor)
		want    string
	}{
		{"a server error", ClaudeCode, func(v *vendor) { v.status = http.StatusInternalServerError }, "500"},
		{"a large latest file", ClaudeCode, func(v *vendor) { v.latest = strings.Repeat("9", 65) },
			"larger than 64 bytes"},
		{"a binary over the limit", ClaudeCode, func(*vendor) { maxBinary = 4 }, "larger than 4 bytes"},
		{"a truncated archive", Codex, func(v *vendor) {
			v.codex = v.codex[:len(v.codex)/2]
		}, "unexpected EOF"},
		{"an empty binary", Codex, func(v *vendor) {
			v.codex = tgz(t, "codex-x86_64-unknown-linux-musl", nil)
		}, "has 0 bytes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fastRetry(t)
			defer func(m int64) { maxBinary = m }(maxBinary)
			v := newVendor(t)
			c.edit(v)
			if _, err := v.install(amd64, c.harness, "", ""); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("install error = %v, want %q", err, c.want)
			}
			if f := v.files(t); len(f) != 0 {
				t.Fatalf("a failed install left %v", f)
			}
		})
	}
}

// fastRetry sets retry waits of 1 to 5 ms for 1 test.
func fastRetry(t *testing.T) {
	t.Helper()
	f, m := firstWait, maxWait
	firstWait, maxWait = time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { firstWait, maxWait = f, m })
}

func TestRetryThenInstall(t *testing.T) {
	fastRetry(t)
	for _, code := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		v := newVendor(t)
		v.fails, v.failStatus, v.retryAfter = 2, code, "0"
		got, err := v.install(amd64, ClaudeCode, "", "")
		if err != nil || got != "2.1.3" {
			t.Fatalf("%d: install = %q, %v, want 2.1.3", code, got, err)
		}
		if n := slices.Index(v.requests, "/claude/2.1.3/manifest.json"); n != 3 {
			t.Fatalf("%d: requests = %v, want 3 tries of /claude/latest first", code, v.requests)
		}
	}
}

func TestRetryStops(t *testing.T) {
	fastRetry(t)
	v := newVendor(t)
	v.fails, v.failStatus = 100, http.StatusTooManyRequests
	if _, err := v.install(amd64, ClaudeCode, "", ""); err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("install error = %v, want 429", err)
	}
	if len(v.requests) != retries+1 {
		t.Fatalf("%d requests, want %d", len(v.requests), retries+1)
	}

	v = newVendor(t)
	v.fails, v.failStatus = 1, http.StatusNotFound
	if _, err := v.install(amd64, ClaudeCode, "", ""); err == nil || len(v.requests) != 1 {
		t.Fatalf("a 404 got %d requests and error %v, want 1 request and an error", len(v.requests), err)
	}
}

func TestRetryContext(t *testing.T) {
	v := newVendor(t)
	v.fails, v.failStatus, v.retryAfter = 1, http.StatusTooManyRequests, "20"
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	in := &Installer{HTTP: v.srv.Client(), ClaudeBase: v.srv.URL + "/claude", Arch: amd64}
	start := time.Now()
	if _, err := in.Install(ctx, ClaudeCode, "", "", v.installDir); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("install error = %v, want a deadline error", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the install waited %v after the context end", d)
	}
	if len(v.requests) != 1 {
		t.Fatalf("requests = %v, want 1 request before the wait", v.requests)
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		v    string
		want time.Duration
	}{
		{"", 3 * time.Second},
		{"bad", 3 * time.Second},
		{"2", 2 * time.Second},
		{"-5", 3 * time.Second},
		{"10000000000", maxWait},
		{"600", maxWait},
		{now.Add(10 * time.Second).Format(http.TimeFormat), 10 * time.Second},
		{now.Add(-time.Hour).Format(http.TimeFormat), 0},
	} {
		if got := retryAfter(c.v, 3*time.Second, now); got != c.want {
			t.Errorf("retryAfter(%q) = %v, want %v", c.v, got, c.want)
		}
	}
}
