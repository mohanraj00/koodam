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

package conversion

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	// wait is the time that a test waits for the server. Each server limit of a test is much less.
	wait = 5 * time.Second
	// short is a server limit of a test.
	short = 150 * time.Millisecond
	// The header of a request that has a body of 10 bytes.
	tenByteRequest = "POST " + ConvertPath + " HTTP/1.1\r\nHost: test\r\nContent-Length: 10\r\n\r\n"
	retryAfter     = "Retry-After"
)

// reports is a list of refusal reasons that is safe for the server goroutines.
type reports struct {
	mu   sync.Mutex
	list []string
}

func (r *reports) add(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append(r.list, reason)
}

func (r *reports) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.list...)
}

// gate is a handler that tells the test when a request is in it, and holds the request until the
// test opens the gate.
type gate struct {
	in   chan struct{}
	open chan struct{}
}

func newGate() *gate { return &gate{in: make(chan struct{}, 100), open: make(chan struct{})} }

func (g *gate) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	g.in <- struct{}{}
	<-g.open
	w.WriteHeader(http.StatusOK)
}

// request returns a request with a body length. A negative length is a length that is not known.
func request(length int64) *http.Request {
	r := httptest.NewRequest(http.MethodPost, ConvertPath, http.NoBody)
	r.ContentLength = length
	return r
}

// hold starts a request that stays in the gate. It returns a channel with the status code.
func hold(t *testing.T, l *limiter, g *gate, length int64) chan int {
	t.Helper()
	code := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		l.ServeHTTP(rec, request(length))
		code <- rec.Code
	}()
	select {
	case <-g.in:
	case <-time.After(wait):
		t.Fatal("the request did not get to the handler")
	}
	return code
}

func TestReservation(t *testing.T) {
	for length, want := range map[int64]int64{
		-1:                  MaxRequestBytes,
		0:                   minReservation,
		minReservation - 1:  minReservation,
		minReservation + 1:  minReservation + 1,
		MaxRequestBytes:     MaxRequestBytes,
		MaxRequestBytes + 1: MaxRequestBytes,
	} {
		if got := reservation(length); got != want {
			t.Errorf("length %d: got the reservation %d, want %d", length, got, want)
		}
	}
}

// TestLimiterRefusesOverTheBudget holds one request with a body length that is not known. It
// reserves the full budget, so the limiter refuses each other request until it ends.
func TestLimiterRefusesOverTheBudget(t *testing.T) {
	g, reported := newGate(), &reports{}
	l := newLimiter(g, reported.add, defaultLimits.bodyBudget)
	first := hold(t, l, g, -1)

	rec := httptest.NewRecorder()
	l.ServeHTTP(rec, request(0))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get(retryAfter) != "1" ||
		strings.TrimSpace(rec.Body.String()) != ReasonBusy || !reflect.DeepEqual(reported.all(), []string{ReasonBusy}) {
		t.Errorf("got the code %d, the header %q, the body %q and the reports %v", rec.Code,
			rec.Header().Get(retryAfter), rec.Body.String(), reported.all())
	}

	close(g.open)
	if code := <-first; code != http.StatusOK {
		t.Errorf("the first request got the code %d", code)
	}
	rec = httptest.NewRecorder()
	l.ServeHTTP(rec, request(MaxRequestBytes))
	if rec.Code != http.StatusOK || l.used != 0 {
		t.Errorf("after the release: got the code %d and %d bytes in use", rec.Code, l.used)
	}
}

// TestLimiterCapsTheRequestCount makes sure that small requests cannot be more than
// budget / minReservation at the same time.
func TestLimiterCapsTheRequestCount(t *testing.T) {
	const count = 4
	g, reported := newGate(), &reports{}
	l := newLimiter(g, reported.add, count*minReservation)
	held := make([]chan int, 0, count)
	for range count {
		held = append(held, hold(t, l, g, 100))
	}
	rec := httptest.NewRecorder()
	l.ServeHTTP(rec, request(100))
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("request %d got the code %d", count+1, rec.Code)
	}
	close(g.open)
	for _, code := range held {
		if got := <-code; got != http.StatusOK {
			t.Errorf("a request below the cap got the code %d", got)
		}
	}
	if l.used != 0 {
		t.Errorf("%d bytes are in use after the last request", l.used)
	}
}

