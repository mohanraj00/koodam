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
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	zoneHere, zoneFar = "zone-a", "zone-b"
	farHost           = "models.far.example.com"
)

func mustHostZones(t *testing.T, list string) *HostZones {
	t.Helper()
	z, err := ParseHostZones(list)
	if err != nil {
		t.Fatal(err)
	}
	return z
}

func TestParseHostZonesRefuses(t *testing.T) {
	for name, list := range map[string]string{
		"no zone":            "a.example.com",
		"an empty zone":      "a.example.com=",
		"a zone not a label": "a.example.com=Zone_B",
		"an empty host":      "=zone-b",
		"a host with a path": "a.example.com/x=zone-b",
		"a host with a port": "a.example.com:443=zone-b",
		"an IP range":        "10.0.0.0/8=zone-b",
		"only the wildcard":  "*.=zone-b",
		"a wildcard inside":  "a.*.example.com=zone-b",
		"a host 2 times":     "a.example.com=zone-a,A.Example.com.=zone-b",
		"a pattern 2 times":  "*.example.com=zone-a,*.example.com=zone-b",
	} {
		if z, err := ParseHostZones(list); err == nil {
			t.Errorf("%s: %q is accepted: %+v", name, list, z)
		}
	}
	if z := mustHostZones(t, " , "); z.Len() != 0 {
		t.Errorf("an empty list has %d hosts", z.Len())
	}
}

func TestHostZonesZone(t *testing.T) {
	z := mustHostZones(t, "api.example.com=zone-a, *.example.com=zone-b,*.eu.example.com=zone-c,example.org=zone-b")
	if z.Len() != 4 {
		t.Fatalf("got %d hosts, want 4", z.Len())
	}
	for host, want := range map[string]string{
		"api.example.com":       zoneHere,
		"API.Example.COM.":      zoneHere,
		"other.example.com":     zoneFar,
		"a.b.example.com":       zoneFar,
		"x.eu.example.com":      "zone-c",
		"deep.x.eu.example.com": "zone-c",
		"eu.example.com":        zoneFar,
		"example.org":           zoneFar,
		// A pattern is only for the names below its name. An exact name is only that name.
		"example.com":      "",
		"www.example.org":  "",
		"notexample.com":   "",
		"example.com.evil": "",
		"192.0.2.7":        "",
		"":                 "",
	} {
		if got, ok := z.Zone(host); got != want || ok != (want != "") {
			t.Errorf("%q: got %q and %v, want %q", host, got, ok, want)
		}
	}
	var none *HostZones
	if _, ok := none.Zone("api.example.com"); ok || none.Len() != 0 {
		t.Error("a nil list has a zone")
	}
}

// zoned gives the proxy the zone of the install, 2 declared hosts, and a resolver that counts.
func zoned(lookups *atomic.Int32, zone string) func(*Proxy) {
	return func(p *Proxy) {
		p.Zone = zone
		p.HostZones, _ = ParseHostZones(farHost + "=" + zoneFar + "," + testHost + "=" + zoneHere)
		p.Sink = ZoneSink{Sink: p.Sink, Zone: zone}
		p.Resolve = func(context.Context, string) ([]netip.Addr, error) {
			lookups.Add(1)
			return []netip.Addr{netip.MustParseAddr(loopback)}, nil
		}
	}
}

