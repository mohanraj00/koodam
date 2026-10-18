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
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fake is a small LiteLLM: keys by hash, a spend that settles after some reads, and call logs.
type fake struct {
	mu       sync.Mutex
	keys     map[string]map[string]any // hash -> generate body
	blocked  map[string]bool
	spend    []float64 // values returned by successive /key/info reads
	reads    int
	logs     []spendLog
	rawInURL bool
	echoKey  bool
	// routeBody, if set, makes /key/block and /key/info return this 404 body, as a
	// wrong route or a proxy in front of LiteLLM can.
	routeBody string
	// deleted holds the hashes that /key/info gives from the archive of deleted keys.
	deleted map[string]bool
	// blockNotFound makes /key/block return 404 for all keys.
	blockNotFound bool
	// echoMaster makes each response an error that contains the Authorization header.
	echoMaster bool
}

const testModel = "local-agent"

// notFoundBody is the LiteLLM v1.104.2 body for a missing key. The long message
// makes it longer than the 300 bytes that an error message keeps.
var notFoundBody = `{"error":{"message":"Key not found.` + strings.Repeat(" More detail.", 30) +
	`","type":"not_found_error","param":"key","code":"404"}}`

func newFake() *fake {
	return &fake{keys: map[string]map[string]any{}, blocked: map[string]bool{}, deleted: map[string]bool{}}
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer master" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.echoMaster {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad token ` + r.Header.Get("Authorization") + `"}`))
		return
	}
	if strings.Contains(r.URL.RawQuery, "sk-kd-") {
		f.rawInURL = true
	}
	switch r.URL.Path {
	case "/key/generate":
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		h := HashKey(b["key"].(string))
		if _, ok := f.keys[h]; ok || f.echoKey {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"duplicate key ` + b["key"].(string) + `"}`))
			return
		}
		f.keys[h] = b
		_, _ = w.Write([]byte(`{}`))
	case "/key/block":
		if f.routeBody != "" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(f.routeBody))
			return
		}
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		h := hashOf(b["key"])
		if _, ok := f.keys[h]; !ok || f.blockNotFound {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(notFoundBody))
			return
		}
		f.blocked[h] = true
		_, _ = w.Write([]byte(`{}`))
	case "/key/info":
		if f.routeBody != "" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(f.routeBody))
			return
		}
		h := r.URL.Query().Get("key")
		if f.deleted[h] {
			_ = json.NewEncoder(w).Encode(map[string]any{"info": map[string]any{"status": "deleted", "spend": 0.0}})
			return
		}
		if _, ok := f.keys[h]; !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(notFoundBody))
			return
		}
		s := 0.0
		if len(f.spend) > 0 {
			i := f.reads
			if i >= len(f.spend) {
				i = len(f.spend) - 1
			}
			s = f.spend[i]
		}
		f.reads++
		_ = json.NewEncoder(w).Encode(map[string]any{"info": map[string]any{"spend": s}})
	case "/spend/logs":
		_ = json.NewEncoder(w).Encode(f.logs)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Not Found"}`))
	}
}

func client(f *fake) (*LiteLLM, func()) {
	srv := httptest.NewServer(f)
	l := NewLiteLLM(srv.URL+"/", "master")
	l.RetryDelay, l.SpendPoll, l.SpendSettle, l.SpendWait = time.Millisecond, time.Millisecond, 3*time.Millisecond, time.Second
	return l, srv.Close
}

func spec(t *testing.T) KeySpec {
	v, err := NewKeyValue()
	if err != nil {
		t.Fatal(err)
	}
	return KeySpec{Value: v, Alias: "kd-uid", Model: testModel, Budget: 0.5, TTL: 960 * time.Second,
		Metadata: map[string]string{"agent": "a"}}
}

