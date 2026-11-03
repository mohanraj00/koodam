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
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// The test binary is also the helper child. The supervisor starts it with serveArg, and the
// environment selects the helper behavior. Production code reads no environment variable.
const (
	envMode   = "KOODAM_ADMISSION_HELPER_MODE"
	envDir    = "KOODAM_ADMISSION_HELPER_DIR"
	modePanic = "panic"
	modeCert  = "cert"
	modeExit  = "exit"
	modeWait  = "wait"
	marker    = "koodam-harmless-output-marker"
	readyFile = "ready"
	termFile  = "terminated"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == serveArg {
		os.Exit(helper(os.Getenv(envMode), os.Getenv(envDir)))
	}
	os.Exit(m.Run())
}

// helper writes the marker to each output and then acts as mode selects. It never serves. The
// marker writes ignore a failure: the supervisor sends both outputs to the null device, and the
// tests check only the exit code and the supervisor output.
func helper(mode, dir string) int {
	_, _ = fmt.Fprintln(os.Stdout, marker)
	_, _ = fmt.Fprintln(os.Stderr, marker)
	switch mode {
	case modePanic:
		panic(marker)
	case modeCert:
		return codeCert
	case modeExit:
		return codeOK
	case modeWait:
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGTERM)
		tmp := filepath.Join(dir, readyFile+".tmp")
		if os.WriteFile(tmp, []byte(strconv.Itoa(os.Getpid())), 0o600) != nil ||
			os.Rename(tmp, filepath.Join(dir, readyFile)) != nil {
			return 1
		}
		<-sigs
		_, _ = fmt.Fprintln(os.Stderr, marker)
		_ = os.WriteFile(filepath.Join(dir, termFile), nil, 0o600)
		return codeOK
	}
	return 1
}

func helperEnv(mode, dir string) []string {
	return []string{envMode + "=" + mode, envDir + "=" + dir}
}

func testExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("the test executable is not known")
	}
	return exe
}

func TestInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"--help"}, {serveArg + "=" + marker}, {serveArg, marker}, {marker}} {
		var out bytes.Buffer
		if run(args, &out) != codeArgs || out.String() != messages[codeArgs]+"\n" {
			t.Fatal("an invalid argument did not give the fixed failure")
		}
	}
}

func TestSuperviseStartFailure(t *testing.T) {
	var out bytes.Buffer
	missing := filepath.Join(t.TempDir(), marker)
	if supervise(missing, nil, nil, &out) != codeStart || out.String() != messages[codeStart]+"\n" {
		t.Fatal("a start failure did not give the fixed failure")
	}
}

// TestSuperviseContainsChildOutput runs real children that write a marker to stdout and stderr.
// Only the fixed message of the exit code can reach the supervisor output.
func TestSuperviseContainsChildOutput(t *testing.T) {
	for mode, want := range map[string]int{modePanic: codeChild, modeCert: codeCert, modeExit: codeChild} {
		var out bytes.Buffer
		if supervise(testExe(t), helperEnv(mode, t.TempDir()), nil, &out) != want ||
			out.String() != messages[want]+"\n" {
			t.Fatalf("the supervisor output for the %s child is not the fixed message", mode)
		}
	}
}

func TestSuperviseForwardsTermination(t *testing.T) {
	dir, exe := t.TempDir(), testExe(t)
	sigs := make(chan os.Signal, 1)
	var out bytes.Buffer
	code := make(chan int, 1)
	go func() { code <- supervise(exe, helperEnv(modeWait, dir), sigs, &out) }()
	pid := waitForPID(t, filepath.Join(dir, readyFile))
	sigs <- syscall.SIGTERM
	select {
	case got := <-code:
		if got != codeOK || out.String() != messages[codeOK]+"\n" {
			t.Fatal("a forwarded termination did not give the fixed stop")
		}
	case <-time.After(30 * time.Second):
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("the supervisor did not stop after the termination")
	}
	if _, err := os.Stat(filepath.Join(dir, termFile)); err != nil {
		t.Fatal("the child did not get the forwarded termination")
	}
	if !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		t.Fatal("the supervisor returned before it reaped the child")
	}
}

// TestSuperviseEarlySignal sends the termination before the child starts. The child stops with
// code 0 or by the default signal action. Both are a normal stop.
func TestSuperviseEarlySignal(t *testing.T) {
	sigs := make(chan os.Signal, 1)
	sigs <- syscall.SIGTERM
	var out bytes.Buffer
	if supervise(testExe(t), helperEnv(modeWait, t.TempDir()), sigs, &out) != codeOK ||
		out.String() != messages[codeOK]+"\n" {
		t.Fatal("an early termination did not give the fixed stop")
	}
}

