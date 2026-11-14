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

package appclaim

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// CallerHeader gives the caller label of the claim to the app container.
	CallerHeader = "Koodam-Caller"
	// TenantHeader gives the tenant label of the claim to the app container.
	TenantHeader = "Koodam-Tenant"
	// outcomeOK is the outcome of a request with a valid claim.
	outcomeOK = "ok"
)

// Span is the record of one request at the ingress of an App. It has no token, no path, no
// header and no body.
type Span struct {
	Time       time.Time `json:"time"`
	Kind       string    `json:"kind"`
	App        string    `json:"app,omitempty"`
	Version    string    `json:"version,omitempty"`
	Method     string    `json:"method"`
	Status     int       `json:"status"`
	Outcome    string    `json:"outcome"`
	Caller     string    `json:"caller,omitempty"`
	Tenant     string    `json:"tenant,omitempty"`
	DurationMs float64   `json:"durationMs"`
}

// SpanWriter writes each span as one JSON line.
type SpanWriter struct {
	mu  sync.Mutex
	out io.Writer
}

// NewSpanWriter gives back a writer of span lines.
func NewSpanWriter(out io.Writer) *SpanWriter { return &SpanWriter{out: out} }

// Write writes one span. A write error has no effect on the request.
func (w *SpanWriter) Write(s Span) {
	line, err := json.Marshal(s)
	if err != nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = w.out.Write(append(line, '\n'))
}

// Keys has the current key set. A reload changes the set with no restart.
type Keys struct{ set atomic.Pointer[KeySet] }

// Load reads the JWKS file. If the file is not valid, the last valid set stays.
func (k *Keys) Load(path string) error {
	data, err := os.ReadFile(path) //nolint:gosec // The operator configures the path.
	if err != nil {
		return errors.New("the JWKS file is not readable")
	}
	set, err := ParseKeySet(data)
	if err != nil {
		return err
	}
	k.set.Store(set)
	return nil
}

// Check is the handler of the check container. It sends a request with a valid claim to Next,
// and it refuses each other request with the status 401.
type Check struct {
	Config Config
	Keys   *Keys
	// Next gets the request after the check. The request has no Authorization header.
	Next http.Handler
	// Spans gets one span for each request.
	Spans *SpanWriter
	// App and Version are the names in each span.
	App, Version string
	// Now gives the time. It is time.Now if it is nil.
	Now func() time.Time
}

// methods are the method names that a span can have. Each other method has the name OTHER.
var methods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodPost: true, http.MethodPut: true,
	http.MethodPatch: true, http.MethodDelete: true, http.MethodOptions: true,
}

func (c *Check) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	start := now()
	span := Span{Time: start.UTC(), Kind: "app-ingress", App: c.App, Version: c.Version, Method: "OTHER"}
	if methods[r.Method] {
		span.Method = r.Method
	}
	// A defer writes the span, because the proxy stops an aborted answer with a panic.
	var rec *statusWriter
	defer func() {
		if rec != nil {
			span.Status = rec.status
		}
		span.DurationMs = float64(now().Sub(start).Microseconds()) / 1000
		if c.Spans != nil {
			c.Spans.Write(span)
		}
	}()
	id, err := c.Config.Verify(c.Keys.set.Load(), bearer(r), start)
	// The app gets the labels of the claim only. A caller cannot set them, and the app does not
	// get the token.
	r.Header.Del("Authorization")
	r.Header.Del(CallerHeader)
	r.Header.Del(TenantHeader)
	if err != nil {
		span.Outcome = string(RefusedSyntax)
		if refusal, ok := errors.AsType[Refusal](err); ok {
			span.Outcome = string(refusal)
		}
		span.Status = http.StatusUnauthorized
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "A valid signed claim is necessary.", http.StatusUnauthorized)
	} else {
		span.Outcome, span.Caller, span.Tenant = outcomeOK, id.Caller, id.Tenant
		r.Header.Set(CallerHeader, id.Caller)
		if id.Tenant != "" {
			r.Header.Set(TenantHeader, id.Tenant)
		}
		rec = &statusWriter{ResponseWriter: w, status: http.StatusOK}
		c.Next.ServeHTTP(rec, r)
	}
}

// Upstream gives back a handler that sends each request to a port of the loopback address. The
// app container gets the host name of the request and the address of the client. The handler
// writes no proxy error to a log, because an error can have a header value of the request.
func Upstream(port int) http.Handler {
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host
			pr.Out.Header["X-Forwarded-For"] = pr.In.Header["X-Forwarded-For"]
			pr.SetXForwarded()
			// A Connection header of the caller can name the labels of the claim, and the proxy then
			// removes them. Set them again from the request that Check made.
			for _, h := range []string{CallerHeader, TenantHeader} {
				if v := pr.In.Header.Get(h); v != "" {
					pr.Out.Header.Set(h, v)
				}
			}
		},
		ErrorLog: log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "The app did not answer.", http.StatusBadGateway)
		},
	}
}

// bearer gives back the token of the Authorization header, or an empty string.
func bearer(r *http.Request) string {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return ""
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return token
}

// statusWriter keeps the status of the answer for the span.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(status int) {
	if !w.wrote && status >= 200 {
		w.status, w.wrote = status, true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController flush and hijack the connection for a stream.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