func TestCreateKeySendsBudgetModelAndTTL(t *testing.T) {
	f := newFake()
	l, stop := client(f)
	defer stop()
	s := spec(t)
	if err := l.CreateKey(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	b := f.keys[HashKey(s.Value)]
	if b["duration"] != "960s" || b["max_budget"] != 0.5 || b["key_alias"] != "kd-uid" {
		t.Fatalf("body %v", b)
	}
	if m := b["models"].([]any); len(m) != 1 || m[0] != testModel {
		t.Fatalf("models %v", m)
	}
}

func TestCreateKeyIsIdempotent(t *testing.T) {
	f := newFake()
	l, stop := client(f)
	defer stop()
	s := spec(t)
	for i := range 2 {
		if err := l.CreateKey(context.Background(), s); err != nil {
			t.Fatalf("try %d: %v", i, err)
		}
	}
}

func TestErrorsDoNotContainTheKey(t *testing.T) {
	f := newFake()
	f.echoKey = true
	l, stop := client(f)
	defer stop()
	s := spec(t)
	err := l.CreateKey(context.Background(), s)
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), s.Value) || !strings.Contains(err.Error(), "[key]") {
		t.Fatalf("error not redacted: %v", err)
	}
}

func TestErrorsDoNotContainTheMasterKey(t *testing.T) {
	f := newFake()
	f.echoMaster = true
	l, stop := client(f)
	defer stop()
	err := l.CreateKey(context.Background(), spec(t))
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(strings.ReplaceAll(err.Error(), "[master key]", ""), "master") {
		t.Fatalf("error not redacted: %v", err)
	}
}

