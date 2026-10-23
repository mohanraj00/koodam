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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

const (
	codeModeHost = "codex-code-mode-host"
	bubblewrap   = "bwrap"
	codexX86     = "x86_64"
	codexARM     = "aarch64"
)

// codex installs the CLI and its helpers from the same release. Each binary must pass the
// signature, exact workflow identity, Rekor entry and sha256 checks before any file is published.
func (in *Installer) codex(ctx context.Context, version, dir string) (string, error) {
	arch := map[string]string{amd64: codexX86, arm64: codexARM}[in.Arch]
	if arch == "" {
		return "", fmt.Errorf("codex has no build for %s", in.Arch)
	}
	if version == "" {
		var err error
		version, err = in.codexLatest(ctx)
		if err != nil {
			return "", err
		}
	}
	stage, err := os.MkdirTemp(dir, ".codex-release-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	// Publish the CLI last, so a partial publication does not replace it.
	// A publication error can leave verified helpers in place. The init container then fails.
	binaries := []string{Codex, codeModeHost, bubblewrap}
	digests := make(map[string]string, len(binaries))
	for _, name := range binaries {
		asset := name + "-" + arch + "-unknown-linux-musl"
		rel := in.CodexDownload + "rust-v" + version + "/" + asset
		sum, err := in.codexSum(ctx, rel+".sigstore", version)
		if err == nil {
			if previous, exists := digests[sum]; exists {
				return "", fmt.Errorf("codex %s %s: the sha256 is also used by %s", version, asset, previous)
			}
			digests[sum] = asset
			dst := name
			if name == Codex {
				dst = realName(name)
			}
			err = in.codexArchive(ctx, rel+".tar.gz", sum, asset, filepath.Join(stage, dst))
		}
		if err != nil {
			return "", fmt.Errorf("codex %s %s: %w", version, asset, err)
		}
	}
	for _, name := range slices.Backward(binaries) {
		if name == Codex {
			name = realName(name)
		}
		if err := os.Rename(filepath.Join(stage, name), filepath.Join(dir, name)); err != nil {
			return "", err
		}
	}
	return version, nil
}
