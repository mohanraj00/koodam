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

package egress

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

const (
	testHost = "example.com"
	// testKey is a fake gateway key. The test checks that no span holds it.
	testKey = "sk-test-not-a-real-key"
)

type chanSink chan Span

func (c chanSink) Write(sp Span) { c <- sp }

func (c chanSink) next(t *testing.T) Span {
	t.Helper()
	select {
	case sp := <-c:
		return sp
	case <-time.After(5 * time.Second):
		t.Fatal("no span")
		return Span{}
	}
}

// start runs a proxy that resolves testHost to 127.0.0.1 and permits all addresses, unless
// strict is true. It returns the proxy URL and the sink.
func start(t *testing.T, strict bool, port string, gateway *url.URL) (*url.URL, chanSink) {
	t.Helper()
	sink := make(chanSink, 4)
	p := &Proxy{Sink: sink, Gateway: gateway, HTTPPorts: []string{port},
		Resolve: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}}
	if !strict {
		p.Permit = func(netip.Addr) bool { return true }
	}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return u, sink
}

func port(t *testing.T, raw string) string {
	t.Helper()
	u, _ := url.Parse(raw)
	return u.Port()
}

func TestRefused(t *testing.T) {
	up := httptest.NewServer(http.NotFoundHandler())
	defer up.Close()
	upPort := port(t, up.URL)
	tests := []struct {
		name, method, target, reason string
		strict                       bool
	}{
		{"a port that is not permitted", http.MethodGet, "http://" + net.JoinHostPort(testHost, "8080") + "/", reasonPort, false},
		{"a loopback address", http.MethodGet, "http://" + net.JoinHostPort("127.0.0.1", upPort) + "/", reasonAddress, true},
		{"CONNECT", http.MethodConnect, net.JoinHostPort(testHost, "443"), reasonNoProxy, false},
		{"plain HTTP to a private address", http.MethodGet, "http://" + net.JoinHostPort(testHost, upPort) + "/", reasonAddress, true},
		{"https without CONNECT", http.MethodGet, "https://" + net.JoinHostPort(testHost, upPort) + "/", reasonPort, false},
		{"no gateway", http.MethodGet, "/llm/v1/messages", reasonNoProxy, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pu, sink := start(t, tc.strict, upPort, nil)
			c, err := net.Dial("tcp", pu.Host)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()
			_, _ = io.WriteString(c, tc.method+" "+tc.target+" HTTP/1.1\r\nHost: "+testHost+"\r\n\r\n")
			resp, err := http.ReadResponse(bufio.NewReader(c), nil)
			if err != nil {
				t.Fatal(err)
			}
			code := resp.StatusCode
			sp := sink.next(t)
			if code != http.StatusForbidden || sp.Result != ResultDenied || sp.Reason != tc.reason {
				t.Fatalf("status %d, span %+v", code, sp)
			}
		})
	}
}

func TestForwardHTTP(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("X-Forwarded-For") != "" {
			w.WriteHeader(http.StatusTeapot)
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer up.Close()
	pu, sink := start(t, false, port(t, up.URL), nil)
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu), ProxyConnectHeader: nil}}
	req, _ := http.NewRequest(http.MethodGet, "http://"+net.JoinHostPort(testHost, port(t, up.URL))+"/a?token=q", nil)
	req.Header.Set("Proxy-Authorization", "Basic "+testKey)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	sp := sink.next(t)
	if resp.StatusCode != http.StatusOK || sp.Kind != KindHTTP || sp.Status != http.StatusOK || sp.Path != "/a" ||
		sp.BytesIn != 2 {
		t.Fatalf("status %d, span %+v", resp.StatusCode, sp)
	}
}

func TestLLMRouteKeepsTheKeyOutOfTheSpan(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey || r.URL.Path != "/v1/messages" ||
			r.URL.RawQuery != "key="+testKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "done")
	}))
	defer gw.Close()
	gu, _ := url.Parse(gw.URL)
	// The gateway is a private address. The proxy does not check the address of the gateway.
	pu, sink := start(t, true, "443", gu)
	req, _ := http.NewRequest(http.MethodPost, pu.String()+"/llm/v1/messages?key="+testKey, strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+testKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	sp := sink.next(t)
	if resp.StatusCode != http.StatusOK || sp.Kind != KindLLM || sp.Path != "/llm/v1/messages" || sp.BytesOut != 2 {
		t.Fatalf("status %d, span %+v", resp.StatusCode, sp)
	}
	var out bytes.Buffer
	NewJSONSink(&out).Write(sp)
	if strings.Contains(out.String(), testKey) || strings.Contains(out.String(), "key=") {
		t.Fatalf("the span holds the key: %s", out.String())
	}
	var back Span
	if err := json.Unmarshal(out.Bytes(), &back); err != nil || back.Result != ResultOK {
		t.Fatalf("one JSON line: %v %s", err, out.String())
	}
}

func TestIdentifyRefuses(t *testing.T) {
	sink := make(chanSink, 1)
	p := &Proxy{Sink: sink, Identify: func(_ *http.Request, sp *Span) error {
		sp.Pod = "p"
		return errors.New("the address has 2 pods")
	}}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://"+testHost+"/", nil))
	sp := sink.next(t)
	if w.Code != http.StatusForbidden || sp.Reason != reasonIdentity || sp.Pod != "p" {
		t.Fatalf("status %d, span %+v", w.Code, sp)
	}
}

func TestIsPublic(t *testing.T) {
	for s, want := range map[string]bool{
		"93.184.215.14": true, "2606:2800:21f:cb07:6820:80da:af6b:8b2c": true,
		"10.0.0.1": false, "172.16.0.1": false, "192.168.1.1": false, "127.0.0.1": false,
		"169.254.169.254": false, "100.64.0.1": false, "0.0.0.0": false, "224.0.0.1": false,
		"::1": false, "fd00::1": false, "fe80::1": false, "::ffff:10.0.0.1": false,
	} {
		if got := IsPublic(netip.MustParseAddr(s)); got != want {
			t.Errorf("IsPublic(%s) = %v", s, got)
		}
	}
}
