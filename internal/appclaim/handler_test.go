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

package appclaim

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCheckHandler(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(t.TempDir(), "jwks.json")
	if err := os.WriteFile(path, f.jwks, 0o600); err != nil {
		t.Fatal(err)
	}
	keys := &Keys{}
	if err := keys.Load(path); err != nil {
		t.Fatal(err)
	}
	// A file that is not valid keeps the last valid keys.
	_ = os.WriteFile(path, []byte("{"), 0o600)
	if keys.Load(path) == nil || keys.Load(path+".none") == nil || keys.set.Load() == nil {
		t.Fatal("A reload that fails must give an error and keep the keys.")
	}
	var got http.Header
	spans := &bytes.Buffer{}
	check := &Check{Config: testCfg, Keys: keys, Spans: NewSpanWriter(spans), App: testAud, Version: "shop-v1",
		Now: func() time.Time { return now },
		Next: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Clone()
			w.WriteHeader(http.StatusAccepted)
		})}
	token := f.sign(t, header(algES256, "e1"), claims(nil))
	call := func(method string, set func(*http.Request)) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/orders?secret=1", nil)
		r.Header.Set(CallerHeader, "forged")
		r.Header.Set(TenantHeader, "forged")
		set(r)
		w := httptest.NewRecorder()
		check.ServeHTTP(w, r)
		return w
	}

	w := call(http.MethodPost, func(r *http.Request) { r.Header.Set("Authorization", "bearer "+token) })
	if w.Code != http.StatusAccepted || got.Get(CallerHeader) != testCaller || got.Get(TenantHeader) != testTenant ||
		got.Get("Authorization") != "" {
		t.Errorf("valid claim: status %d, headers %v", w.Code, got)
	}
	got = nil
	for name, set := range map[string]func(*http.Request){
		"no header":    func(*http.Request) {},
		"basic scheme": func(r *http.Request) { r.Header.Set("Authorization", "Basic "+token) },
		"2 headers":    func(r *http.Request) { r.Header["Authorization"] = []string{"Bearer " + token, "Bearer " + token} },
		"bad token":    func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token+"x") },
	} {
		w := call("PROPFIND", set)
		if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") != "Bearer" || got != nil {
			t.Errorf("%s: status %d, the app got the request: %v", name, w.Code, got != nil)
		}
	}
	lines := strings.Split(strings.TrimSpace(spans.String()), "\n")
	var first Span
	if len(lines) != 5 || json.Unmarshal([]byte(lines[0]), &first) != nil {
		t.Fatalf("spans: %q", spans.String())
	}
	want := Span{Time: now.UTC(), Kind: "app-ingress", App: testAud, Version: "shop-v1", Method: http.MethodPost,
		Status: http.StatusAccepted, Outcome: outcomeOK, Caller: testCaller, Tenant: testTenant}
	if first != want {
		t.Errorf("span: %+v", first)
	}
	for _, secret := range []string{token, strings.Split(token, ".")[2], "forged", "secret=1", "PROPFIND"} {
		if strings.Contains(spans.String(), secret) {
			t.Errorf("A span has %q.", secret)
		}
	}
	if !strings.Contains(spans.String(), `"method":"OTHER","status":401,"outcome":"signature"`) {
		t.Errorf("The span of a refusal is not correct: %s", spans.String())
	}
}

func TestUpstreamAndStatus(t *testing.T) {
	var host, forwarded, caller string
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, forwarded, caller = r.Host, r.Header.Get("X-Forwarded-For"), r.Header.Get(CallerHeader)
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("tea"))
	}))
	defer app.Close()
	_, port, _ := net.SplitHostPort(app.Listener.Addr().String())
	n, _ := strconv.Atoi(port)
	r := httptest.NewRequest(http.MethodGet, "http://shop.example.com/", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.7")
	// A Connection header that names the label does not remove the label.
	r.Header.Set("Connection", CallerHeader)
	r.Header.Set(CallerHeader, "alice")
	rec := httptest.NewRecorder()
	w := &statusWriter{ResponseWriter: rec, status: http.StatusOK}
	Upstream(n).ServeHTTP(w, r)
	if w.status != http.StatusTeapot || rec.Body.String() != "tea" || host != "shop.example.com" ||
		!strings.HasPrefix(forwarded, "203.0.113.7, ") || w.Unwrap() != rec || caller != "alice" {
		t.Errorf("status %d, body %q, host %q, forwarded %q, caller %q", w.status, rec.Body.String(), host,
			forwarded, caller)
	}

	// With no app, the answer has a fixed text and no detail of the error.
	app.Close()
	rec = httptest.NewRecorder()
	Upstream(n).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://shop.example.com/", nil))
	if rec.Code != http.StatusBadGateway || rec.Body.String() != "The app did not answer.\n" {
		t.Errorf("status %d, body %q", rec.Code, rec.Body.String())
	}
}

// An aborted answer stops the handler with a panic. The request still has a span.
func TestCheckWritesSpanOnAbort(t *testing.T) {
	f := newFixture(t)
	keys := &Keys{}
	keys.set.Store(f.keys)
	spans := &bytes.Buffer{}
	check := &Check{Config: testCfg, Keys: keys, Spans: NewSpanWriter(spans),
		Next: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusPartialContent)
			panic(http.ErrAbortHandler)
		})}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+f.sign(t, header(algES256, "e1"), claims(nil)))
	func() {
		defer func() {
			if recover() != http.ErrAbortHandler { //nolint:errorlint // The panic value is the sentinel itself.
				t.Error("The abort did not go to the server.")
			}
		}()
		check.ServeHTTP(httptest.NewRecorder(), r)
	}()
	if !strings.Contains(spans.String(), `"status":206,"outcome":"ok"`) {
		t.Errorf("The span of an aborted answer is not correct: %q", spans.String())
	}
}