func TestBlockKeyFailsOn404WhenTheKeyExists(t *testing.T) {
	f := newFake()
	f.blockNotFound = true
	l, stop := client(f)
	defer stop()
	s := spec(t)
	ctx := context.Background()
	if err := l.CreateKey(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := l.BlockKey(ctx, s.Value); err == nil {
		t.Fatal("a 404 for a key that exists must fail")
	}
}

func TestBlockKeyFailsOnARoute404(t *testing.T) {
	for _, body := range []string{`{"detail":"Not Found"}`, `{"error":"route not found"}`, `<html>404</html>`} {
		f := newFake()
		f.routeBody = body
		l, stop := client(f)
		if err := l.BlockKey(context.Background(), "sk-kd-missing"); err == nil {
			t.Errorf("a route 404 with the body %s must not count as a revoke", body)
		}
		stop()
	}
}

func TestBlockKeySucceedsForAMissingKey(t *testing.T) {
	l, stop := client(newFake())
	defer stop()
	if err := l.BlockKey(context.Background(), "sk-kd-missing"); err != nil {
		t.Fatalf("a missing key is revoked: %v", err)
	}
}

func TestBlockKeySucceedsForADeletedKey(t *testing.T) {
	f := newFake()
	f.deleted[HashKey("sk-kd-deleted")] = true
	l, stop := client(f)
	defer stop()
	if err := l.BlockKey(context.Background(), "sk-kd-deleted"); err != nil {
		t.Fatalf("a deleted key is revoked: %v", err)
	}
}

func TestUsageReportsSpendNotSettled(t *testing.T) {
	f := newFake()
	f.spend = []float64{0.01, 0.02, 0.03, 0.04, 0.05}
	l, stop := client(f)
	defer stop()
	l.SpendWait = 2 * time.Millisecond
	s := spec(t)
	ctx := context.Background()
	if err := l.CreateKey(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Usage(ctx, s.Value); !errors.Is(err, ErrSpendNotSettled) {
		t.Fatalf("got %v, want ErrSpendNotSettled", err)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var hits int
	dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer dst.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dst.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer src.Close()
	l := NewLiteLLM(src.URL, "master")
	l.Retries = 0
	if err := l.BlockKey(context.Background(), "sk-kd-x"); err == nil {
		t.Fatal("want an error for a redirect")
	}
	if hits != 0 {
		t.Fatal("the redirect was followed")
	}
}

func TestUsageWaitsForSettleAfterLateBatch(t *testing.T) {
	f := newFake()
	f.spend = []float64{0, 0, 0.03, 0.03}
	l, stop := client(f)
	defer stop()
	s := spec(t)
	ctx := context.Background()
	if err := l.CreateKey(ctx, s); err != nil {
		t.Fatal(err)
	}
	u, err := l.Usage(ctx, s.Value)
	if err != nil {
		t.Fatal(err)
	}
	if u.Spend != 0.03 {
		t.Fatalf("got spend %v, want 0.03", u.Spend)
	}
}

func TestKeyIsNotInURLs(t *testing.T) {
	f := newFake()
	f.spend = []float64{0.1, 0.1}
	l, stop := client(f)
	defer stop()
	s := spec(t)
	ctx := context.Background()
	if err := l.CreateKey(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Usage(ctx, s.Value); err != nil {
		t.Fatal(err)
	}
	if f.rawInURL {
		t.Fatal("a key value was in a URL")
	}
}

func TestBlockKey(t *testing.T) {
	f := newFake()
	l, stop := client(f)
	defer stop()
	s := spec(t)
	ctx := context.Background()
	if err := l.CreateKey(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := l.BlockKey(ctx, s.Value); err != nil {
		t.Fatal(err)
	}
	if !f.blocked[HashKey(s.Value)] {
		t.Fatal("key not blocked")
	}
	if err := l.BlockKey(ctx, "sk-kd-missing"); err != nil {
		t.Fatalf("a missing key must not fail: %v", err)
	}
}

func TestUsageWaitsForStableSpendAndCountsSuccessOnly(t *testing.T) {
	f := newFake()
	f.spend = []float64{0.01, 0.02, 0.03, 0.03}
	f.logs = []spendLog{
		{Status: "success", PromptTokens: 100, CompletionTokens: 10},
		{Status: "success", PromptTokens: 200, CompletionTokens: 20},
		{Status: "failure"},
	}
	l, stop := client(f)
	defer stop()
	s := spec(t)
	ctx := context.Background()
	if err := l.CreateKey(ctx, s); err != nil {
		t.Fatal(err)
	}
	u, err := l.Usage(ctx, s.Value)
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{Spend: 0.03, Calls: 2, FailedCalls: 1, InputTokens: 300, OutputTokens: 30}
	if u != want {
		t.Fatalf("got %+v, want %+v", u, want)
	}
}

func TestRetryOnConnectionErrorOnly(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // nothing listens: each try is a connection error
	l := NewLiteLLM("http://"+addr, "master")
	l.Retries, l.RetryDelay = 2, time.Millisecond
	err = l.BlockKey(context.Background(), "sk-kd-x")
	var he *HTTPError
	if err == nil || errors.As(err, &he) {
		t.Fatalf("want a connection error, got %v", err)
	}

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	l = NewLiteLLM(srv.URL, "master")
	l.RetryDelay = time.Millisecond
	if err := l.BlockKey(context.Background(), "sk-kd-x"); !errors.As(err, &he) || he.Status != 500 {
		t.Fatalf("want HTTP 500, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("an HTTP error was retried: %d calls", calls)
	}
}

func TestBlockKeyAndUsageByHash(t *testing.T) {
	f := newFake()
	f.spend = []float64{0.1, 0.1}
	l, stop := client(f)
	defer stop()
	s := spec(t)
	ctx := context.Background()
	if err := l.CreateKey(ctx, s); err != nil {
		t.Fatal(err)
	}
	h := HashKey(s.Value)
	if err := l.BlockKey(ctx, h); err != nil {
		t.Fatal(err)
	}
	if !f.blocked[h] {
		t.Fatal("key not blocked by its hash")
	}
	if _, err := l.Usage(ctx, h); err != nil {
		t.Fatal(err)
	}
}

func TestUsageReportsAMissingKey(t *testing.T) {
	l, stop := client(newFake())
	defer stop()
	if _, err := l.Usage(context.Background(), HashKey("sk-kd-missing")); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("got %v, want ErrKeyNotFound", err)
	}
}
