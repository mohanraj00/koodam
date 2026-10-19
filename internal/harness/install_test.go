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
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// vendor serves a Claude Code release 2.1.3 and a Codex release rust-v0.9.1.
type vendor struct {
	latest     string
	claude     []byte
	claudeSum  string
	codex      []byte
	codexSum   string
	codexTag   string
	requests   []string
	srv        *httptest.Server
	installDir string
}

func newVendor(t *testing.T) *vendor {
	v := &vendor{latest: "2.1.3\n", claude: []byte("claude binary"), codexTag: "rust-v0.9.1"}
	v.claudeSum = sum(v.claude)
	v.codex = tgz(t, "codex-x86_64-unknown-linux-musl", []byte("codex binary"))
	v.codexSum = "sha256:" + sum(v.codex)
	v.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.requests = append(v.requests, r.URL.Path)
		switch r.URL.Path {
		case "/claude/latest":
			_, _ = fmt.Fprint(w, v.latest)
		case "/claude/2.1.3/manifest.json":
			_, _ = fmt.Fprintf(w, `{"version":pinned,"platforms":{"linux-x64":{"checksum":%q},`+
				`"linux-arm64-musl":{"checksum":%q}}}`, v.claudeSum, v.claudeSum)
		case "/claude/2.1.3/linux-x64/claude", "/claude/2.1.3/linux-arm64-musl/claude":
			_, _ = w.Write(v.claude)
		case "/codex/latest", "/codex/tags/rust-v0.9.1":
			_, _ = fmt.Fprintf(w, `{"tag_name":%q,"assets":[{"name":"codex-x86_64-unknown-linux-musl.zst"},`+
				`{"name":"codex-x86_64-unknown-linux-musl.tar.gz","browser_download_url":%q,"digest":%q}]}`,
				v.codexTag, v.srv.URL+"/dl/codex.tar.gz", v.codexSum)
		case "/dl/codex.tar.gz":
			_, _ = w.Write(v.codex)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(v.srv.Close)
	v.installDir = t.TempDir()
	return v
}

func (v *vendor) install(arch, name, version, libc string) (string, error) {
	in := &Installer{HTTP: v.srv.Client(), ClaudeBase: v.srv.URL + "/claude", CodexAPI: v.srv.URL + "/codex",
		Arch: arch}
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
	expectBinary(t, filepath.Join(v.installDir, "claude"), v.claude)
	if f := v.files(t); len(f) != 1 {
		t.Fatalf("the install folder has %v, want only claude", f)
	}
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
	expectBinary(t, filepath.Join(v.installDir, "claude"), v.claude)
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
		expectBinary(t, filepath.Join(v.installDir, "codex"), []byte("codex binary"))
		if f := v.files(t); len(f) != 1 {
			t.Fatalf("the install folder has %v, want only codex", f)
		}
	}
}

func TestCodexBadRelease(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*vendor)
		want string
	}{
		{"digest mismatch", func(v *vendor) { v.codexSum = "sha256:" + sum([]byte("x")) }, "sha256"},
		{"no digest", func(v *vendor) { v.codexSum = "" }, "no sha256 digest"},
		{"bad tag", func(v *vendor) { v.codexTag = "rust-v0.9.1-alpha" }, "not the expected version"},
		{"no binary in the archive", func(v *vendor) {
			v.codex = tgz(t, "other", []byte("x"))
			v.codexSum = "sha256:" + sum(v.codex)
		}, "no file codex-x86_64-unknown-linux-musl"},
	} {
		t.Run(c.name, func(t *testing.T) {
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
	if _, err := v.install(arm64, Codex, "", ""); err == nil || !strings.Contains(err.Error(), "no asset") {
		t.Fatalf("install error = %v, want no asset", err)
	}
	if _, err := v.install(amd64, "aider", "", ""); err == nil {
		t.Fatal("an unknown harness did not fail")
	}
}
