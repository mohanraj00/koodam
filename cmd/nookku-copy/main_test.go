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

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
}

// TestCopy copies a folder and a file, and checks the content and the modes.
func TestCopy(t *testing.T) {
	src, dst, bin := t.TempDir(), t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "lib", "nookku", "a.py"), "a", 0o644)
	write(t, filepath.Join(src, "plugin", "hooks", "hook.sh"), "h", 0o755)
	launcher := filepath.Join(t.TempDir(), "nookku")
	write(t, launcher, "l", 0o755)
	if err := run([]string{src, dst, launcher, filepath.Join(bin, "nookku")}); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]struct {
		text string
		mode os.FileMode
	}{
		filepath.Join(dst, "lib", "nookku", "a.py"):      {"a", 0o444},
		filepath.Join(dst, "plugin", "hooks", "hook.sh"): {"h", 0o555},
		filepath.Join(bin, "nookku"):                     {"l", 0o555},
	} {
		got, err := os.ReadFile(path)
		info, statErr := os.Stat(path)
		if err != nil || statErr != nil || string(got) != want.text || info.Mode().Perm() != want.mode {
			t.Errorf("%s: got %q with mode %v, want %q with mode %v", path, got, info.Mode().Perm(), want.text, want.mode)
		}
	}
}

// TestCopyRefuse checks that a link, an existing file and odd arguments stop the copy.
func TestCopyRefuse(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "a"), "a", 0o644)
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{src, t.TempDir()}); err == nil {
		t.Error("the copy accepted a link")
	}
	file := filepath.Join(t.TempDir(), "f")
	write(t, file, "f", 0o644)
	if err := run([]string{file, file}); err == nil {
		t.Error("the copy wrote over an existing file")
	}
	for _, args := range [][]string{nil, {src}} {
		if err := run(args); err == nil {
			t.Errorf("the copy accepted the arguments %q", args)
		}
	}
}
