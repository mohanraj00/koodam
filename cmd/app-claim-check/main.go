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

// The app-claim-check command runs as a container of an AppVersion pod. The Service of the
// version sends each request to this container. The command verifies the signed claim of the
// caller and then sends the request to the app container on the loopback address. It never
// writes a token to its output.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mohanraj00/koodam/internal/appclaim"
)

// routes is a list of "listen=upstream" port pairs.
type routes [][2]int

func (r *routes) String() string { return fmt.Sprint(*r) }

func (r *routes) Set(v string) error {
	a, b, ok := strings.Cut(v, "=")
	listen, err1 := strconv.Atoi(a)
	upstream, err2 := strconv.Atoi(b)
	if !ok || err1 != nil || err2 != nil || listen < 1 || listen > 65535 || upstream < 1 || upstream > 65535 {
		return errors.New("a route is listen=upstream, with 2 port numbers")
	}
	*r = append(*r, [2]int{listen, upstream})
	return nil
}

func main() {
	var cfg appclaim.Config
	var pairs routes
	flag.StringVar(&cfg.Issuer, "issuer", "", "The necessary value of the claim iss.")
	flag.StringVar(&cfg.Audience, "audience", "", "A value that the claim aud must have.")
	flag.StringVar(&cfg.CallerClaim, "caller-claim", "sub", "The claim with the caller label.")
	flag.StringVar(&cfg.TenantClaim, "tenant-claim", "", "The claim with the tenant label. Empty: no tenant label.")
	jwks := flag.String("jwks-file", "", "The file with the JWKS of the issuer.")
	reload := flag.Duration("jwks-reload", time.Minute, "The time between 2 reads of the JWKS file.")
	app := flag.String("app", "", "The App name in each span.")
	version := flag.String("version", "", "The AppVersion name in each span.")
	flag.Var(&pairs, "route", "A listen=upstream port pair. Use the flag one time for each port.")
	flag.Parse()

	keys := &appclaim.Keys{}
	if err := errors.Join(cfg.Valid(), keys.Load(*jwks)); err != nil || len(pairs) == 0 {
		fmt.Fprintln(os.Stderr, "The configuration is not valid:", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		tick := time.NewTicker(*reload)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				// A file that is not valid keeps the last valid keys.
				if err := keys.Load(*jwks); err != nil {
					fmt.Fprintln(os.Stderr, "The JWKS reload failed:", err)
				}
			}
		}
	}()

	spans := appclaim.NewSpanWriter(os.Stdout)
	failed := make(chan error, len(pairs))
	servers := make([]*http.Server, 0, len(pairs))
	for _, p := range pairs {
		srv := &http.Server{
			Addr: ":" + strconv.Itoa(p[0]), ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 64 << 10,
			Handler: &appclaim.Check{Config: cfg, Keys: keys, Next: appclaim.Upstream(p[1]), Spans: spans,
				App: *app, Version: *version},
		}
		servers = append(servers, srv)
		go func() { failed <- srv.ListenAndServe() }()
	}
	select {
	case err := <-failed:
		fmt.Fprintln(os.Stderr, "A listener stopped:", err)
		os.Exit(1)
	case <-ctx.Done():
	}
	end, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, srv := range servers {
		_ = srv.Shutdown(end)
	}
}
