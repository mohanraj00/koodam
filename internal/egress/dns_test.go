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
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

var proxyIP = netip.MustParseAddr("10.96.100.10")

const (
	anthropicName = "api.anthropic.com."
	clusterAnswer = "10.0.0.2"
)

func query(t *testing.T, name string, qt dnsmessage.Type, questions int) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 7, RecursionDesired: true})
	_ = b.StartQuestions()
	for range questions {
		if err := b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName(name), Type: qt,
			Class: dnsmessage.ClassINET}); err != nil {
			t.Fatal(err)
		}
	}
	msg, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func parse(t *testing.T, msg []byte) dnsmessage.Message {
	t.Helper()
	var m dnsmessage.Message
	if err := m.Unpack(msg); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	return m
}

// upstream is an Exchange that records the queries and returns a fixed response.
type upstream struct {
	queries [][]byte
	err     error
}

func (u *upstream) exchange(_ context.Context, q []byte, _ string) ([]byte, error) {
	u.queries = append(u.queries, q)
	return []byte("cluster answer"), u.err
}

func TestResolverExternalNames(t *testing.T) {
	up := &upstream{}
	r := &Resolver{Answer: proxyIP, Exchange: up.exchange}
	for _, tc := range []struct {
		name string
		qt   dnsmessage.Type
		want int
	}{
		{anthropicName, dnsmessage.TypeA, 1},
		{"API.Anthropic.COM.", dnsmessage.TypeA, 1},
		{anthropicName, dnsmessage.TypeAAAA, 0},
		{anthropicName, dnsmessage.TypeHTTPS, 0},
		{anthropicName, dnsmessage.TypeTXT, 0},
		{"4.3.2.1.in-addr.arpa.", dnsmessage.TypePTR, 0},
		// A name that only ends with the domain text is not in the cluster domain.
		{"evilcluster.local.", dnsmessage.TypeA, 1},
	} {
		m := parse(t, r.Respond(t.Context(), query(t, tc.name, tc.qt, 1), netUDP))
		if m.ID != 7 || !m.Response || m.RCode != dnsmessage.RCodeSuccess || len(m.Questions) != 1 {
			t.Errorf("%s %v: header %+v, %d questions", tc.name, tc.qt, m.Header, len(m.Questions))
		}
		if len(m.Answers) != tc.want {
			t.Fatalf("%s %v: %d answers, want %d", tc.name, tc.qt, len(m.Answers), tc.want)
		}
		if tc.want == 1 {
			a, ok := m.Answers[0].Body.(*dnsmessage.AResource)
			if !ok || netip.AddrFrom4(a.A) != proxyIP || m.Answers[0].Header.TTL != 30 {
				t.Errorf("%s: answer %+v", tc.name, m.Answers[0])
			}
		}
	}
	if len(up.queries) != 0 {
		t.Errorf("the resolver sent %d external names upstream", len(up.queries))
	}
}

func TestResolverClusterNames(t *testing.T) {
	up := &upstream{}
	r := &Resolver{Answer: proxyIP, ClusterDomain: "example.internal", Exchange: up.exchange}
	for _, name := range []string{"kubernetes.default.svc.example.internal.", "Example.Internal."} {
		q := query(t, name, dnsmessage.TypeA, 1)
		if got := r.Respond(t.Context(), q, "tcp"); string(got) != "cluster answer" {
			t.Errorf("%s: got %q", name, got)
		}
		if !bytes.Equal(up.queries[len(up.queries)-1], q) {
			t.Errorf("%s: the query changed", name)
		}
	}
	// The default domain is cluster.local.
	if parse(t, (&Resolver{Answer: proxyIP, Exchange: up.exchange}).Respond(t.Context(),
		query(t, "kubernetes.default.svc.example.internal.", dnsmessage.TypeA, 1), netUDP)).Answers == nil {
		t.Error("a name out of cluster.local got no proxy answer")
	}

	up.err = errors.New("no cluster DNS")
	m := parse(t, r.Respond(t.Context(), query(t, "a.svc.example.internal.", dnsmessage.TypeA, 1), netUDP))
	if m.RCode != dnsmessage.RCodeServerFailure || len(m.Answers) != 0 {
		t.Errorf("upstream error: %+v", m)
	}
}

func TestResolverBadQueries(t *testing.T) {
	r := &Resolver{Answer: proxyIP, Exchange: (&upstream{}).exchange}
	if got := r.Respond(t.Context(), []byte{1, 2, 3}, netUDP); got != nil {
		t.Errorf("a short message got %q", got)
	}
	for name, q := range map[string][]byte{
		"no question":  query(t, "a.example.", dnsmessage.TypeA, 0),
		"2 questions":  query(t, "a.example.", dnsmessage.TypeA, 2),
		"bad question": append(query(t, "a.example.", dnsmessage.TypeA, 0)[:12:12], 0xff),
	} {
		if name == "bad question" {
			q[5] = 1 // 1 question in the header, with no valid question.
		}
		m := parse(t, r.Respond(t.Context(), q, netUDP))
		if m.RCode != dnsmessage.RCodeFormatError || m.ID != 7 || len(m.Answers) != 0 {
			t.Errorf("%s: %+v", name, m.Header)
		}
	}
}

func TestResolverServe(t *testing.T) {
	cluster := &Resolver{Answer: netip.MustParseAddr(clusterAnswer), ClusterDomain: "unused.invalid"}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// The cluster DNS is a second resolver that answers each name.
	cpc, err := net.ListenPacket(netUDP, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cln, err := net.Listen("tcp", cpc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = cluster.ServeUDP(ctx, cpc) }()
	go func() { _ = cluster.ServeTCP(ctx, cln) }()

	r := &Resolver{Answer: proxyIP, ClusterDNS: cpc.LocalAddr().String()}
	pc, err := net.ListenPacket(netUDP, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = r.ServeUDP(ctx, pc) }()
	go func() { _ = r.ServeTCP(ctx, ln) }()

	for _, tc := range []struct{ network, addr, name, want string }{
		{netUDP, pc.LocalAddr().String(), "api.openai.com.", "10.96.100.10"},
		{netUDP, pc.LocalAddr().String(), "kubernetes.default.svc.cluster.local.", clusterAnswer},
		{"tcp", ln.Addr().String(), "github.com.", "10.96.100.10"},
		{"tcp", ln.Addr().String(), "kubernetes.default.svc.cluster.local.", clusterAnswer},
	} {
		c, err := net.DialTimeout(tc.network, tc.addr, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		q := query(t, tc.name, dnsmessage.TypeA, 1)
		var resp []byte
		if tc.network == netUDP {
			_, err = c.Write(q)
			buf := make([]byte, 512)
			var n int
			if err == nil {
				n, err = c.Read(buf)
			}
			resp = buf[:n]
		} else if err = writeTCPMessage(c, q); err == nil {
			resp, err = readTCPMessage(c)
		}
		_ = c.Close()
		if err != nil {
			t.Fatalf("%s %s: %v", tc.network, tc.name, err)
		}
		m := parse(t, resp)
		if len(m.Answers) != 1 || netip.AddrFrom4(m.Answers[0].Body.(*dnsmessage.AResource).A).String() != tc.want {
			t.Errorf("%s %s: answers %+v, want %s", tc.network, tc.name, m.Answers, tc.want)
		}
	}
}
