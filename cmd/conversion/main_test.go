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
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mohanraj00/koodam/internal/conversion"
)

// marker is in the request data. The output of the binary must not have it.
const marker = "object-data-marker"

// safeBuffer is a buffer for the output of a server that runs in a second goroutine.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// writeCertificate writes a self-signed serving certificate for 127.0.0.1 to dir and returns a
// pool with it.
func writeCertificate(t *testing.T, dir string) *x509.CertPool {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "conversion-test"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	for name, data := range map[string][]byte{"tls.crt": certPEM,
		"tls.key": pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	return pool
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func TestRunRefusesAnArgument(t *testing.T) {
	out := &bytes.Buffer{}
	if code := run([]string{"--" + marker}, out); code != 2 || out.String() != msgArgs+"\n" {
		t.Errorf("got the code %d and the output %q", code, out.String())
	}
}

func TestServerFailsWithNoCertificate(t *testing.T) {
	if err := newServer(t.TempDir(), freePort(t), io.Discard).Start(t.Context()); err == nil {
		t.Error("the server started with no certificate")
	}
}

// TestServer starts the server with a certificate and calls the two paths with TLS.
func TestServer(t *testing.T) {
	dir := t.TempDir()
	tlsConfig := &tls.Config{RootCAs: writeCertificate(t, dir), MinVersion: tls.VersionTLS12}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsConfig}}
	port := freePort(t)
	out := &safeBuffer{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- newServer(dir, port, out).Start(ctx) }()

	base := fmt.Sprintf("https://127.0.0.1:%d", port)
	var resp *http.Response
	var err error
	for range 100 {
		if resp, err = client.Get(base + conversion.ReadyPath); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("the server is not ready: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("%s: got the code %d", conversion.ReadyPath, resp.StatusCode)
	}

	review := `{"apiVersion":"apiextensions.k8s.io/v1","kind":"ConversionReview","request":{"uid":"1",
		"desiredAPIVersion":"core.koodam.dev/v1beta1","objects":[%s]}}`
	post := func(object string) string {
		t.Helper()
		request := strings.NewReader(fmt.Sprintf(review, object))
		resp, err := client.Post(base+conversion.ConvertPath, "application/json", request)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("got the code %d and the error %v", resp.StatusCode, err)
		}
		return string(body)
	}
	testKey := `{"apiVersion":"core.koodam.dev/v1alpha1","kind":"TestKey","metadata":{"name":"a"},"spec":{"testID":%s}}`
	good := post(fmt.Sprintf(testKey, `"run-1"`))
	if !strings.Contains(good, `"testRunID":"run-1"`) || !strings.Contains(good, `"status":"Success"`) {
		t.Errorf("the conversion did not succeed: %s", good)
	}
	bad := post(fmt.Sprintf(testKey, `["`+marker+`"]`))
	if !strings.Contains(bad, `"status":"Failure"`) || strings.Contains(bad, marker) {
		t.Errorf("want a failure with no object data: %s", bad)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("the server stopped with an error: %v", err)
	}
	if got, want := out.String(), msgFailed+conversion.ReasonObject+"\n"; got != want {
		t.Errorf("got the output %q, want %q", got, want)
	}
}