func waitForPID(t *testing.T, path string) int {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		if data, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(string(data)); err == nil {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the helper child did not become ready")
	return 0
}

func TestChildCode(t *testing.T) {
	for _, c := range []struct{ exit, want int }{
		{codeOK, codeChild}, {-1, codeChild}, {2, codeChild}, {codeCert, codeCert}, {codeServe, codeServe},
	} {
		if childCode(c.exit, false) != c.want {
			t.Fatalf("the child code %d does not map to %d", c.exit, c.want)
		}
	}
	if childCode(codeOK, true) != codeOK || childCode(-1, true) != codeOK || childCode(2, true) != codeChild {
		t.Fatal("a child code after a forwarded signal is wrong")
	}
}

// testPair is an ephemeral self-signed key pair for 127.0.0.1. It exists only in memory and in
// TempDir. No test prints it.
type testPair struct {
	cert, key []byte
	pool      *x509.CertPool
}

func newTestPair(t *testing.T) testPair {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal("the test key was not made")
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal("the test certificate was not made")
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal("the test key was not encoded")
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal("the test certificate was not parsed")
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return testPair{
		cert: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		key:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		pool: pool,
	}
}

func writePair(t *testing.T, certPEM, keyPEM []byte) (string, string) {
	t.Helper()
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if os.WriteFile(certPath, certPEM, 0o600) != nil || os.WriteFile(keyPath, keyPEM, 0o600) != nil {
		t.Fatal("the test key pair was not written")
	}
	return certPath, keyPath
}

func TestLoadCertificateFailures(t *testing.T) {
	a, b := newTestPair(t), newTestPair(t)
	missing := filepath.Join(t.TempDir(), "missing")
	garbageCert, garbageKey := writePair(t, []byte(marker), []byte(marker))
	mixedCert, mixedKey := writePair(t, a.cert, b.key)
	for _, p := range [][2]string{{missing, missing}, {garbageCert, garbageKey}, {mixedCert, mixedKey}} {
		if cert, ok := loadCertificate(p[0], p[1]); ok || cert != nil {
			t.Fatal("a bad key pair loaded")
		}
	}
}

// TestCertificateLoadedOnce removes the files after the load. GetCertificate must still return
// the same certificate, so it reads no file and a restart is necessary to rotate.
func TestCertificateLoadedOnce(t *testing.T) {
	p := newTestPair(t)
	certPath, keyPath := writePair(t, p.cert, p.key)
	cert, ok := loadCertificate(certPath, keyPath)
	if !ok || os.Remove(certPath) != nil || os.Remove(keyPath) != nil {
		t.Fatal("the test key pair did not load")
	}
	// The controller-runtime v0.25.0 server starts from NextProtos h2, then applies TLSOpts.
	cfg := &tls.Config{NextProtos: []string{"h2"}}
	tlsOptions(cert)(cfg)
	if cfg.MinVersion != tls.VersionTLS12 || len(cfg.NextProtos) != 1 || cfg.NextProtos[0] != alpnHTTP1 ||
		cfg.ClientCAs != nil || cfg.ClientAuth != tls.NoClientCert || cfg.GetCertificate == nil {
		t.Fatal("the TLS options are wrong")
	}
	for range 2 {
		if got, err := cfg.GetCertificate(&tls.ClientHelloInfo{}); err != nil || got != cert {
			t.Fatal("GetCertificate did not return the loaded certificate")
		}
	}
}

func defaultServer(t *testing.T, cert *tls.Certificate) *webhook.DefaultServer {
	t.Helper()
	srv, ok := newServer(cert).(*webhook.DefaultServer)
	if !ok {
		t.Fatal("the server is not the controller-runtime default server")
	}
	return srv
}

func TestServerRegistration(t *testing.T) {
	srv := defaultServer(t, &tls.Certificate{})
	o := srv.Options
	if o.Port != servePort || o.Host != "" || o.ClientCAName != "" || len(o.TLSOpts) != 1 {
		t.Fatal("the server options are wrong")
	}
	mux := srv.WebhookMux()
	if _, pattern := mux.Handler(httptest.NewRequest(http.MethodPost, webhookPath, nil)); pattern != webhookPath {
		t.Fatal("the validator is not at the approved path")
	}
	if _, pattern := mux.Handler(httptest.NewRequest(http.MethodPost, webhookPath+"/x", nil)); pattern != "" {
		t.Fatal("the server has a handler at another path")
	}
	// Only the strict handler from NewHandler refuses GET with this fixed text.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, webhookPath, nil))
	if w.Code != http.StatusMethodNotAllowed || w.Body.String() != "Only the POST method is permitted.\n" {
		t.Fatal("the registered handler is not the Agent admission handler")
	}
}

// TestServedTLS serves the registered mux with the same TLS options on a loopback port. It does
// not run Start on port 9443 or prove a deployed pod.
func TestServedTLS(t *testing.T) {
	p := newTestPair(t)
	pair, err := tls.X509KeyPair(p.cert, p.key)
	if err != nil {
		t.Fatal("the test key pair did not parse")
	}
	srv := defaultServer(t, &pair)
	cfg := &tls.Config{NextProtos: []string{"h2"}}
	srv.Options.TLSOpts[0](cfg)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal("the loopback listener did not start")
	}
	hs := &http.Server{Handler: srv.WebhookMux(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = hs.Serve(ln) }()
	t.Cleanup(func() { _ = hs.Close() })

	client := &tls.Config{RootCAs: p.pool, ServerName: "127.0.0.1", NextProtos: []string{"h2", alpnHTTP1}}
	hc := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: client, ForceAttemptHTTP2: true,
	}}
	t.Cleanup(hc.CloseIdleConnections)
	resp, err := hc.Get("https://" + ln.Addr().String() + webhookPath)
	if err != nil {
		t.Fatal("the TLS request failed")
	}
	_ = resp.Body.Close()
	if resp.ProtoMajor != 1 || resp.TLS.NegotiatedProtocol != alpnHTTP1 || resp.TLS.Version < tls.VersionTLS12 ||
		!bytes.Equal(resp.TLS.PeerCertificates[0].Raw, pair.Certificate[0]) ||
		resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatal("the served TLS connection is wrong")
	}

	old := client.Clone()
	old.MinVersion, old.MaxVersion = tls.VersionTLS10, tls.VersionTLS11
	if conn, err := tls.Dial("tcp", ln.Addr().String(), old); err == nil {
		_ = conn.Close()
		t.Fatal("the server accepted TLS 1.1")
	}
}
