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
	"net"
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
	flag.StringVar(&cfg.Zone, "zone", "", "The zone of the install, in each span. Empty: the install has no zone.")
	flag.StringVar(&cfg.ZoneClaim, "zone-claim", "", "The claim with the zone of the caller. Empty: no zone check.")
	flag.BoolVar(&cfg.RequireZoneClaim, "zone-require-claim", false, "Refuse a token with no zone claim.")
	jwks := flag.String("jwks-file", "", "The file with the JWKS of the issuer.")
	reload := flag.Duration("jwks-reload", time.Minute, "The time between 2 reads of the JWKS file.")
	limitsFile := flag.String("tenants-file", "", "The file with the limits of the tenants. Empty: a tenant has no limit.")
	limitsReload := flag.Duration("tenants-reload", 10*time.Second, "The time between 2 reads of the tenant limits file.")
	app := flag.String("app", "", "The App name in each span.")
	version := flag.String("version", "", "The AppVersion name in each span.")
	flag.Var(&pairs, "route", "A listen=upstream port pair. Use the flag one time for each port.")
	flag.Parse()

	keys := &appclaim.Keys{}
	if err := errors.Join(cfg.Valid(), keys.Load(*jwks)); err != nil || len(pairs) == 0 {
		fmt.Fprintln(os.Stderr, "The configuration is not valid:", err)
		os.Exit(2)
	}
	// The check starts only with valid limits. Thus a pod never serves a tenant with no limit
	// because of a bad file.
	var tenants *appclaim.Tenants
	if *limitsFile != "" {
		tenants = &appclaim.Tenants{}
		if err := tenants.Load(*limitsFile); err != nil || *limitsReload <= 0 {
			fmt.Fprintln(os.Stderr, "The tenant limits are not valid:", err)
			os.Exit(2)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	// A file that is not valid keeps the last valid keys or limits.
	go every(ctx, *reload, "The JWKS reload failed:", func() error { return keys.Load(*jwks) })
	if tenants != nil {
		go every(ctx, *limitsReload, "The reload of the tenant limits failed:",
			func() error { return tenants.Load(*limitsFile) })
	}

	spans := appclaim.NewSpanWriter(os.Stdout)
	failed := make(chan error, len(pairs))
	servers := make([]*http.Server, 0, len(pairs))
	listeners := make([]net.Listener, 0, len(pairs))
	for _, p := range pairs {
		srv := &http.Server{
			Addr: ":" + strconv.Itoa(p[0]), ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 64 << 10,
			Handler: &appclaim.Check{Config: cfg, Keys: keys, Next: appclaim.Upstream(p[1]), Spans: spans,
				Tenants: tenants, App: *app, Version: *version},
		}
		// Bind the ports in order before the first request. Then a bound last port shows that the
		// check owns all its ports, and the startup probe of the container uses that port.
		ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", srv.Addr)
		if err != nil {
			fmt.Fprintln(os.Stderr, "A listener did not start:", err)
			os.Exit(1)
		}
		servers = append(servers, srv)
		listeners = append(listeners, ln)
	}
	for i, srv := range servers {
		go func() { failed <- srv.Serve(listeners[i]) }()
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

// every calls load after each period until ctx ends. It writes an error of load to stderr.
func every(ctx context.Context, period time.Duration, message string, load func() error) {
	tick := time.NewTicker(period)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := load(); err != nil {
				fmt.Fprintln(os.Stderr, message, err)
			}
		}
	}
}
