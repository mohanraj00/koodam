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

// Package egress is the egress proxy core. It forwards the calls of the Agent and Workspace pods
// and writes one span for each call. It reads the SNI of a TLS call and does not decrypt it.
package egress

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The kinds of a span.
const (
	KindConnect = "connect"
	KindHTTP    = "http"
	KindLLM     = "llm"
	KindTLS     = "tls"
)

// The results of a span.
const (
	ResultOK     = "ok"
	ResultDenied = "denied"
	ResultError  = "error"
)

// The reasons of a denied span.
const (
	reasonPort     = "port"
	reasonAddress  = "address"
	reasonIdentity = "identity"
	reasonNoProxy  = "not a proxy request"
	reasonNoTLS    = "no TLS ClientHello"
	reasonSNI      = "the SNI is not the CONNECT host"
	reasonNoSNI    = "no SNI"
	reasonLimit    = "connection limit"
)

// SpanVersion is the version of the span contract in docs/egress-spans.md. An added field keeps
// the version. A removed field, a renamed field or a new meaning of a field changes it.
const SpanVersion = 1

// DefaultIdle is the default of Proxy.Idle. A model stream sends bytes more frequently.
const DefaultIdle = 10 * time.Minute

// schemeHTTP is the URL scheme of plain HTTP.
const schemeHTTP = "http"

// LLMPrefix is the path prefix of the reverse route to the gateway.
const LLMPrefix = "/llm/"