func TestLimiterReleasesAfterAPanic(t *testing.T) {
	l := newLimiter(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(marker) }), func(string) {},
		defaultLimits.bodyBudget)
	func() {
		defer func() { _ = recover() }()
		l.ServeHTTP(httptest.NewRecorder(), request(-1))
	}()
	if l.used != 0 {
		t.Errorf("%d bytes are in use after the panic", l.used)
	}
}

// TestDefaultLimits makes sure that the server of the binary has each limit, and that the limits
// agree with the API server and with the pod.
func TestDefaultLimits(t *testing.T) {
	s, ok := NewServer("", 0, "", func(string) {}).(*server)
	if !ok {
		t.Fatal("NewServer does not return the server of this package")
	}
	srv := s.httpServer()
	for name, limit := range map[string]time.Duration{"read header": srv.ReadHeaderTimeout, "read": srv.ReadTimeout,
		"write": srv.WriteTimeout, "idle": srv.IdleTimeout, "shutdown": s.limits.shutdown} {
		if limit <= 0 {
			t.Errorf("the server has no %s time limit", name)
		}
	}
	// The default time limit of an API request is 60 seconds, and the pod has 10 seconds to stop.
	if srv.ReadHeaderTimeout > srv.ReadTimeout || srv.ReadTimeout > 60*time.Second || srv.WriteTimeout <= srv.ReadTimeout ||
		s.limits.shutdown >= 10*time.Second {
		t.Errorf("the time limits do not agree: %+v", s.limits)
	}
	if srv.MaxHeaderBytes <= 0 || srv.MaxHeaderBytes > 1<<20 || srv.TLSNextProto == nil || len(srv.TLSNextProto) != 0 {
		t.Errorf("got the header limit %d and the HTTP/2 table %v", srv.MaxHeaderBytes, srv.TLSNextProto)
	}
	// One request of the largest size must get a reservation, and two must not.
	if s.limits.bodyBudget < MaxRequestBytes || s.limits.bodyBudget >= 2*MaxRequestBytes {
		t.Errorf("got the body budget %d", s.limits.bodyBudget)
	}
	cfg := tlsConfig(nil)
	if cfg.MinVersion != tls.VersionTLS12 || !reflect.DeepEqual(cfg.NextProtos, []string{httpOne}) ||
		cfg.ClientAuth != tls.NoClientCert {
		t.Errorf("got the TLS configuration %+v", cfg)
	}
}

