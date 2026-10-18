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

package gateway

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiteLLMLive runs against a real LiteLLM proxy. Set LITELLM_URL and LITELLM_MASTER_KEY_FILE.
// It makes a key, blocks it, checks that a call with the key fails, and reads the usage.
// It makes no model call that can succeed.
func TestLiteLLMLive(t *testing.T) {
	base, keyFile := os.Getenv("LITELLM_URL"), os.Getenv("LITELLM_MASTER_KEY_FILE")
	if base == "" || keyFile == "" {
		t.Skip("LITELLM_URL and LITELLM_MASTER_KEY_FILE are not set")
	}
	master, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	l := NewLiteLLM(base, strings.TrimSpace(string(master)))
	l.SpendWait = 10 * time.Second
	ctx := context.Background()
	s := spec(t)
	s.Alias = "kd-live-" + s.Value[len(s.Value)-8:]
	s.Model = os.Getenv("LITELLM_MODEL")
	if s.Model == "" {
		s.Model = "local-agent"
	}
	s.TTL = 2 * time.Minute
	if err := l.CreateKey(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := l.CreateKey(ctx, s); err != nil {
		t.Fatalf("second create: %v", err)
	}
	if err := l.BlockKey(ctx, s.Value); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, l.BaseURL+"/v1/chat/completions",
		strings.NewReader(`{"model":"`+s.Model+`","max_tokens":1,"messages":[{"role":"user","content":"x"}]}`))
	req.Header.Set("Authorization", "Bearer "+s.Value)
	req.Header.Set("Content-Type", "application/json")
	resp, err := l.HTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		t.Fatalf("a call with a blocked key got HTTP %d", resp.StatusCode)
	}
	u, err := l.Usage(ctx, s.Value)
	if err != nil {
		t.Fatal(err)
	}
	if u.Calls != 0 || u.InputTokens != 0 {
		t.Fatalf("a blocked key has usage %+v", u)
	}
}
