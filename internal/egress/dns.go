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
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	dnsTimeout = 5 * time.Second
	// maxDNSQueries is the limit of the queries that the responder answers at the same time.
	maxDNSQueries = 512
	netUDP        = "udp"
)

// Resolver is the DNS server of the Agent and Workspace pods. It sends a name in the cluster
// domain to the cluster DNS. It answers each other name itself: the A record is Answer, and the
// other types are empty. It never sends an external name upstream, so a query cannot carry data
// out of the cluster.
type Resolver struct {
	// Answer is the IPv4 address of the proxy. It is required.
	Answer netip.Addr
	// ClusterDNS is the host:port of the cluster DNS. It is required.
	ClusterDNS string
	// ClusterDomain is the cluster domain. The default is cluster.local.
	ClusterDomain string
	// TTL is the time to live of an answer, in seconds. The default is 30.
	TTL uint32
	// Exchange sends a query to the cluster DNS and returns the response. The default dials
	// ClusterDNS with network ("udp" or "tcp").
	Exchange func(ctx context.Context, query []byte, network string) ([]byte, error)
}

// Respond returns the response to query. It returns nil if query has no valid header.
func (r *Resolver) Respond(ctx context.Context, query []byte, network string) []byte {
	var p dnsmessage.Parser
	h, err := p.Start(query)
	if err != nil {
		return nil
	}
	qs, err := p.AllQuestions()
	if err != nil || len(qs) != 1 || qs[0].Class != dnsmessage.ClassINET {
		return r.reply(h, nil, dnsmessage.RCodeFormatError, nil)
	}
	q := qs[0]
	if r.inCluster(q.Name.String()) {
		resp, err := r.exchange(ctx, query, network)
		if err != nil {
			return r.reply(h, &q, dnsmessage.RCodeServerFailure, nil)
		}
		return resp
	}
	var a *dnsmessage.AResource
	if q.Type == dnsmessage.TypeA {
		a = &dnsmessage.AResource{A: r.Answer.As4()}
	}
	return r.reply(h, &q, dnsmessage.RCodeSuccess, a)
}

// inCluster is true for a name in the cluster domain.
func (r *Resolver) inCluster(name string) bool {
	domain := r.ClusterDomain
	if domain == "" {
		domain = "cluster.local"
	}
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	domain = strings.ToLower(strings.Trim(domain, "."))
	return name == domain || strings.HasSuffix(name, "."+domain)
}

// reply builds a response to the query with header h. q and a can be nil.
func (r *Resolver) reply(h dnsmessage.Header, q *dnsmessage.Question, rcode dnsmessage.RCode,
	a *dnsmessage.AResource) []byte {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, OpCode: h.OpCode,
		RecursionDesired: h.RecursionDesired, RecursionAvailable: true, RCode: rcode})
	b.EnableCompression()
	if q != nil {
		_ = b.StartQuestions()
		_ = b.Question(*q)
		if a != nil {
			ttl := r.TTL
			if ttl == 0 {
				ttl = 30
			}
			_ = b.StartAnswers()
			_ = b.AResource(dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: ttl}, *a)
		}
	}
	msg, err := b.Finish()
	if err != nil {
		return nil
	}
	return msg
}

func (r *Resolver) exchange(ctx context.Context, query []byte, network string) ([]byte, error) {
	if r.Exchange != nil {
		return r.Exchange(ctx, query, network)
	}
	ctx, cancel := context.WithTimeout(ctx, dnsTimeout)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(ctx, network, r.ClusterDNS)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	deadline, _ := ctx.Deadline()
	_ = c.SetDeadline(deadline)
	if network == netUDP {
		if _, err := c.Write(query); err != nil {
			return nil, err
		}
		buf := make([]byte, 65535)
		n, err := c.Read(buf)
		if err != nil {
			return nil, err
		}
		return buf[:n], nil
	}
	if err := writeTCPMessage(c, query); err != nil {
		return nil, err
	}
	return readTCPMessage(c)
}

// ServeUDP answers the queries on pc until ctx ends.
func (r *Resolver) ServeUDP(ctx context.Context, pc net.PacketConn) error {
	go func() {
		<-ctx.Done()
		_ = pc.Close()
	}()
	slots := make(chan struct{}, maxDNSQueries)
	for {
		buf := make([]byte, 65535)
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			// Too many queries at the same time. The client sends the query again.
			continue
		}
		go func() {
			defer func() { <-slots }()
			if resp := r.Respond(ctx, buf[:n], netUDP); resp != nil {
				_, _ = pc.WriteTo(resp, addr)
			}
		}()
	}
}

// ServeTCP answers the queries on ln until ctx ends.
func (r *Resolver) ServeTCP(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	slots := make(chan struct{}, maxDNSQueries)
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			_ = c.Close()
			continue
		}
		go func() {
			defer func() { <-slots }()
			defer func() { _ = c.Close() }()
			for {
				_ = c.SetDeadline(time.Now().Add(10 * time.Second))
				query, err := readTCPMessage(c)
				if err != nil {
					return
				}
				resp := r.Respond(ctx, query, "tcp")
				if resp == nil || writeTCPMessage(c, resp) != nil {
					return
				}
			}
		}()
	}
}

// readTCPMessage reads a DNS message with its 2-byte length prefix.
func readTCPMessage(c io.Reader) ([]byte, error) {
	var n [2]byte
	if _, err := io.ReadFull(c, n[:]); err != nil {
		return nil, err
	}
	msg := make([]byte, binary.BigEndian.Uint16(n[:]))
	if _, err := io.ReadFull(c, msg); err != nil {
		return nil, err
	}
	return msg, nil
}

// writeTCPMessage writes a DNS message with its 2-byte length prefix.
func writeTCPMessage(c io.Writer, msg []byte) error {
	if len(msg) > 65535 {
		return errors.New("the DNS message is too large")
	}
	_, err := c.Write(append(binary.BigEndian.AppendUint16(nil, uint16(len(msg))), msg...))
	return err
}