// TestOneRequestAtTheBodyLimit makes sure that the server of the binary accepts a request with
// the largest body, with a known length and with a length that is not known. If the body budget
// is less than the limit of one body, the server refuses each large list.
func TestOneRequestAtTheBodyLimit(t *testing.T) {
	review, err := json.Marshal(reviewOf(beta, newObject(alpha, "TestKey", object{oldID: run})))
	if err != nil {
		t.Fatal(err)
	}
	body := append(bytes.Repeat([]byte(" "), MaxRequestBytes-len(review)), review...)
	for _, length := range []int64{MaxRequestBytes, -1} {
		reported := &reports{}
		r := httptest.NewRequest(http.MethodPost, ConvertPath, bytes.NewReader(body))
		r.ContentLength = length
		rec := httptest.NewRecorder()
		NewServer("", 0, "", reported.add).WebhookMux().ServeHTTP(rec, r)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"testRunID":"`+run+`"`) ||
			len(reported.all()) != 0 {
			t.Errorf("length %d: got the code %d and the reports %v", length, rec.Code, reported.all())
		}
	}
}

// serveWith serves l on a local port with no TLS, because the limits are below TLS. It returns the
// server, its address and its reports.
func serveWith(t *testing.T, l limits) (*server, string, *reports) {
	t.Helper()
	reported := &reports{}
	s := newServer("", 0, "", reported.add, l)
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("the server stopped with an error: %v", err)
			}
		case <-time.After(wait):
			t.Error("the server did not stop")
		}
	})
	return s, listener.Addr().String(), reported
}

// testLimits are limits that no test gets to. A test makes one of them short.
func testLimits() limits {
	return limits{readHeader: wait, read: wait, write: wait, idle: wait, shutdown: time.Second,
		headerBytes: 64 << 10, bodyBudget: MaxRequestBytes}
}

// rawClient is a raw connection to the server.
type rawClient struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

func dial(t *testing.T, address string) *rawClient {
	t.Helper()
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(wait)); err != nil {
		t.Fatal(err)
	}
	return &rawClient{t: t, conn: conn, r: bufio.NewReader(conn)}
}

func (c *rawClient) send(data string) {
	c.t.Helper()
	if _, err := c.conn.Write([]byte(data)); err != nil {
		c.t.Fatal(err)
	}
}

// response reads one response. It returns the status code, or 0 and the error.
func (c *rawClient) response() (int, http.Header, error) {
	resp, err := http.ReadResponse(c.r, nil)
	if err != nil {
		return 0, nil, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, resp.Header, nil
}

// closed makes sure that the server closes the connection, and that it sends no response first.
func (c *rawClient) closed(limit string) {
	c.t.Helper()
	code, _, err := c.response()
	if err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		c.t.Errorf("%s time limit: the server did not close the connection: code %d, error %v", limit, code, err)
	}
}

func TestReadHeaderTimeLimit(t *testing.T) {
	l := testLimits()
	l.readHeader = short
	_, address, _ := serveWith(t, l)
	c := dial(t, address)
	c.send("POST " + ConvertPath + " HTTP/1.1\r\nHost: test\r\n")
	c.closed("read header")
}

// TestReadTimeLimit sends a part of a body. The handler must not wait for the other part.
func TestReadTimeLimit(t *testing.T) {
	l := testLimits()
	l.read = short
	_, address, reported := serveWith(t, l)
	c := dial(t, address)
	c.send(tenByteRequest + "{")
	if code, _, err := c.response(); err != nil || code != http.StatusBadRequest ||
		!reflect.DeepEqual(reported.all(), []string{ReasonRequest}) {
		t.Errorf("got the code %d, the error %v and the reports %v", code, err, reported.all())
	}
}

// TestWriteTimeLimit uses a handler that is not ready to write in the time limit.
func TestWriteTimeLimit(t *testing.T) {
	l := testLimits()
	l.write = short
	s, address, _ := serveWith(t, l)
	s.Register("/slow", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(4 * short)
		w.WriteHeader(http.StatusOK)
	}))
	c := dial(t, address)
	c.send("GET /slow HTTP/1.1\r\nHost: test\r\n\r\n")
	c.closed("write")
}

func TestIdleTimeLimit(t *testing.T) {
	l := testLimits()
	l.idle = short
	_, address, _ := serveWith(t, l)
	c := dial(t, address)
	c.send("GET " + ReadyPath + " HTTP/1.1\r\nHost: test\r\n\r\n")
	if code, _, err := c.response(); err != nil || code != http.StatusOK {
		t.Fatalf("got the code %d and the error %v", code, err)
	}
	c.closed("idle")
}

// TestServerCapsTheConversionRequests holds the full budget with a request that has no body yet.
// The server must refuse a second conversion, stay ready, and convert again after the first
// request ends.
func TestServerCapsTheConversionRequests(t *testing.T) {
	l := testLimits()
	l.bodyBudget = minReservation
	_, address, reported := serveWith(t, l)
	review, err := json.Marshal(reviewOf(beta, newObject(alpha, "TestKey", object{oldID: run})))
	if err != nil {
		t.Fatal(err)
	}
	post := fmt.Sprintf("POST %s HTTP/1.1\r\nHost: test\r\nContent-Length: %d\r\n\r\n%s", ConvertPath, len(review), review)

	first := dial(t, address)
	first.send(tenByteRequest)
	// The server reads the header of the first request before the second request starts.
	second := dial(t, address)
	var code int
	var header http.Header
	for range 100 {
		second.send(post)
		if code, header, err = second.response(); err != nil || code != http.StatusOK {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || code != http.StatusTooManyRequests || header.Get(retryAfter) != "1" {
		t.Fatalf("the second request got the code %d, the header %q and the error %v", code, header.Get(retryAfter), err)
	}
	probe := dial(t, address)
	probe.send("GET " + ReadyPath + " HTTP/1.1\r\nHost: test\r\n\r\n")
	if code, _, err := probe.response(); err != nil || code != http.StatusOK {
		t.Errorf("the busy server is not ready: code %d, error %v", code, err)
	}

	first.send("0123456789")
	if code, _, err := first.response(); err != nil || code != http.StatusBadRequest {
		t.Errorf("the first request got the code %d and the error %v", code, err)
	}
	third := dial(t, address)
	third.send(post)
	if code, _, err := third.response(); err != nil || code != http.StatusOK {
		t.Errorf("after the release: got the code %d and the error %v", code, err)
	}
	if got := reported.all(); len(got) < 2 || got[0] != ReasonBusy || got[len(got)-1] != ReasonRequest {
		t.Errorf("got the reports %v", got)
	}
}

// TestReload makes sure that a failure of the file watch starts the read of the files at an
// interval, and that a file watch with no failure starts no read.
func TestReload(t *testing.T) {
	var reads atomic.Int32
	read := func() error { reads.Add(1); return errors.New("not valid") }
	reload(t.Context(), func(context.Context) error { return nil }, read, time.Millisecond)
	if reads.Load() != 0 {
		t.Error("the files were read, but the file watch did not fail")
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		reload(ctx, func(context.Context) error { return errors.New("no file watch") }, read, time.Millisecond)
	}()
	deadline := time.Now().Add(wait)
	for reads.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(wait):
		t.Fatal("reload did not stop")
	}
	if reads.Load() < 3 {
		t.Errorf("the files were read %d times", reads.Load())
	}
}

// writeCertificate writes a self-signed serving certificate with a serial number to dir.
func writeCertificate(t *testing.T, dir string, serial int64) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "conversion-test"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for name, block := range map[string]*pem.Block{certFile: {Type: "CERTIFICATE", Bytes: der},
		certKeyFile: {Type: "EC PRIVATE KEY", Bytes: keyDER}} {
		if err := os.WriteFile(filepath.Join(dir, name), pem.EncodeToMemory(block), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// handshake connects with TLS and returns the state of the connection. The test reads the
// certificate, so the client does not verify it.
func handshake(t *testing.T, address string, maxVersion uint16) (tls.ConnectionState, error) {
	t.Helper()
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: wait}, Config: &tls.Config{InsecureSkipVerify: true,
		MinVersion: tls.VersionTLS10, MaxVersion: maxVersion, NextProtos: []string{"h2", httpOne}}}
	conn, err := dialer.DialContext(t.Context(), "tcp", address)
	if err != nil {
		return tls.ConnectionState{}, err
	}
	defer func() { _ = conn.Close() }()
	return conn.(*tls.Conn).ConnectionState(), nil
}

// TestServerTLS starts the server with a certificate. It makes sure that the server uses HTTP/1.1
// and TLS 1.2 or later, that it uses a new certificate with no restart, and that it stops with
// no error.
func TestServerTLS(t *testing.T) {
	dir := t.TempDir()
	writeCertificate(t, dir, 1)
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	s := newServer("127.0.0.1", port, dir, func(string) {}, defaultLimits)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	var state tls.ConnectionState
	for range 100 {
		if state, err = handshake(t, s.address(), tls.VersionTLS13); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("the server is not ready: %v", err)
	}
	if err := s.StartedChecker()(nil); err != nil {
		t.Errorf("the started check failed: %v", err)
	}
	if state.NegotiatedProtocol != httpOne || state.PeerCertificates[0].SerialNumber.Int64() != 1 {
		t.Errorf("got the protocol %q and the certificate %v", state.NegotiatedProtocol,
			state.PeerCertificates[0].SerialNumber)
	}
	if _, err := handshake(t, s.address(), tls.VersionTLS11); err == nil {
		t.Error("the server accepted TLS 1.1")
	}

	// The file watch loads the new certificate. If it has a failure, the read at certPoll loads it.
	writeCertificate(t, dir, 2)
	serial := int64(0)
	for deadline := time.Now().Add(3 * certPoll); serial != 2 && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
		if state, err = handshake(t, s.address(), tls.VersionTLS13); err == nil {
			serial = state.PeerCertificates[0].SerialNumber.Int64()
		}
	}
	if serial != 2 {
		t.Errorf("the server did not load the new certificate: serial %d, error %v", serial, err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the server stopped with an error: %v", err)
		}
	case <-time.After(defaultLimits.shutdown + wait):
		t.Error("the server did not stop")
	}
}
