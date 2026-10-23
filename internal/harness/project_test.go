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
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mohanraj00/koodam/images/workspace"
)

// expectInstalled checks the binary, the wrapper, the script and the Codex helpers.
func expectInstalled(t *testing.T, v *vendor, bin string) {
	t.Helper()
	want := []string{"." + bin + "-real", bin, projectScript}
	if bin == Codex {
		want = append(want, codeModeHost, bubblewrap)
		slices.Sort(want)
	}
	if f := v.files(t); !slices.Equal(f, want) {
		t.Fatalf("the install folder has %v, want %v", f, want)
	}
	expectBinary(t, filepath.Join(v.installDir, projectScript), workspace.ProjectScript)
}

// TestWrap runs the wrapper with a fake binary. The wrapper must make the project files in the
// current folder, then run the binary with the same arguments, stdin and exit code.
func TestWrap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "it's bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := "#!/bin/sh\nprintf '%s|' \"$@\"\ncat\nexit 7\n"
	if err := os.WriteFile(filepath.Join(dir, realName("claude")), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := wrap(dir, "claude"); err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("agents\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(proj, ".agents", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(dir, "claude"), "-p", "a b", "")
	cmd.Dir = proj
	cmd.Stdin = strings.NewReader("in")
	out, err := cmd.Output()
	if ee, ok := errors.AsType[*exec.ExitError](err); !ok || ee.ExitCode() != 7 {
		t.Fatalf("the wrapper error = %v, want the exit code 7", err)
	}
	if string(out) != "-p|a b||in" {
		t.Fatalf("the wrapper wrote %q, want the arguments and stdin of the binary", out)
	}
	if b, err := os.ReadFile(filepath.Join(proj, "CLAUDE.md")); err != nil || string(b) != "@AGENTS.md\n" {
		t.Fatalf("CLAUDE.md = %q, %v", b, err)
	}
	if l, err := os.Readlink(filepath.Join(proj, ".claude", "skills")); err != nil || l != "../.agents/skills" {
		t.Fatalf(".claude/skills links to %q, %v", l, err)
	}
}

func TestQuote(t *testing.T) {
	out, err := exec.Command("/bin/sh", "-c", "printf %s "+quote(`a'b "$c" \d`)).Output()
	if err != nil || string(out) != `a'b "$c" \d` {
		t.Fatalf("sh read the quoted text as %q, %v", out, err)
	}
}
