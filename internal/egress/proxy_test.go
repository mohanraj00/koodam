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
	"crypto/tls"
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
// strict is true. opts change the proxy. It returns the proxy URL and the sink.
func start(t *testing.T, strict bool, port string, gateway *url.URL, opts ...func(*Proxy)) (*url.URL, chanSink) {
	t.Helper()
	sink := make(chanSink, 4)
	p := &Proxy{Sink: sink, Gateway: gateway, ConnectPorts: []string{port}, HTTPPorts: []string{port},
		Resolve: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}}
	if !strict {
		p.Permit = func(netip.Addr) bool { return true }
	}
	for _, o := range opts {
		o(p)
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

func TestConnectReadsSNI(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "hello")
	}))
	defer up.Close()
	pu, sink := start(t, false, port(t, up.URL), nil)
	tr := up.Client().Transport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyURL(pu)
	resp, err := (&http.Client{Transport: tr}).Get("https://" + net.JoinHostPort(testHost, port(t, up.URL)) + "/a?q=1")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	tr.CloseIdleConnections()
	sp := sink.next(t)
	if sp.V != SpanVersion || sp.Kind != KindConnect || sp.Result != ResultOK || sp.SNI != testHost ||
		sp.Path != "" || sp.Port != port(t, up.URL) {
		t.Fatalf("span %+v", sp)
	}
	if sp.Source != "127.0.0.1" || sp.BytesIn == 0 || sp.BytesOut == 0 {
		t.Fatalf("span %+v", sp)
	}
}

// connect sends CONNECT to the proxy and returns the connection and the status code.
func connect(t *testing.T, pu *url.URL, target string) (net.Conn, int) {
	t.Helper()
	c, err := net.Dial("tcp", pu.Host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_, _ = io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	return c, resp.StatusCode
}

func TestConnectRefusesFront(t *testing.T) {
	up := httptest.NewTLSServer(http.NotFoundHandler())
	defer up.Close()
	pu, sink := start(t, false, port(t, up.URL), nil)
	c, code := connect(t, pu, net.JoinHostPort(testHost, port(t, up.URL)))
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	//nolint:gosec // The test server has a self-signed certificate.
	if err := tls.Client(c, &tls.Config{ServerName: "front.example", InsecureSkipVerify: true}).Handshake(); err == nil {
		t.Fatal("the handshake with a different SNI passed")
	}
	sp := sink.next(t)
	if sp.Result != ResultDenied || sp.SNI != "front.example" || sp.Host != net.JoinHostPort(testHost, port(t, up.URL)) {
		t.Fatalf("span %+v", sp)
	}
}

func TestConnectRefusesNoSNI(t *testing.T) {
	up := httptest.NewTLSServer(http.NotFoundHandler())
	defer up.Close()
	pu, sink := start(t, false, port(t, up.URL), nil)
	c, _ := connect(t, pu, net.JoinHostPort(testHost, port(t, up.URL)))
	// With no ServerName, the client sends no SNI.
	//nolint:gosec // The test server has a self-signed certificate.
	if err := tls.Client(c, &tls.Config{InsecureSkipVerify: true}).Handshake(); err == nil {
		t.Fatal("the handshake with no SNI passed")
	}
	if sp := sink.next(t); sp.Result != ResultDenied || sp.Reason != reasonSNI {
		t.Fatalf("span %+v", sp)
	}
}

func TestConnectHelloTimeout(t *testing.T) {
	up := httptest.NewTLSServer(http.NotFoundHandler())
	defer up.Close()
	pu, sink := start(t, false, port(t, up.URL), nil, func(p *Proxy) { p.HelloTimeout = 50 * time.Millisecond })
	_, code := connect(t, pu, net.JoinHostPort(testHost, port(t, up.URL)))
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if sp := sink.next(t); sp.Result != ResultError || sp.Reason != "hello timeout" {
		t.Fatalf("span %+v", sp)
	}
}

// TestConnectEndsWithTheClient checks that the proxy ends the call when the client closes,
// also if the upstream stays open and sends nothing.
func TestConnectEndsWithTheClient(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	got := make(chan struct{})
	go func() {
		// The upstream reads all bytes and never writes or closes.
		if c, err := ln.Accept(); err == nil {
			if _, err := c.Read(make([]byte, 1)); err == nil {
				close(got)
			}
			_, _ = io.Copy(io.Discard, c)
		}
	}()
	lnPort := port(t, "http://"+ln.Addr().String())
	pu, sink := start(t, false, lnPort, nil)
	c, _ := connect(t, pu, net.JoinHostPort(testHost, lnPort))
	tc := tls.Client(c, &tls.Config{ServerName: testHost})
	go func() { _ = tc.Handshake() }()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream got no ClientHello")
	}
	_ = c.Close()
	if sp := sink.next(t); sp.Result != ResultOK || sp.SNI != testHost || sp.BytesOut == 0 {
		t.Fatalf("span %+v", sp)
	}
}

