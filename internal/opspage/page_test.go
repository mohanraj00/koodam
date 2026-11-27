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

package opspage

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func get(t *testing.T, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

func TestHandlerServesThePage(t *testing.T) {
	for path, kind := range map[string]string{
		"/": "text/html; charset=utf-8", "/page.js": "text/javascript; charset=utf-8",
		"/page.css": "text/css; charset=utf-8", "/kit/src/kit.js": "text/javascript; charset=utf-8",
		"/kit/LICENSE": "text/plain; charset=utf-8",
	} {
		w := get(t, http.MethodGet, path)
		h := w.Header()
		if w.Code != http.StatusOK || h.Get("Content-Type") != kind || w.Body.Len() == 0 {
			t.Fatalf("GET %s gave %d %q", path, w.Code, h.Get("Content-Type"))
		}
		if h.Get("Content-Security-Policy") != Policy || h.Get("X-Content-Type-Options") != "nosniff" ||
			h.Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("GET %s does not have the security headers", path)
		}
	}
	if w := get(t, http.MethodHead, "/"); w.Code != http.StatusOK {
		t.Fatalf("HEAD / gave %d", w.Code)
	}
}

func TestHandlerRefusals(t *testing.T) {
	for _, path := range []string{"/index.html", "/kit/", "/kit", "/static/page.js", "/mugham.pin.json", "/none.js"} {
		if w := get(t, http.MethodGet, path); w.Code != http.StatusNotFound {
			t.Errorf("GET %s gave %d, want 404", path, w.Code)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		w := get(t, method, "/")
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" ||
			w.Header().Get("Content-Security-Policy") != Policy {
			t.Errorf("%s / gave %d", method, w.Code)
		}
	}
}

func TestPolicyAndPageHaveNoInlineCode(t *testing.T) {
	if strings.Contains(Policy, "unsafe") || !strings.Contains(Policy, "script-src 'self';") {
		t.Fatal("the policy permits inline or eval code")
	}
	inline := regexp.MustCompile(`(?i)<script(?:[^>]*[^/])?>\s*[^<\s]|<style|\son[a-z]+=|\sstyle=|javascript:`)
	err := fs.WalkDir(files, "static", func(name string, d fs.DirEntry, err error) error {
		if err != nil || !strings.HasSuffix(name, ".html") {
			return err
		}
		data, err := files.ReadFile(name)
		if err == nil && inline.Match(data) {
			t.Errorf("%s has inline script or style", name)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPageKeepsTheTokenInMemory(t *testing.T) {
	data, err := files.ReadFile("static/page.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"localStorage", "sessionStorage", "indexedDB", "document.cookie", "console.log", "console.error"} {
		if strings.Contains(string(data), bad) {
			t.Errorf("page.js uses %s", bad)
		}
	}
}

func TestRoutes(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := Routes(api)
	for path, want := range map[string]int{
		"/v1/runs": http.StatusTeapot, "/v1/unknown": http.StatusTeapot, "/": http.StatusOK,
		"/page.js": http.StatusOK, "/v1": http.StatusTemporaryRedirect, "/none": http.StatusNotFound,
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != want {
			t.Errorf("GET %s gave %d, want %d", path, w.Code, want)
		}
	}
}

func pageFile(t *testing.T, name string) string {
	t.Helper()
	data, err := files.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The page reads the spend API only with GET requests to relative /v1/ paths of its own origin.
func TestPageSendsOnlyGETToV1(t *testing.T) {
	js := pageFile(t, "static/page.js")
	if n := strings.Count(js, "fetch("); n != 1 {
		t.Fatalf("page.js has %d fetch calls, want 1 in the request function", n)
	}
	for _, bad := range []string{"method:", "XMLHttpRequest", "sendBeacon", "WebSocket", "http://", "https://", "'/v1"} {
		if strings.Contains(js, bad) {
			t.Errorf("page.js has %q", bad)
		}
	}
	paths := regexp.MustCompile("(?:request|read|readAll|readJSON)\\((?:v, )?([`'][^`']*)")
	for _, m := range paths.FindAllStringSubmatch(js, -1) {
		if p := m[1][1:]; p != "" && !strings.HasPrefix(p, "v1/") && !strings.HasPrefix(p, "${v.path}") {
			t.Errorf("page.js reads %q, which is not a /v1/ path", p)
		}
	}
	if !strings.Contains(js, "`v1/runs/${encodeURIComponent(run.namespace)}/${encodeURIComponent(run.name)}`") {
		t.Error("page.js does not encode the namespace and the name in the run path")
	}
}

// The issue example: the page shows the last 200 log lines, as text.
func TestPageShowsTheLogTailAsText(t *testing.T) {
	js := pageFile(t, "static/page.js")
	for _, want := range []string{"const LOG_LINES = 200;", "/log?lines=${LOG_LINES}", "byId('log').textContent = lines.join('\\n');"} {
		if !strings.Contains(js, want) {
			t.Errorf("page.js does not have %q", want)
		}
	}
	for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "DOMParser", "createContextualFragment"} {
		if strings.Contains(js, bad) {
			t.Errorf("page.js uses %s", bad)
		}
	}
	// The line limit and the time limit errors come as the error field of a problem body.
	if !strings.Contains(js, "typeof body?.error === 'string'") {
		t.Error("page.js does not show the error text of the API")
	}
}

// The watch sends the token in the Authorization header. EventSource cannot send that header.
func TestPageWatchesTheRunWithTheTokenInTheHeader(t *testing.T) {
	js := pageFile(t, "static/page.js")
	for _, bad := range []string{"new EventSource", "token=", "access_token", "?token", "&token"} {
		if strings.Contains(js, bad) {
			t.Errorf("page.js has %q", bad)
		}
	}
	for _, want := range []string{
		"Authorization: `Bearer ${token}`", "request(`${v.path}/watch`, 'text/event-stream', connection.signal)",
		"new AbortController()", "v.connection?.abort()", "getReader()",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("page.js does not have %q", want)
		}
	}
}

// The watch parts of the API document: the retry field, the events status, deleted and end, the ping comment,
// and status 429 with Retry-After.
func TestPageReadsEachPartOfTheWatch(t *testing.T) {
	js := pageFile(t, "static/page.js")
	for _, want := range []string{
		"field === 'retry'", "name === 'status'", "name === 'deleted'", "name === 'end'", "line.startsWith(':')",
		"response.status === 429", "headers.get('Retry-After')", "const WATCH_RETRY_MS = 5000;",
		"before.phase !== s.phase) readView(v)",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("page.js does not have %q", want)
		}
	}
	// The latency target of a phase change is 5 s. The page shows the phase of a status event at once, with
	// no wait for a timer.
	onStatus := js[strings.Index(js, "function onStatus("):]
	onStatus = onStatus[:strings.Index(onStatus, "\n}\n")]
	if !strings.Contains(onStatus, "showCard();") || strings.Contains(onStatus, "setTimeout") {
		t.Error("onStatus does not show the new phase at once")
	}
	// A viewer that forgets the token or leaves the run stops the watch.
	if !strings.Contains(js, "token = null;\n  clearTimeout(timer);\n  closeView();") {
		t.Error("forget does not stop the watch")
	}
}

// The run view has the Job, the pods, the conditions and the log tail. The log box takes the focus, so a keyboard
// can scroll it, and it stays inside the page width.
func TestPageHasTheRunView(t *testing.T) {
	html := pageFile(t, "static/index.html")
	for _, want := range []string{
		`<div id="run-view" hidden>`, `<div id="job"></div>`, `<div id="pods"></div>`, `<div id="conditions"></div>`,
		`<div id="watch-state"></div>`, `<div id="log-state"></div>`,
		`<div id="log-box" class="log" role="region" aria-labelledby="log-title" tabindex="0"><pre id="log"></pre></div>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html does not have %s", want)
		}
	}
	css := pageFile(t, "static/page.css")
	for _, want := range []string{"max-inline-size: 100%", "overflow: auto", "white-space: pre-wrap", "overflow-wrap: anywhere"} {
		if !strings.Contains(css, want) {
			t.Errorf("page.css does not have %q", want)
		}
	}
}
