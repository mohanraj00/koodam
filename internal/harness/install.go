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
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The harness names.
const (
	ClaudeCode = "claude-code"
	Codex      = "codex"
)

// The vendor addresses.
const (
	ClaudeBase = "https://downloads.claude.ai/claude-code-releases"
	CodexAPI   = "https://api.github.com/repos/openai/codex/releases"
)

// maxBinary is the size limit of a download and of an extracted binary.
const maxBinary = 1 << 30

var versionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// ValidVersion is true for a version of the form 1.2.3.
func ValidVersion(v string) bool { return versionRE.MatchString(v) }

// Installer downloads a harness binary. Arch is a GOARCH value.
type Installer struct {
	HTTP       *http.Client
	ClaudeBase string
	CodexAPI   string
	Arch       string
}

// Install puts the binary of the harness in dir and returns the version. An empty version is
// the latest version. libc is "glibc" or "musl" and applies to Claude Code only.
func (in *Installer) Install(ctx context.Context, name, version, libc, dir string) (string, error) {
	if version != "" && !ValidVersion(version) {
		return "", fmt.Errorf("the version %q is not of the form 1.2.3", version)
	}
	switch name {
	case ClaudeCode:
		return in.claude(ctx, version, libc, dir)
	case Codex:
		return in.codex(ctx, version, dir)
	}
	return "", fmt.Errorf("the harness %q is not known", name)
}

func (in *Installer) claude(ctx context.Context, version, libc, dir string) (string, error) {
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[in.Arch]
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
	if err := in.download(ctx, url, sum, filepath.Join(dir, "claude")); err != nil {
		return "", err
	}
	return version, nil
}

func (in *Installer) codex(ctx context.Context, version, dir string) (string, error) {
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[in.Arch]
	if arch == "" {
		return "", fmt.Errorf("codex has no build for %s", in.Arch)
	}
	url := in.CodexAPI + "/latest"
	if version != "" {
		url = in.CodexAPI + "/tags/rust-v" + version
	}
	b, err := in.get(ctx, url, 4<<20)
	if err != nil {
		return "", err
	}
	var rel struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(b, &rel); err != nil {
		return "", fmt.Errorf("read the codex release: %w", err)
	}
	got := strings.TrimPrefix(rel.Tag, "rust-v")
	if !ValidVersion(got) || (version != "" && got != version) {
		return "", fmt.Errorf("the codex release tag %q is not the expected version", rel.Tag)
	}
	bin := "codex-" + arch + "-unknown-linux-musl"
	for _, a := range rel.Assets {
		if a.Name != bin+".tar.gz" {
			continue
		}
		sum, ok := strings.CutPrefix(a.Digest, "sha256:")
		if !ok {
			return "", fmt.Errorf("the codex asset %s has no sha256 digest", a.Name)
		}
		archive := filepath.Join(dir, ".codex.tar.gz")
		defer func() { _ = os.Remove(archive) }()
		if err := in.download(ctx, a.URL, sum, archive); err != nil {
			return "", err
		}
		if err := extract(archive, bin, filepath.Join(dir, "codex")); err != nil {
			return "", err
		}
		return got, nil
	}
	return "", fmt.Errorf("the codex release %s has no asset %s.tar.gz", rel.Tag, bin)
}

// get reads a small response body, to the limit.
func (in *Installer) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	body, err := in.open(ctx, url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	return io.ReadAll(io.LimitReader(body, limit))
}

func (in *Installer) open(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := in.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp.Body, nil
}

// download writes the body to dst only if its sha256 is sum. The file is executable.
func (in *Installer) download(ctx context.Context, url, sum, dst string) error {
	body, err := in.open(ctx, url)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	return writeChecked(io.LimitReader(body, maxBinary+1), sum, dst)
}

// writeChecked writes r to a temporary file. It renames the file to dst only after the check.
// An empty sum skips the check. Use it only for a file from an archive that was checked.
func writeChecked(r io.Reader, sum, dst string) error {
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
	if got := hex.EncodeToString(h.Sum(nil)); sum != "" && !strings.EqualFold(got, sum) {
		return fmt.Errorf("the sha256 of %s is %s, not %s", filepath.Base(dst), got, sum)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// extract writes the regular file with the base name bin from the checked tar.gz archive to dst.
func extract(archive, bin, dst string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("the archive has no file %s", bin)
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != bin {
			continue
		}
		if hdr.Size > maxBinary {
			return fmt.Errorf("the file %s is larger than %d bytes", bin, maxBinary)
		}
		return writeChecked(io.LimitReader(tr, hdr.Size), "", dst)
	}
}
