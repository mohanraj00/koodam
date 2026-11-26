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