// Span is the record of one call. It has no header, no query string and no body. The path, the
// host and the claimed fields are text from the pod (#170). BytesIn go to the pod, and BytesOut
// come from the pod.
type Span struct {
	V          int       `json:"v"`
	Time       time.Time `json:"time"`
	DurationMS float64   `json:"durationMs"`
	Source     string    `json:"source"`
	Kind       string    `json:"kind"`
	Method     string    `json:"method,omitempty"`
	Host       string    `json:"host,omitempty"`
	SNI        string    `json:"sni,omitempty"`
	Port       string    `json:"port,omitempty"`
	Path       string    `json:"path,omitempty"`
	Status     int       `json:"status,omitempty"`
	BytesIn    int64     `json:"bytesIn"`
	BytesOut   int64     `json:"bytesOut"`
	Result     string    `json:"result"`
	Reason     string    `json:"reason,omitempty"`

	// The identity of the source pod. Identify fills these fields.
	Namespace string `json:"namespace,omitempty"`
	Pod       string `json:"pod,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Run       string `json:"run,omitempty"`
	User      string `json:"user,omitempty"`
	// The identity that the pod claims in a header. The proxy does not trust these fields.
	ClaimedRun  string `json:"claimedRun,omitempty"`
	ClaimedUser string `json:"claimedUser,omitempty"`
}

// Sink receives each span. Write must be safe for concurrent use.
type Sink interface{ Write(Span) }

// JSONSink writes one JSON line for each span.
type JSONSink struct {
	mu  sync.Mutex
	enc *json.Encoder
}

// NewJSONSink returns a sink that writes to w, for example os.Stdout.
func NewJSONSink(w io.Writer) *JSONSink { return &JSONSink{enc: json.NewEncoder(w)} }

func (s *JSONSink) Write(sp Span) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.enc.Encode(sp)
}

var errDenied = errors.New("the address is not permitted")

// Proxy is an http.Handler for CONNECT, for plain HTTP with an absolute URI, and for /llm/.
type Proxy struct {
	// Sink is required.
	Sink Sink
	// Gateway is the target of /llm/. If it is nil, the proxy refuses /llm/.
	Gateway *url.URL
	// HTTPPorts are the permitted plain HTTP ports. The default is 80.
	HTTPPorts []string
	// ConnectPorts are the permitted CONNECT ports. The default is 443.
	ConnectPorts []string
	// HelloTimeout is the time to read the TLS ClientHello. The default is 10 seconds.
	HelloTimeout time.Duration
	// TLSPort is the upstream port of the transparent TLS listener. The default is 443.
	TLSPort string
	// Idle is the time limit with no bytes on a tunnel, a request body or a response. When it
	// ends, the proxy closes the connection. The default is DefaultIdle.
	Idle time.Duration
	// Permit returns true for an address that the proxy can dial. The default is IsPublic.
	Permit func(netip.Addr) bool
	// Resolve finds the addresses of a host name. The default is the system resolver.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	// Identify fills the identity fields of the span before the call. If it returns an error,
	// the proxy refuses the call.
	Identify func(r *http.Request, sp *Span) error

	once      sync.Once
	direct    http.RoundTripper
	toGateway http.RoundTripper
}

func (p *Proxy) init() {
	p.once.Do(func() {
		// An upstream that does not answer cannot hold a call without a limit.
		d := newTransport(time.Minute)
		d.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			return p.dial(ctx, host, port)
		}
		p.direct = d
		// A model can take minutes before the first byte of a long answer.
		p.toGateway = newTransport(10 * time.Minute)
	})
}

// newTransport returns a transport with the values of http.DefaultTransport and no proxy.
func newTransport(header time.Duration) *http.Transport {
	return &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		ResponseHeaderTimeout: header,
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.init()
	start := time.Now()
	sp := Span{V: SpanVersion, Time: start.UTC(), Source: sourceIP(r.RemoteAddr), Method: r.Method, Kind: KindHTTP,
		Host: r.Host, Path: r.URL.Path}
	switch {
	case r.Method == http.MethodConnect:
		sp.Kind = KindConnect
	case !r.URL.IsAbs() && strings.HasPrefix(r.URL.Path, LLMPrefix):
		sp.Kind = KindLLM
	}
	if p.Identify != nil {
		if err := p.Identify(r, &sp); err != nil {
			p.deny(w, &sp, reasonIdentity)
		}
	}
	if sp.Result == "" {
		switch {
		case sp.Kind == KindConnect:
			p.connect(w, r, &sp)
		case sp.Kind == KindLLM && p.Gateway != nil:
			p.llm(w, r, &sp)
		case sp.Kind == KindHTTP && r.URL.IsAbs() && r.URL.Host != "":
			p.forward(w, r, &sp)
		default:
			p.deny(w, &sp, reasonNoProxy)
		}
	}
	sp.DurationMS = float64(time.Since(start).Microseconds()) / 1000
	p.Sink.Write(sp)
}

func (p *Proxy) deny(w http.ResponseWriter, sp *Span, reason string) {
	sp.Result, sp.Reason, sp.Status = ResultDenied, reason, http.StatusForbidden
	http.Error(w, "egress denied: "+reason, http.StatusForbidden)
}

// connect checks the port and the address, then reads the TLS ClientHello and refuses a
// ClientHello with a different SNI. Then it copies the bytes in the 2 directions.
func (p *Proxy) connect(w http.ResponseWriter, r *http.Request, sp *Span) {
	// The request target is the authority of a CONNECT, not the Host header.
	sp.Host = r.URL.Host
	host, port, err := net.SplitHostPort(r.URL.Host)
	sp.Port = port
	if err != nil || !slices.Contains(orDefault(p.ConnectPorts, "443"), port) {
		p.deny(w, sp, reasonPort)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		sp.Result, sp.Reason, sp.Status = ResultError, "no hijack", http.StatusInternalServerError
		http.Error(w, "egress error", http.StatusInternalServerError)
		return
	}
	upstream, err := p.dial(r.Context(), host, port)
	if err != nil {
		p.dialFailed(w, sp, err)
		return
	}
	defer func() { _ = upstream.Close() }()
	client, buf, err := hj.Hijack()
	if err != nil {
		sp.Result, sp.Reason = ResultError, "hijack"
		return
	}
	defer func() { _ = client.Close() }()
	sp.Status = http.StatusOK
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		sp.Result, sp.Reason = ResultError, "client write"
		return
	}
	hello, ok := p.clientHello(client, buf.Reader, sp)
	if !ok {
		return
	}
	// A client sends no SNI for an IP address. For a name, the SNI must be that name, so that the
	// TLS name is the name that the proxy checked.
	want := strings.TrimSuffix(host, ".")
	if _, err := netip.ParseAddr(host); err == nil {
		want = ""
	}
	if !strings.EqualFold(strings.TrimSuffix(sp.SNI, "."), want) {
		sp.Result, sp.Reason = ResultDenied, reasonSNI
		return
	}
	if _, err := upstream.Write(hello); err != nil {
		sp.Result, sp.Reason = ResultError, "upstream write"
		return
	}
	sp.BytesIn, sp.BytesOut = pipe(client, buf.Reader, upstream, p.idle())
	sp.BytesOut += int64(len(hello))
	sp.Result = ResultOK
}

func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, sp *Span) {
	sp.Host = r.URL.Host
	port := r.URL.Port()
	if port == "" {
		port = "80"
	}
	sp.Port = port
	if r.URL.Scheme != schemeHTTP || !slices.Contains(orDefault(p.HTTPPorts, "80"), port) {
		p.deny(w, sp, reasonPort)
		return
	}
	// The Rewrite keeps the absolute URL, and the Host header is the URL host. ReverseProxy removes
	// the hop-by-hop headers, for example Proxy-Authorization, and adds no X-Forwarded-For.
	p.reverse(w, r, sp, p.direct, func(pr *httputil.ProxyRequest) { pr.Out.Host = "" })
}

func (p *Proxy) llm(w http.ResponseWriter, r *http.Request, sp *Span) {
	sp.Host = p.Gateway.Host
	sp.Port = p.Gateway.Port()
	if sp.Port == "" {
		sp.Port = map[string]string{schemeHTTP: "80", "https": "443"}[p.Gateway.Scheme]
	}
	// The gateway key header passes through. The span has no header.
	p.reverse(w, r, sp, p.toGateway, func(pr *httputil.ProxyRequest) {
		root := strings.TrimSuffix(LLMPrefix, "/")
		pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, root)
		pr.Out.URL.RawPath = strings.TrimPrefix(pr.In.URL.RawPath, root)
		pr.SetURL(p.Gateway)
	})
}

func (p *Proxy) reverse(w http.ResponseWriter, r *http.Request, sp *Span, rt http.RoundTripper,
	rewrite func(*httputil.ProxyRequest)) {
	// Each read of the body and each write of the response moves the deadline. A client that
	// stops for p.idle() loses the call.
	rc := http.NewResponseController(w)
	idle := p.idle()
	cw := &countWriter{ResponseWriter: w, extend: func() { _ = rc.SetWriteDeadline(time.Now().Add(idle)) }}
	body := &countReader{ReadCloser: r.Body, extend: func(done bool) {
		// After the body, the server reads the connection to find a close. A deadline there
		// cancels the call, thus the deadline is removed.
		d := time.Now().Add(idle)
		if done {
			d = time.Time{}
		}
		_ = rc.SetReadDeadline(d)
	}}
	r.Body = body
	rp := &httputil.ReverseProxy{
		Rewrite: rewrite, Transport: rt,
		// The gateway streams the tokens. Each write goes to the pod at once.
		FlushInterval: -1,
		// The error text can hold the URL with its query string. Thus the proxy does not log it.
		ErrorLog: log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			p.dialFailed(w, sp, err)
		},
	}
	rp.ServeHTTP(cw, r)
	sp.BytesIn, sp.BytesOut = cw.n, body.n.Load()
	if sp.Result == "" {
		sp.Result, sp.Status = ResultOK, cw.status
	}
}

func (p *Proxy) dialFailed(w http.ResponseWriter, sp *Span, err error) {
	if errors.Is(err, errDenied) {
		p.deny(w, sp, reasonAddress)
		return
	}
	// The reason is fixed text. The error text can hold a URL.
	sp.Result, sp.Reason, sp.Status = ResultError, "upstream", http.StatusBadGateway
	http.Error(w, "egress error", http.StatusBadGateway)
}

// dial resolves host and dials the first address. It refuses the call if one address is not
// permitted. It dials the checked address, thus a second name lookup cannot change it.
func (p *Proxy) dial(ctx context.Context, host, port string) (net.Conn, error) {
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else if p.Resolve != nil {
		addrs, err = p.Resolve(ctx, host)
		if err != nil {
			return nil, err
		}
	} else if addrs, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host); err != nil {
		return nil, err
	}
	permit := p.Permit
	if permit == nil {
		permit = IsPublic
	}
	if len(addrs) == 0 {
		return nil, errors.New("no address")
	}
	for _, a := range addrs {
		if !permit(a.Unmap()) {
			return nil, errDenied
		}
	}
	// One limit for all the addresses of the name.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	d := net.Dialer{Timeout: 10 * time.Second}
	var err error
	for _, a := range addrs {
		var c net.Conn
		if c, err = d.DialContext(ctx, "tcp", net.JoinHostPort(a.Unmap().String(), port)); err == nil {
			return c, nil
		}
	}
	return nil, err
}

var (
	sharedAddress = netip.MustParsePrefix("100.64.0.0/10")
	// A dial to 0.0.0.0/8 can go to the local host.
	thisNetwork = netip.MustParsePrefix("0.0.0.0/8")
)

// IsPublic is false for a loopback, private, link-local, multicast, unspecified, shared
// (100.64.0.0/10) or 0.0.0.0/8 address.
func IsPublic(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsGlobalUnicast() && !a.IsPrivate() && !a.IsLinkLocalUnicast() &&
		!sharedAddress.Contains(a) && !thisNetwork.Contains(a)
}

// clientHello reads the TLS ClientHello from r in the HelloTimeout and sets sp.SNI. If it cannot
// read a ClientHello, it sets the result of sp and returns false.
func (p *Proxy) clientHello(c net.Conn, r io.Reader, sp *Span) ([]byte, bool) {
	timeout := p.HelloTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	hello, sni, isTLS, readErr := readHello(c, r)
	_ = c.SetReadDeadline(time.Time{})
	sp.SNI = sni
	var ne net.Error
	switch {
	case !isTLS && errors.As(readErr, &ne) && ne.Timeout():
		sp.Result, sp.Reason = ResultError, "hello timeout"
		return nil, false
	case !isTLS:
		sp.Result, sp.Reason = ResultDenied, reasonNoTLS
		return nil, false
	}
	return hello, true
}

// readHello reads the TLS ClientHello from r and returns its bytes and its SNI. It does not
// decrypt. isTLS is false if the bytes are not a ClientHello. err is the last read error.
func readHello(c net.Conn, r io.Reader) (hello []byte, sni string, isTLS bool, err error) {
	rc := &recordConn{Conn: c, r: r}
	_ = tls.Server(rc, &tls.Config{GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		sni, isTLS = h.ServerName, true
		return nil, errDenied
	}}).Handshake()
	return rc.buf, sni, isTLS, rc.err
}

// recordConn keeps a copy of the bytes that it reads, and it cannot write.
type recordConn struct {
	net.Conn
	r   io.Reader
	buf []byte
	err error
}

func (c *recordConn) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.buf = append(c.buf, b[:n]...)
	if err != nil {
		c.err = err
	}
	return n, err
}

func (c *recordConn) Write([]byte) (int, error) { return 0, net.ErrClosed }

// pipe copies in the 2 directions. When one direction ends, it closes the 2 connections, so
// that an idle peer cannot keep the other copy and its goroutine.
func pipe(client net.Conn, fromClient io.Reader, upstream net.Conn, idle time.Duration) (in, out int64) {
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	stop := make(chan struct{})
	defer close(stop)
	// If no byte moves in the 2 directions for idle, close the 2 connections. A blocked write
	// also stops the reads, thus it ends the same way.
	go func() {
		t := time.NewTicker(idle / 4)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-t.C:
				if now.Sub(time.Unix(0, last.Load())) >= idle {
					_ = upstream.Close()
					_ = client.Close()
					return
				}
			}
		}
	}()
	done := make(chan int64, 1)
	go func() {
		n, _ := io.Copy(upstream, &activeReader{Reader: fromClient, last: &last})
		_ = upstream.Close()
		_ = client.Close()
		done <- n
	}()
	in, _ = io.Copy(client, &activeReader{Reader: upstream, last: &last})
	_ = upstream.Close()
	_ = client.Close()
	return in, <-done
}

// activeReader records the time of the last read that moved bytes.
type activeReader struct {
	io.Reader
	last *atomic.Int64
}

func (r *activeReader) Read(b []byte) (int, error) {
	n, err := r.Reader.Read(b)
	if n > 0 {
		r.last.Store(time.Now().UnixNano())
	}
	return n, err
}

func (p *Proxy) idle() time.Duration {
	if p.Idle > 0 {
		return p.Idle
	}
	return DefaultIdle
}

func sourceIP(remote string) string {
	if ap, err := netip.ParseAddrPort(remote); err == nil {
		return ap.Addr().Unmap().String()
	}
	return remote
}

func orDefault(s []string, def string) []string {
	if len(s) == 0 {
		return []string{def}
	}
	return s
}

type countWriter struct {
	http.ResponseWriter
	status int
	n      int64
	extend func()
}

func (w *countWriter) WriteHeader(code int) {
	// A 1xx status is not the final status.
	if w.status == 0 && code >= http.StatusOK {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *countWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.extend()
	n, err := w.ResponseWriter.Write(b)
	w.n += int64(n)
	return n, err
}

func (w *countWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush keeps a streamed answer of the gateway streamed.
func (w *countWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// countReader counts the request body. The transport can read the body in a different goroutine,
// thus n is atomic.
type countReader struct {
	io.ReadCloser
	n      atomic.Int64
	extend func(done bool)
}

func (r *countReader) Read(b []byte) (int, error) {
	r.extend(false)
	n, err := r.ReadCloser.Read(b)
	r.n.Add(int64(n))
	// After a timeout, the deadline stays. The transport closes the body, and the close reads the
	// rest of the body. With no deadline, that read waits for the stalled client.
	if errors.Is(err, io.EOF) {
		r.extend(true)
	}
	return n, err
}
