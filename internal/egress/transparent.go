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
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

// The DNS responder answers an external name with the proxy address. Thus the pod sends the call
// to the proxy without a proxy setting. The transparent listeners receive these calls.

// ServeTLS accepts the TLS connections on ln until ctx ends. The target is the SNI on TLSPort.
// The proxy does not decrypt. It sends the ClientHello to the target and copies the bytes.
func (p *Proxy) ServeTLS(ctx context.Context, ln net.Listener) error {
	p.init()
	defer context.AfterFunc(ctx, func() { _ = ln.Close() })()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		go p.transparentTLS(ctx, c)
	}
}

func (p *Proxy) transparentTLS(ctx context.Context, client net.Conn) {
	defer func() { _ = client.Close() }()
	start := time.Now()
	port := p.TLSPort
	if port == "" {
		port = "443"
	}
	sp := Span{V: SpanVersion, Time: start.UTC(), Source: sourceIP(client.RemoteAddr().String()), Kind: KindTLS,
		Port: port}
	defer func() {
		sp.DurationMS = float64(time.Since(start).Microseconds()) / 1000
		p.Sink.Write(sp)
	}()
	if p.Identify != nil {
		// A TLS connection has no header. Thus the span has no claimed fields.
		r := &http.Request{RemoteAddr: client.RemoteAddr().String(), Header: http.Header{}}
		if err := p.Identify(r, &sp); err != nil {
			sp.Result, sp.Reason = ResultDenied, reasonIdentity
			return
		}
	}
	hello, ok := p.clientHello(client, client, &sp)
	if !ok {
		return
	}
	host := strings.TrimSuffix(sp.SNI, ".")
	if host == "" {
		sp.Result, sp.Reason = ResultDenied, reasonNoSNI
		return
	}
	sp.Host = host
	upstream, err := p.dial(ctx, host, port)
	if err != nil {
		sp.Result, sp.Reason = ResultError, "upstream"
		if errors.Is(err, errDenied) {
			sp.Result, sp.Reason = ResultDenied, reasonAddress
		}
		return
	}
	defer func() { _ = upstream.Close() }()
	if _, err := upstream.Write(hello); err != nil {
		sp.Result, sp.Reason = ResultError, "upstream write"
		return
	}
	sp.BytesIn, sp.BytesOut = pipe(client, client, upstream)
	sp.BytesOut += int64(len(hello))
	sp.Result = ResultOK
}

// Transparent returns the handler of the transparent HTTP listener. The target of a call is the
// Host header. The port of the Host header must be in HTTPPorts. The default port is 80.
func (p *Proxy) Transparent() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			sp := Span{V: SpanVersion, Time: time.Now().UTC(), Source: sourceIP(r.RemoteAddr), Kind: KindConnect,
				Host: r.Host}
			p.deny(w, &sp, reasonNoProxy)
			p.Sink.Write(sp)
			return
		}
		// A call in origin form, for example "GET /a", becomes a call to http://<Host>/a. Thus the
		// /llm/ route is not available on this listener.
		r.URL.Scheme, r.URL.Host = schemeHTTP, r.Host
		p.ServeHTTP(w, r)
	})
}
