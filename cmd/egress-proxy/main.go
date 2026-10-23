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

// Command egress-proxy is the egress proxy of the Agent and Workspace pods. It writes one JSON
// line for each span to stdout. It finds the pod of a call from the source IP, so it needs get,
// list and watch on pods. It reads no credential.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mohanraj00/koodam/internal/egress"
)

func main() {
	if err := run(); err != nil {
		slog.Error("the egress proxy stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	listen := flag.String("listen", ":3128", "the address of the proxy")
	probe := flag.String("probe-address", ":8081", "the address of /healthz and /readyz")
	gateway := flag.String("gateway-url", "", "the target of /llm/. If it is empty, the proxy refuses /llm/")
	httpPorts := flag.String("http-ports", "80", "the permitted plain HTTP ports, separated by commas")
	connectPorts := flag.String("connect-ports", "443", "the permitted CONNECT ports, separated by commas")
	namespaces := flag.String("namespaces", "", "the pod namespaces, separated by commas. Empty is all")
	syncTimeout := flag.Duration("sync-timeout", 2*time.Minute, "the time limit of the first pod cache sync")
	flag.Parse()

	p := &egress.Proxy{
		Sink:         egress.NewJSONSink(os.Stdout),
		HTTPPorts:    list(*httpPorts),
		ConnectPorts: list(*connectPorts),
	}
	if *gateway != "" {
		u, err := parseGateway(*gateway)
		if err != nil {
			return err
		}
		p.Gateway = u
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var ready atomic.Bool
	probeErr := make(chan error, 1)
	probes := &http.Server{Addr: *probe, Handler: probeMux(&ready), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := probes.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			probeErr <- fmt.Errorf("the probe server: %w", err)
			stop()
		}
	}()

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("the cluster config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	pods := egress.NewPodIndex()
	synced := make(chan error, 1)
	go func() { synced <- pods.Watch(ctx, cs, 0, list(*namespaces)...) }()
	select {
	case err := <-synced:
		if err != nil {
			return errors.Join(err, firstError(probeErr))
		}
	case <-time.After(*syncTimeout):
		// For example, the RBAC refuses the pod watch. A restart shows the problem.
		return errors.New("the pod caches did not sync in the time limit")
	}
	p.Identify = pods.Identify

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: p, ReadHeaderTimeout: 30 * time.Second}
	ready.Store(true)
	slog.Info("the egress proxy is ready", "address", ln.Addr().String())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		ready.Store(false)
		shutdown(srv)
		shutdown(probes)
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	// Serve returns when the shutdown starts. Wait until the open calls end.
	<-stopped
	return firstError(probeErr)
}

// shutdown stops a server. It waits at most 5 seconds for the open calls.
func shutdown(srv *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// firstError returns the error in errs, or nil if errs is empty.
func firstError(errs <-chan error) error {
	select {
	case err := <-errs:
		return err
	default:
		return nil
	}
}

// parseGateway returns the /llm/ target. The URL is a flag value, so it must hold no credential.
func parseGateway(v string) (*url.URL, error) {
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("--gateway-url must be an http or https URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("--gateway-url must have no user info, no query and no fragment")
	}
	return u, nil
}

// probeMux returns 200 on /healthz, and 200 on /readyz when ready is true.
func probeMux(ready *atomic.Bool) *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	m.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return m
}

// list splits a value at the commas and removes the empty items.
func list(v string) []string {
	var out []string
	for s := range strings.SplitSeq(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
