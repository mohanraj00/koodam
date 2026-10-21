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

package usage

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// remoteOutput runs RemoteScript with sh and the home dir home.
func remoteOutput(t *testing.T, home string) string {
	t.Helper()
	cmd := exec.Command("sh", "-s")
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "LC_ALL=C"}
	cmd.Stdin = strings.NewReader(RemoteScript)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the remote script failed: %v", err)
	}
	return string(out)
}

// The fixtures have FIXTURE-TEXT in each text, tool input, tool output and instruction. They have
// fake keys and counts in the text too, which the script must not count.
func TestRemoteScriptPrintsOnlyModelsAndCounts(t *testing.T) {
	home, err := filepath.Abs("testdata/home")
	if err != nil {
		t.Fatal(err)
	}
	out := remoteOutput(t, home)
	for _, bad := range []string{"FIXTURE-TEXT", "text-model", "tool-model", "toolu_", "req_", "/home/dev", "999", "777"} {
		if strings.Contains(out, bad) {
			t.Errorf("the remote output has %q:\n%s", bad, out)
		}
	}
	rows, err := Parse(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	want := []Row{
		{Harness: ClaudeCode, Model: "claude-fixture-unpriced", Input: 7, Output: 8},
		{Harness: ClaudeCode, Model: "claude-haiku-4-5", Input: 20, Output: 30},
		// 2 lines of 1 message count 1 time, with the largest output count.
		{Harness: ClaudeCode, Model: "claude-sonnet-4-5-20250929", Input: 10, Output: 50, CacheRead: 3000,
			CacheWrite: 200},
		// The last total of the session file. The input does not include the cached input.
		{Harness: Codex, Model: "gpt-5-mini", Input: 2000, Output: 250, CacheRead: 1000},
	}
	if !slices.Equal(rows, want) {
		t.Errorf("rows:\n got %+v\nwant %+v", rows, want)
	}
}

func TestRemoteScriptWithNoSessionFiles(t *testing.T) {
	out := remoteOutput(t, t.TempDir())
	rows, err := Parse(strings.NewReader(out))
	if out != "" || err != nil || len(rows) != 0 {
		t.Errorf("got output %q, rows %v, error %v", out, rows, err)
	}
}

func TestParseRefusesOtherLinesAndDoesNotShowThem(t *testing.T) {
	for _, in := range []string{
		"claude FIXTURE-TEXT\n",
		`claude "model":"a b"` + "\n",
		`other "koodam_line":1` + "\n",
		`claude "input_tokens":5` + "\n",
	} {
		_, err := Parse(strings.NewReader(in))
		if err == nil || strings.Contains(err.Error(), "FIXTURE") || strings.Contains(err.Error(), "a b") {
			t.Errorf("Parse(%q): got %v", in, err)
		}
	}
}

func TestParseSkipsAMessageWithNoID(t *testing.T) {
	rows, err := Parse(strings.NewReader("claude \"koodam_line\":1\nclaude \"model\":\"m\"\nclaude \"input_tokens\":5\n"))
	if err != nil || len(rows) != 0 {
		t.Errorf("got rows %v, error %v", rows, err)
	}
}
