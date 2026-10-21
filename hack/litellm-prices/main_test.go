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
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

// The input has fake prices. Only "keep-chat" and "keep-responses" are in the subset.
const testInput = `{
 "sample_spec": {"input_cost_per_token": 1, "output_cost_per_token": 1},
 "keep-chat": {"litellm_provider": "anthropic", "mode": "chat", "input_cost_per_token": 1e-6,
   "output_cost_per_token": 2e-6, "cache_read_input_token_cost": 1e-7, "max_tokens": 10},
 "keep-responses": {"litellm_provider": "openai", "mode": "responses", "input_cost_per_token": 1e-6,
   "output_cost_per_token": 2e-6},
 "other-provider": {"litellm_provider": "bedrock", "mode": "chat", "input_cost_per_token": 1e-6,
   "output_cost_per_token": 2e-6},
 "embedding": {"litellm_provider": "openai", "mode": "embedding", "input_cost_per_token": 1e-6,
   "output_cost_per_token": 0},
 "no-token-cost": {"litellm_provider": "openai", "mode": "chat", "output_cost_per_token": 2e-6},
 "openai/with-slash": {"litellm_provider": "openai", "mode": "chat", "input_cost_per_token": 1e-6,
   "output_cost_per_token": 2e-6}
}`

func writeInputs(t *testing.T, license string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	in, lic := filepath.Join(dir, "prices.json"), filepath.Join(dir, "LICENSE")
	if err := os.WriteFile(in, []byte(testInput), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lic, []byte(license), 0o600); err != nil {
		t.Fatal(err)
	}
	return in, lic
}

func TestRunKeepsOnlyTheChatModelsOfAnthropicAndOpenAI(t *testing.T) {
	in, lic := writeInputs(t, "MIT License\nCopyright (c) test\n")
	out := &bytes.Buffer{}
	if err := run(testCommit, in, lic, out); err != nil {
		t.Fatal(err)
	}
	var s subset
	if err := json.Unmarshal(out.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	var names []string
	for n := range s.Models {
		names = append(names, n)
	}
	slices.Sort(names)
	if want := []string{"keep-chat", "keep-responses"}; !slices.Equal(names, want) {
		t.Errorf("models: got %v, want %v", names, want)
	}
	if p := s.Models["keep-chat"]; p.CacheRead == nil || *p.CacheRead != 1e-7 || p.CacheWrite != nil {
		t.Errorf("keep-chat: got %+v", p)
	}
	if s.Commit != testCommit || !strings.Contains(s.Source, testCommit) || !strings.Contains(s.License, "MIT License") {
		t.Errorf("source %q, commit %q, license %q", s.Source, s.Commit, s.License)
	}
	if strings.Contains(out.String(), "max_tokens") {
		t.Error("the subset must have only the 4 prices")
	}
}

func TestRunRefusesABadCommitAndABadLicence(t *testing.T) {
	in, lic := writeInputs(t, "MIT License\n")
	for _, c := range []string{"", "main", strings.ToUpper(testCommit), testCommit + "0"} {
		if err := run(c, in, lic, &bytes.Buffer{}); err == nil {
			t.Errorf("commit %q: got no error", c)
		}
	}
	in, lic = writeInputs(t, "GPL\n")
	if err := run(testCommit, in, lic, &bytes.Buffer{}); err == nil {
		t.Error("a licence that is not MIT: got no error")
	}
}
