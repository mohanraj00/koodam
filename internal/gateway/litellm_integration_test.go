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
	"errors"
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
	// The test gateway uses proxy_batch_write_at: 1, as in the stage 2 spike.
	l.SpendSettle, l.SpendWait = 3*time.Second, 10*time.Second
	ctx := context.Background()
	s := spec(t)
	s.Alias = "kd-live-" + s.Value[len(s.Value)-8:]
	s.Model = os.Getenv("LITELLM_MODEL")
	if s.Model == "" {
		s.Model = testModel
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
	// This checks that the missing-key error of this LiteLLM version passes keyMissing.
	if err := l.BlockKey(ctx, "sk-kd-missing-"+s.Value[len(s.Value)-8:]); err != nil {
		t.Fatalf("block of a missing key: %v", err)
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

// TestLiteLLMCustomerLive runs against a real LiteLLM proxy. Set LITELLM_URL and
// LITELLM_MASTER_KEY_FILE. It makes a customer, changes its limits, reads its usage and deletes
// it. It also gives the Koodam budget to a customer that the gateway has with no budget. It makes
// no model call.
func TestLiteLLMCustomerLive(t *testing.T) {
	base, keyFile := os.Getenv("LITELLM_URL"), os.Getenv("LITELLM_MASTER_KEY_FILE")
	if base == "" || keyFile == "" {
		t.Skip("LITELLM_URL and LITELLM_MASTER_KEY_FILE are not set")
	}
	master, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	l := NewLiteLLM(base, strings.TrimSpace(string(master)))
	ctx := context.Background()
	id := "kd live/" + time.Now().UTC().Format("150405.000")
	t.Cleanup(func() { _ = l.DeleteCustomer(ctx, id) })
	if _, err := l.CustomerUsage(ctx, id); !errors.Is(err, ErrCustomerNotFound) {
		t.Fatalf("usage of a missing customer: %v", err)
	}
	// A customer with no budget, as the gateway makes it at the first call of a tenant label.
	if err := l.do(ctx, http.MethodPost, "/customer/new", map[string]string{"user_id": id}, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := l.CustomerUsage(ctx, id); !errors.Is(err, ErrCustomerNotFound) {
		t.Fatalf("usage of a customer with no budget: %v", err)
	}
	spec := CustomerSpec{ID: id, Budget: 1.5, Period: 24 * time.Hour, RequestsPerMinute: 120}
	for _, s := range []CustomerSpec{spec, spec, {ID: id, Period: time.Hour}} {
		if err := l.SetCustomer(ctx, s); err != nil {
			t.Fatalf("set %+v: %v", s, err)
		}
		info, err := l.customer(ctx, id)
		if err != nil || info.Budget == nil || !s.same(*info.Budget) {
			t.Fatalf("after set %+v: info %+v, error %v", s, info, err)
		}
		u, err := l.CustomerUsage(ctx, id)
		now := time.Now()
		// The gateway starts a period of an hour at the full hour, and a period of a day at 00:00 UTC.
		if err != nil || u.Spend != 0 || u.Requests != 0 || u.RequestsCapped || !u.PeriodEnd.After(now) || u.PeriodEnd.After(now.Add(s.Period)) ||
			!u.PeriodEnd.Equal(u.PeriodEnd.Truncate(s.Period)) {
			t.Fatalf("usage after set %+v: %+v, error %v", s, u, err)
		}
	}
	for range 2 {
		if err := l.DeleteCustomer(ctx, id); err != nil {
			t.Fatalf("delete: %v", err)
		}
	}
	if _, err := l.CustomerUsage(ctx, id); !errors.Is(err, ErrCustomerNotFound) {
		t.Fatalf("usage after the delete: %v", err)
	}
}
