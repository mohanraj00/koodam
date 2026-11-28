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
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

const (
	// ConvertPath is the path of the conversion handler. The CRDs name it.
	ConvertPath = "/convert"
	// ReadyPath is on the TLS port, so the pod has one port and the NetworkPolicy has one rule.
	ReadyPath = "/readyz"
	// The files of the serving certificate in the certificate directory.
	certFile    = "tls.crt"
	certKeyFile = "tls.key"
	// httpOne is the one protocol of the server.
	httpOne = "http/1.1"
	// certPoll is the interval of the certificate read when the file watch is not available.
	certPoll = 10 * time.Second
)

// server is the HTTPS server of the conversion webhook. It is not the controller-runtime webhook
// server, because that server has no read time limit and no write time limit.
type server struct {
	host    string
	port    int
	certDir string
	limits  limits
	mux     *http.ServeMux

	mu      sync.Mutex
	started bool
}

// NewServer returns the server with the conversion handler. It has no manager, no API client and
// no client CA. The server reads tls.crt and tls.key from certDir and loads them again when the
// files change. Thus a new certificate needs no restart. The server has a time limit for each
// part of a request and a cap on the conversion requests in progress (see limits).
func NewServer(host string, port int, certDir string, refused func(reason string)) webhook.Server {
	return newServer(host, port, certDir, refused, defaultLimits)
}

func newServer(host string, port int, certDir string, refused func(reason string), l limits) *server {
	s := &server{host: host, port: port, certDir: certDir, limits: l, mux: http.NewServeMux()}
	s.Register(ConvertPath, newLimiter(NewHandler(refused), refused, l.bodyBudget))
	// The cap does not apply to the probes, so a busy server stays ready.
	s.Register(ReadyPath, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	return s
}

// NeedLeaderElection is false: each replica serves.
func (s *server) NeedLeaderElection() bool { return false }

// Register adds a handler for a path.
func (s *server) Register(path string, hook http.Handler) { s.mux.Handle(path, hook) }

// WebhookMux returns the mux of the server.
func (s *server) WebhookMux() *http.ServeMux { return s.mux }

func (s *server) address() string { return net.JoinHostPort(s.host, strconv.Itoa(s.port)) }

// tlsConfig returns the TLS configuration of the server. It permits HTTP/1.1 only. Thus one
// connection has one request at a time, and the time limits apply to that request.
func tlsConfig(certificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{httpOne}, GetCertificate: certificate}
}

func (s *server) httpServer() *http.Server {
	return &http.Server{
		Handler:           s.mux,
		ReadHeaderTimeout: s.limits.readHeader,
		ReadTimeout:       s.limits.read,
		WriteTimeout:      s.limits.write,
		IdleTimeout:       s.limits.idle,
		MaxHeaderBytes:    s.limits.headerBytes,
		// An empty map that is not nil turns off HTTP/2.
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
	}
}

// reload keeps the certificate current until ctx is done. start is the file watch. If it fails,
// reload reads the files at an interval, so a new certificate loads with no file watch.
func reload(ctx context.Context, start func(context.Context) error, read func() error, interval time.Duration) {
	if start(ctx) == nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// A file that is not valid keeps the certificate in use.
			_ = read()
		}
	}
}

// Start serves until ctx is done. It fails if the certificate is not there or not valid.
func (s *server) Start(ctx context.Context) error {
	watcher, err := certwatcher.New(filepath.Join(s.certDir, certFile), filepath.Join(s.certDir, certKeyFile))
	if err != nil {
		return err
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	go reload(ctx, watcher.Start, watcher.ReadCertificate, certPoll)

	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", s.address())
	if err != nil {
		return err
	}
	return s.serve(ctx, tls.NewListener(listener, tlsConfig(watcher.GetCertificate)))
}

// serve serves the listener until ctx is done. Then it gives the requests in progress the
// shutdown time.
func (s *server) serve(ctx context.Context, listener net.Listener) error {
	srv := s.httpServer()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		grace, cancel := context.WithTimeout(context.Background(), s.limits.shutdown)
		defer cancel()
		if srv.Shutdown(grace) != nil {
			_ = srv.Close()
		}
	}()
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
	if err := srv.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-stopped
	return nil
}

// StartedChecker returns a check that the server accepts a TLS connection.
func (s *server) StartedChecker() healthz.Checker {
	return func(req *http.Request) error {
		s.mu.Lock()
		started := s.started
		s.mu.Unlock()
		if !started {
			return errors.New("the conversion server did not start")
		}
		ctx := context.Background()
		if req != nil {
			ctx = req.Context()
		}
		// The check is for a connection only. It sends no data, so it does not verify the certificate.
		dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second},
			Config: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}}
		conn, err := dialer.DialContext(ctx, "tcp", s.address())
		if err != nil {
			return err
		}
		return conn.Close()
	}
}
