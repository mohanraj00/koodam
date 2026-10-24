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
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSourceLimit(t *testing.T) {
	inner, err := net.Listen("tcp", loopback+":0")
	if err != nil {
		t.Fatal(err)
	}
	ln := SourceLimit(inner, 2)
	defer func() { _ = ln.Close() }()
	accepted := make(chan net.Conn, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	dial := func() net.Conn {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	next := func() net.Conn {
		select {
		case c := <-accepted:
			return c
		case <-time.After(5 * time.Second):
			t.Fatal("no connection")
			return nil
		}
	}
	dial()
	first := next()
	dial()
	next()
	// The third connection is over the limit. The listener closes it.
	third := dial()
	_ = third.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := third.Read(make([]byte, 1)); err == nil {
		t.Fatal("the third connection is open")
	}
	// A closed connection gives its place back. Close twice: the count goes down only once.
	_ = first.Close()
	_ = first.Close()
	dial()
	next()
	fifth := dial()
	_ = fifth.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := fifth.Read(make([]byte, 1)); err == nil {
		t.Fatal("the fifth connection is open")
	}
}

func TestPipeIdle(t *testing.T) {
	for _, tc := range []struct {
		name   string
		active bool
	}{{"an idle tunnel closes", false}, {"a tunnel that sends bytes stays open", true}} {
		t.Run(tc.name, func(t *testing.T) {
			client, pod := net.Pipe()
			upstream, remote := net.Pipe()
			defer func() { _ = pod.Close() }()
			defer func() { _ = remote.Close() }()
			go func() { _, _ = io.Copy(io.Discard, pod) }()
			stop := make(chan struct{})
			go func() {
				defer close(stop)
				if !tc.active {
					return
				}
				// The stream sends 1 byte for each 50ms, for 3 idle limits.
				for range 12 {
					if _, err := remote.Write([]byte("x")); err != nil {
						return
					}
					time.Sleep(50 * time.Millisecond)
				}
			}()
			idle := 200 * time.Millisecond
			start := time.Now()
			in, _ := pipe(client, client, upstream, idle)
			took := time.Since(start)
			<-stop
			switch {
			case !tc.active && (took < idle || took > 2*idle):
				t.Fatalf("the idle tunnel closed after %v", took)
			case tc.active && (took < 3*idle || in != 12):
				t.Fatalf("the active tunnel closed after %v with %d bytes", took, in)
			}
		})
	}
}

func TestStalledBody(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	defer up.Close()
	pu, sink := start(t, false, port(t, up.URL), nil, func(p *Proxy) { p.Idle = 200 * time.Millisecond })
	c, err := net.Dial("tcp", pu.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	target := "http://" + net.JoinHostPort(testHost, port(t, up.URL)) + "/"
	// The pod promises 10 bytes, sends 1 and stops.
	_, _ = io.WriteString(c, "POST "+target+" HTTP/1.1\r\nHost: "+testHost+"\r\nContent-Length: 10\r\n\r\nx")
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err == nil {
		_ = resp.Body.Close()
	}
	sp := sink.next(t)
	if sp.Result == ResultOK && sp.Status == http.StatusOK {
		t.Fatalf("the stalled body passed: span %+v", sp)
	}
	if sp.BytesOut != 1 || !strings.HasPrefix(sp.Host, testHost) {
		t.Fatalf("span %+v", sp)
	}
}

// idleCall sends raw to a proxy with an idle limit of 200ms, and returns the time until the
// proxy ends the call.
func idleCall(t *testing.T, strict bool, upPort, raw string) (time.Duration, Span) {
	t.Helper()
	pu, sink := start(t, strict, upPort, nil, func(p *Proxy) { p.Idle = 200 * time.Millisecond })
	c, err := net.Dial("tcp", pu.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	begin := time.Now()
	_, _ = io.WriteString(c, raw)
	sp := sink.next(t)
	return time.Since(begin), sp
}

func TestIdleCalls(t *testing.T) {
	// The upstream sends the headers, then stops.
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer stalled.Close()
	// The upstream accepts an upgrade, then stops.
	upgrade := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Connection", "Upgrade")
		w.Header().Set("Upgrade", "test")
		w.WriteHeader(http.StatusSwitchingProtocols)
		c, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = io.Copy(io.Discard, c)
	}))
	defer upgrade.Close()
	get := func(up *httptest.Server, extra string) string {
		return "GET http://" + net.JoinHostPort(testHost, port(t, up.URL)) + "/ HTTP/1.1\r\nHost: " + testHost +
			"\r\n" + extra + "\r\n"
	}
	tests := []struct {
		name, upPort, raw, reason string
		strict                    bool
	}{
		{"a response that stops after the headers", port(t, stalled.URL), get(stalled, ""), reasonIdle, false},
		{"an upgrade with no bytes", port(t, upgrade.URL), get(upgrade, "Connection: Upgrade\r\nUpgrade: test\r\n"),
			reasonIdle, false},
		{"a denied address with a body that does not come", port(t, stalled.URL),
			"POST http://" + net.JoinHostPort(testHost, port(t, stalled.URL)) + "/ HTTP/1.1\r\nHost: " + testHost +
				"\r\nContent-Length: 1\r\n\r\n", reasonAddress, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			took, sp := idleCall(t, tc.strict, tc.upPort, tc.raw)
			if took > 2*time.Second || sp.Reason != tc.reason {
				t.Fatalf("the call ended after %v, span %+v", took, sp)
			}
		})
	}
}

func TestWatchStopWaitsForFire(t *testing.T) {
	var a activity
	started := make(chan struct{})
	var done atomic.Bool
	stop := a.watch(20*time.Millisecond, func() {
		close(started)
		time.Sleep(100 * time.Millisecond)
		done.Store(true)
	})
	<-started
	stop()
	if !done.Load() {
		t.Fatal("stop returned before fire was complete")
	}
	stop()
}

func TestKeepAliveAfterTheIdleLimit(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer up.Close()
	idle := 200 * time.Millisecond
	pu, sink := start(t, false, port(t, up.URL), nil, func(p *Proxy) { p.Idle = idle })
	c, err := net.Dial("tcp", pu.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	br := bufio.NewReader(c)
	raw := "GET http://" + net.JoinHostPort(testHost, port(t, up.URL)) + "/ HTTP/1.1\r\nHost: " + testHost + "\r\n\r\n"
	for i := range 2 {
		if i == 1 {
			// The deadlines of the first call end before the second call.
			time.Sleep(2 * idle)
		}
		_, _ = io.WriteString(c, raw)
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if sp := sink.next(t); resp.StatusCode != http.StatusOK || sp.Result != ResultOK {
			t.Fatalf("call %d: status %d, span %+v", i, resp.StatusCode, sp)
		}
	}
}
