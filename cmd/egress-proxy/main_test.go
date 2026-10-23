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
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

func TestList(t *testing.T) {
	if got := list(" 443, ,8443,"); !slices.Equal(got, []string{"443", "8443"}) {
		t.Fatalf("got %q", got)
	}
	if got := list(""); got != nil {
		t.Fatalf("got %q", got)
	}
}

func TestProbes(t *testing.T) {
	var ready atomic.Bool
	m := probeMux(&ready)
	code := func(path string) int {
		w := httptest.NewRecorder()
		m.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w.Code
	}
	if code("/healthz") != http.StatusOK || code("/readyz") != http.StatusServiceUnavailable {
		t.Fatal("the probes before the pod caches sync are wrong")
	}
	ready.Store(true)
	if code("/readyz") != http.StatusOK {
		t.Fatal("/readyz is not ready after the sync")
	}
}

func TestParseGateway(t *testing.T) {
	const host = "koodam-litellm.koodam-system.svc:4000"
	if u, err := parseGateway("http://" + host); err != nil || u.Host != host {
		t.Fatalf("got %v %v", u, err)
	}
	for _, bad := range []string{"ftp://gw", "http:///path", "gw:4000", "http://user:pass@gw",
		"http://gw?key=x", "http://gw#x", "%"} {
		if _, err := parseGateway(bad); err == nil {
			t.Errorf("parseGateway(%q) gave no error", bad)
		} else if strings.Contains(err.Error(), "pass") {
			t.Errorf("the error of %q holds the URL: %v", bad, err)
		}
	}
}
