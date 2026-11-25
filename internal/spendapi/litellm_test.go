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

package spendapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// inMemoryGateway serves the same responses when the test environment cannot bind a socket.
type inMemoryGateway struct{ http.Handler }

func (h inMemoryGateway) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Result(), nil
}

func useGateway(t testing.TB, l *liteLLM, handler http.HandlerFunc) {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Log("Use an in-process HTTP transport because a loopback listener did not start.")
		l.HTTP.Transport = inMemoryGateway{handler}
		return
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	server.Start()
	t.Cleanup(server.Close)
	l.BaseURL = server.URL
}

var (
	testHash     = strings.Repeat("0a", 32)
	testDayStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	testDayEnd   = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
)

func newTestLiteLLM(t testing.TB, handler http.HandlerFunc) *liteLLM {
	t.Helper()
	l, err := newLiteLLM("http://litellm.example", "master")
	if err != nil {
		t.Fatal(err)
	}
	useGateway(t, l, handler)
	return l
}

func TestNewLiteLLM(t *testing.T) {
	for _, address := range []string{
		"", "litellm.example", "ftp://litellm.example", "http://", "http://user:pass@litellm.example",
		"http://litellm.example?", "http://litellm.example?a=b", "http://litellm.example#a",
	} {
		if _, err := newLiteLLM(address, "master"); err == nil {
			t.Fatalf("newLiteLLM(%q) accepted a bad address", address)
		}
	}
	if _, err := newLiteLLM("http://litellm.example", ""); err == nil {
		t.Fatal("newLiteLLM accepted an empty master key")
	}
	l, err := newLiteLLM("https://litellm.example/", "master")
	if err != nil || l.BaseURL != "https://litellm.example" || cap(l.slots) != maxGatewayReads {
		t.Fatal("newLiteLLM did not make the client")
	}
}

func TestLiteLLMSpend(t *testing.T) {
	l := newTestLiteLLM(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer master" {
			t.Error("the gateway did not receive an authenticated GET")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case q.Get("key") == testHash:
			_, _ = fmt.Fprint(w, `{"info":{"spend":1.5}}`)
		case q.Get("api_key") == testHash && q.Get("summarize") == "true" &&
			q.Get("start_date") == "2026-01-01" && q.Get("end_date") == "2026-02-01":
			_, _ = fmt.Fprint(w, `[
				{"startTime":"2026-01-01", "spend":0.25},
				{"startTime":"2026-01-31", "spend":0.5},
				{"startTime":"2026-02-01", "spend":5}
			]`)
		default:
			t.Error("the gateway received an unexpected query")
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	spend, err := l.lifetime(t.Context(), testHash)
	if err != nil || spend == nil || *spend != 1.5 {
		t.Fatal("the lifetime spend does not match the gateway")
	}
	period, err := l.period(t.Context(), testHash, testDayStart, testDayEnd)
	if err != nil || period != 0.75 {
		t.Fatalf("period spend = %g, %v, want 0.75 without the day after the range", period, err)
	}
}

func TestLiteLLMMissingKey(t *testing.T) {
	for body, want := range map[string]error{
		`{"error":{"type":"not_found_error","param":"key"}}`:   errMissingKey,
		`{"error":{"type":"not_found_error","param":"route"}}`: errGateway,
		`not JSON`: errGateway,
	} {
		l := newTestLiteLLM(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, body)
		})
		if _, err := l.lifetime(t.Context(), testHash); !errors.Is(err, want) {
			t.Fatalf("lifetime error = %v, want %v", err, want)
		}
		// Only the key read can find a missing key.
		if _, err := l.period(t.Context(), testHash, testDayStart, testDayEnd); !errors.Is(err, errGateway) {
			t.Fatal("a missing route on the spend logs is not a gateway error")
		}
	}
}

func TestLiteLLMBadData(t *testing.T) {
	for _, body := range []string{
		`{}`, `null`, `{"info":{"spend":"1"}}`, `[{"startTime":"January","spend":1}]`,
		`[{"startTime":"2026-01-01"}]`, `[{"startTime":"2026-01-01","spend":1e400}]`,
		`[{"startTime":"2026-01-01","spend":1.7e308},{"startTime":"2026-01-02","spend":1.7e308}]`,
	} {
		l := newTestLiteLLM(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, body) })
		if _, err := l.lifetime(t.Context(), testHash); err != errGateway {
			t.Fatalf("lifetime accepted %s", body)
		}
		if _, err := l.period(t.Context(), testHash, testDayStart, testDayEnd); err != errGateway {
			t.Fatalf("period accepted %s", body)
		}
	}
	l := newTestLiteLLM(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"info":{"spend":1}}`, testHash)
	})
	if _, err := l.lifetime(t.Context(), testHash); err != errGateway {
		t.Fatal("lifetime accepted a server error")
	}
}

func TestLiteLLMDoesNotFollowRedirects(t *testing.T) {
	l := newTestLiteLLM(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/key/info" {
			t.Error("the client followed a redirect")
		}
		w.Header().Set("Location", "/moved")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	if _, err := l.lifetime(t.Context(), testHash); err != errGateway {
		t.Fatal("lifetime accepted a redirect")
	}
}

func TestLiteLLMReadLimit(t *testing.T) {
	l := newTestLiteLLM(t, func(http.ResponseWriter, *http.Request) {
		t.Error("the read started with no free slot")
	})
	for range maxGatewayReads {
		l.slots <- struct{}{}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := l.lifetime(ctx, testHash); !errors.Is(err, context.Canceled) {
		t.Fatal("the read did not stop with the request")
	}
}

func TestKeyHash(t *testing.T) {
	if hash, err := keyHash(testHash); err != nil || hash != testHash {
		t.Fatal("keyHash refused a valid hash")
	}
	for _, bad := range []string{"", testHash[1:], strings.ToUpper(testHash), testHash[1:] + "g", testHash + "0"} {
		if _, err := keyHash(bad); err == nil {
			t.Fatalf("keyHash accepted %q", bad)
		}
	}
}
