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
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
	"time"
)

// loopback is the address of each test name.
const loopback = "127.0.0.1"

// startTLS runs the transparent TLS listener of a proxy that resolves each name to 127.0.0.1
// and dials port. If strict is false, it permits all addresses. It returns the listener address.
func startTLS(t *testing.T, strict bool, port string, opts ...func(*Proxy)) (string, chanSink) {
	t.Helper()
	sink := make(chanSink, 2*maxTLSPerSource)
	p := &Proxy{Sink: sink, TLSPort: port, ConnectPorts: []string{port},
		Resolve: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr(loopback)}, nil
		}}
	if !strict {
		p.Permit = func(netip.Addr) bool { return true }
	}
	for _, o := range opts {
		o(p)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.ServeTLS(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return ln.Addr().String(), sink
}

func TestTransparentTLS(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "hello")
	}))
	defer up.Close()
	addr, sink := startTLS(t, false, port(t, up.URL))
	tr := up.Client().Transport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	resp, err := (&http.Client{Transport: tr}).Get("https://" + testHost + "/a?q=1")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	tr.CloseIdleConnections()
	sp := sink.next(t)
	if sp.V != SpanVersion || sp.Kind != KindTLS || sp.Result != ResultOK || sp.SNI != testHost ||
		sp.Host != testHost || sp.Port != port(t, up.URL) || sp.Path != "" {
		t.Fatalf("span %+v", sp)
	}
	if sp.Source != loopback || sp.BytesIn == 0 || sp.BytesOut == 0 {
		t.Fatalf("span %+v", sp)
	}
}

func TestTransparentTLSRefused(t *testing.T) {
	up := httptest.NewTLSServer(http.NotFoundHandler())
	defer up.Close()
	tests := []struct {
		name, sni, result, reason string
		strict, plain             bool
		ports                     []string
	}{
		{"no SNI", "", ResultDenied, reasonNoSNI, false, false, nil},
		{"a private address", testHost, ResultDenied, reasonAddress, true, false, nil},
		{"no TLS", "", ResultDenied, reasonNoTLS, false, true, nil},
		{"a port that is not in ConnectPorts", testHost, ResultDenied, reasonPort, false, false, []string{"8443"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			addr, sink := startTLS(t, tc.strict, port(t, up.URL), func(p *Proxy) {
				if tc.ports != nil {
					p.ConnectPorts = tc.ports
				}
			})
			c, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()
			if tc.plain {
				_, _ = io.WriteString(c, "GET / HTTP/1.1\r\nHost: "+testHost+"\r\n\r\n")
			} else if err := tls.Client(c, &tls.Config{ServerName: tc.sni, InsecureSkipVerify: true}).
				Handshake(); err == nil {
				t.Fatal("the handshake passed")
			}
			if sp := sink.next(t); sp.Kind != KindTLS || sp.Result != tc.result || sp.Reason != tc.reason {
				t.Fatalf("span %+v", sp)
			}
		})
	}
}

func TestTransparentTLSLimit(t *testing.T) {
	addr, sink := startTLS(t, false, "443")
	conns := make([]net.Conn, 0, maxTLSPerSource+1)
	for range maxTLSPerSource + 1 {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	// The connections wait in the ClientHello read. Only the last one is over the limit.
	if sp := sink.next(t); sp.Result != ResultDenied || sp.Reason != reasonLimit {
		t.Fatalf("span %+v", sp)
	}
}

func TestTransparentTLSShutdown(t *testing.T) {
	sink := make(chanSink, 4)
	p := &Proxy{Sink: sink}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.ServeTLS(ctx, ln) }()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	// A connection with no ClientHello. ServeTLS must close it and write its span.
	_, _ = io.WriteString(c, "\x16")
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeTLS did not return")
	}
	select {
	case sp := <-sink:
		if sp.Kind != KindTLS || sp.Result == "" {
			t.Fatalf("span %+v", sp)
		}
	default:
		t.Fatal("ServeTLS returned before the span")
	}
}

func TestTransparentHTTP(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Host+r.URL.Path)
	}))
	defer up.Close()
	upHost := net.JoinHostPort(testHost, port(t, up.URL))
	tests := []struct {
		name, method, target, host string
		code                       int
		result, reason             string
	}{
		{"origin form", http.MethodGet, "/a", upHost, http.StatusOK, ResultOK, ""},
		{"the /llm/ path goes to the host", http.MethodGet, "/llm/v1", upHost, http.StatusOK, ResultOK, ""},
		{"a port that is not permitted", http.MethodGet, "/a", net.JoinHostPort(testHost, "8080"),
			http.StatusForbidden, ResultDenied, reasonPort},
		{"CONNECT", http.MethodConnect, upHost, upHost, http.StatusForbidden, ResultDenied, reasonNoProxy},
	}
	gw, _ := url.Parse("http://gateway.invalid")
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sink := make(chanSink, 4)
			p := &Proxy{Sink: sink, Gateway: gw, HTTPPorts: []string{port(t, up.URL)},
				Permit: func(netip.Addr) bool { return true },
				Identify: func(_ *http.Request, sp *Span) error {
					sp.Pod = "pod-a"
					return nil
				},
				Resolve: func(context.Context, string) ([]netip.Addr, error) {
					return []netip.Addr{netip.MustParseAddr(loopback)}, nil
				}}
			srv := httptest.NewServer(p.Transparent())
			defer srv.Close()
			c, err := net.Dial("tcp", srv.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()
			_, _ = io.WriteString(c, tc.method+" "+tc.target+" HTTP/1.1\r\nHost: "+tc.host+"\r\n\r\n")
			resp, err := http.ReadResponse(bufio.NewReader(c), nil)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			sp := sink.next(t)
			if resp.StatusCode != tc.code || sp.Result != tc.result || sp.Reason != tc.reason || sp.Pod != "pod-a" {
				t.Fatalf("status %d, span %+v", resp.StatusCode, sp)
			}
			if tc.code == http.StatusOK && (sp.Kind != KindHTTP || string(body) != upHost+tc.target) {
				t.Fatalf("body %q, span %+v", body, sp)
			}
		})
	}
}