func TestConnectRefusesNoTLS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	lnPort := port(t, "http://"+ln.Addr().String())
	pu, sink := start(t, false, lnPort, nil)
	c, code := connect(t, pu, net.JoinHostPort(testHost, lnPort))
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	_, _ = io.WriteString(c, "GET / HTTP/1.1\r\n\r\n")
	if sp := sink.next(t); sp.Result != ResultDenied || sp.Reason != reasonNoTLS {
		t.Fatalf("span %+v", sp)
	}
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
		{"CONNECT to a port that is not permitted", http.MethodConnect, net.JoinHostPort(testHost, "443"), reasonPort, false},
		{"CONNECT to a private address", http.MethodConnect, net.JoinHostPort(testHost, upPort), reasonAddress, true},
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
			_ = resp.Body.Close()
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
		n, _ := io.Copy(io.Discard, r.Body)
		if n != 4 {
			w.WriteHeader(http.StatusBadRequest)
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer up.Close()
	pu, sink := start(t, false, port(t, up.URL), nil)
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu), ProxyConnectHeader: nil}}
	target := "http://" + net.JoinHostPort(testHost, port(t, up.URL)) + "/a?token=q"
	req, _ := http.NewRequest(http.MethodPost, target, strings.NewReader("body"))
	req.Header.Set("Proxy-Authorization", "Basic "+testKey)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	sp := sink.next(t)
	if resp.StatusCode != http.StatusOK || sp.Kind != KindHTTP || sp.Status != http.StatusOK || sp.Path != "/a" ||
		sp.BytesIn != 2 || sp.BytesOut != 4 || sp.Host != net.JoinHostPort(testHost, port(t, up.URL)) ||
		sp.V != SpanVersion || sp.Port != port(t, up.URL) {
		t.Fatalf("status %d, span %+v", resp.StatusCode, sp)
	}
}

func TestLLMRouteKeepsTheKeyOutOfTheSpan(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey || r.URL.Path != "/api/v1/messages" ||
			r.URL.RawQuery != "key="+testKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "done")
	}))
	defer gw.Close()
	gu, _ := url.Parse(gw.URL + "/api")
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
	if resp.StatusCode != http.StatusOK || sp.Kind != KindLLM || sp.Path != "/llm/v1/messages" || sp.BytesOut != 2 ||
		sp.V != SpanVersion || sp.Port != gu.Port() {
		t.Fatalf("status %d, span %+v", resp.StatusCode, sp)
	}
	var out bytes.Buffer
	NewJSONSink(&out).Write(sp)
	if strings.Contains(out.String(), testKey) || strings.Contains(out.String(), "key=") {
		t.Fatalf("the span holds the key: %s", out.String())
	}
	var back Span
	if err := json.Unmarshal(out.Bytes(), &back); err != nil || back.Result != ResultOK ||
		!strings.HasPrefix(out.String(), `{"v":1,`) {
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
		"169.254.169.254": false, "100.64.0.1": false, "0.0.0.0": false, "0.0.0.1": false, "224.0.0.1": false,
		"::1": false, "fd00::1": false, "fe80::1": false, "::ffff:10.0.0.1": false,
	} {
		if got := IsPublic(netip.MustParseAddr(s)); got != want {
			t.Errorf("IsPublic(%s) = %v", s, got)
		}
	}
}

func TestDial(t *testing.T) {
	addrs := []netip.Addr{netip.MustParseAddr("93.184.215.14"), netip.MustParseAddr("::ffff:127.0.0.1")}
	p := &Proxy{Resolve: func(context.Context, string) ([]netip.Addr, error) { return addrs, nil }}
	if _, err := p.dial(context.Background(), testHost, "80"); !errors.Is(err, errDenied) {
		t.Fatalf("one private address of the name: %v", err)
	}
	p.Resolve = func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("no such host") }
	if _, err := p.dial(context.Background(), testHost, "80"); err == nil || errors.Is(err, errDenied) {
		t.Fatalf("a failed name lookup: %v", err)
	}
	p.Resolve = func(context.Context, string) ([]netip.Addr, error) { return nil, nil }
	if _, err := p.dial(context.Background(), testHost, "80"); err == nil {
		t.Fatal("a name with no address")
	}
}

func TestDialTriesEachAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	// The listener has only the IPv4 address, thus the dial to ::1 fails first.
	addrs := []netip.Addr{netip.MustParseAddr("::1"), netip.MustParseAddr("127.0.0.1")}
	p := &Proxy{Permit: func(netip.Addr) bool { return true },
		Resolve: func(context.Context, string) ([]netip.Addr, error) { return addrs, nil }}
	c, err := p.dial(context.Background(), testHost, port(t, "http://"+ln.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
}

func TestLLMRouteStreams(t *testing.T) {
	release := make(chan struct{})
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: 1\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "data: 2\n\n")
	}))
	defer gw.Close()
	gu, _ := url.Parse(gw.URL)
	pu, sink := start(t, true, "443", gu)
	resp, err := http.Get(pu.String() + "/llm/v1/messages")
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	// The first event arrives before the gateway ends the answer.
	first := make([]byte, len("data: 1\n\n"))
	_, err = io.ReadFull(resp.Body, first)
	close(release)
	if err != nil || string(first) != "data: 1\n\n" {
		t.Fatalf("first event %q: %v", first, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	if sp := sink.next(t); sp.BytesIn != 2*int64(len(first)) {
		t.Fatalf("span %+v", sp)
	}
}