// A host of a different zone is refused on each path of the proxy, before the name lookup. The
// upstream gets no connection.
func TestOutOfZoneRefused(t *testing.T) {
	var accepted atomic.Int32
	up := httptest.NewUnstartedServer(http.NotFoundHandler())
	up.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			accepted.Add(1)
		}
	}
	up.Start()
	defer up.Close()
	upPort := port(t, up.URL)
	var lookups atomic.Int32
	want := func(sp Span, kind string) {
		t.Helper()
		if sp.Kind != kind || sp.Result != ResultDenied || sp.Reason != ReasonOutOfZone || sp.Zone != zoneHere {
			t.Errorf("span %+v", sp)
		}
	}
	for name, tc := range map[string]struct{ method, target string }{
		"plain HTTP": {http.MethodGet, "http://" + net.JoinHostPort(farHost, upPort) + "/"},
		"upper case": {http.MethodGet, "http://" + net.JoinHostPort(strings.ToUpper(farHost)+".", upPort) + "/"},
		"CONNECT":    {http.MethodConnect, net.JoinHostPort(farHost, upPort)},
		// The proxy cannot find the zone of an address, and the list has a host of a different zone.
		"an IP address":            {http.MethodGet, "http://" + net.JoinHostPort(loopback, upPort) + "/"},
		"CONNECT to an IP address": {http.MethodConnect, net.JoinHostPort(loopback, upPort)},
		"CONNECT to IPv6":          {http.MethodConnect, net.JoinHostPort("::1", upPort)},
		"a number as the host":     {http.MethodGet, "http://2130706433:" + upPort + "/"},
	} {
		pu, sink := start(t, false, upPort, nil, zoned(&lookups, zoneHere))
		c, err := net.Dial("tcp", pu.Host)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(c, tc.method+" "+tc.target+" HTTP/1.1\r\nHost: "+farHost+"\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: tc.method})
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		_ = c.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status %d", name, resp.StatusCode)
		}
		if tc.method != http.MethodConnect && !strings.Contains(string(body), ReasonOutOfZone) {
			t.Errorf("%s: the body %q does not have the reason", name, body)
		}
		kind := KindHTTP
		if tc.method == http.MethodConnect {
			kind = KindConnect
		}
		want(sink.next(t), kind)
	}
	// The transparent TLS listener reads the host from the SNI.
	addr, sink := startTLS(t, false, upPort, zoned(&lookups, zoneHere))
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	if tls.Client(c, &tls.Config{ServerName: farHost, InsecureSkipVerify: true}).Handshake() == nil { //nolint:gosec
		t.Error("the TLS handshake to a host of a different zone passed")
	}
	_ = c.Close()
	want(sink.next(t), KindTLS)
	if lookups.Load() != 0 || accepted.Load() != 0 {
		t.Errorf("%d name lookups and %d upstream connections for refused hosts", lookups.Load(), accepted.Load())
	}
}

// A host of the install zone and a host with no declared zone pass the zone check. An install
// with no zone makes no check.
func TestInZonePasses(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer up.Close()
	upPort := port(t, up.URL)
	for name, tc := range map[string]struct{ zone, host, hosts string }{
		"a host of the install zone":             {zoneHere, testHost, ""},
		"a host with no zone":                    {zoneHere, "plain.example.net", ""},
		"a far host in an install of no zone":    {"", farHost, ""},
		"an IP address in an install of no zone": {"", loopback, ""},
		// With no host of a different zone in the list, an address cannot be such a host.
		"an IP address with no far host": {zoneHere, loopback, testHost + "=" + zoneHere},
		"an IP address with no list":     {zoneHere, loopback, ","},
	} {
		var lookups atomic.Int32
		pu, sink := start(t, false, upPort, nil, zoned(&lookups, tc.zone), func(p *Proxy) {
			if tc.hosts != "" {
				p.HostZones = mustHostZones(t, tc.hosts)
			}
		})
		c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
		resp, err := c.Get("http://" + net.JoinHostPort(tc.host, upPort) + "/")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if sp := sink.next(t); resp.StatusCode != http.StatusOK || sp.Result != ResultOK || sp.Zone != tc.zone {
			t.Errorf("%s: status %d, span %+v", name, resp.StatusCode, sp)
		}
	}
}

func TestIsAddress(t *testing.T) {
	for host, want := range map[string]bool{
		"192.0.2.7": true, "192.0.2.7.": true, "::1": true, "[2001:db8::1]": true, "fe80::1%eth0": true,
		"2130706433": true, "0x7f000001": true, "0x7f.1": true, "example.1": true, "127.1": true,
		"example.com": false, "1.example.com": false, "a1": false, "1a": false, "x0x1": false, "": false,
	} {
		if got := isAddress(host); got != want {
			t.Errorf("%q: got %v, want %v", host, got, want)
		}
	}
	var none *HostZones
	if none.hasOther(zoneHere) || mustHostZones(t, "a.example=zone-a").hasOther(zoneHere) {
		t.Error("a list with no host of a different zone has such a host")
	}
	if !mustHostZones(t, "a.example=zone-b").hasOther(zoneHere) || !mustHostZones(t, "a.example=zone-a,*.b.example=zone-b").hasOther(zoneHere) {
		t.Error("a list with a host of a different zone has no such host")
	}
}
