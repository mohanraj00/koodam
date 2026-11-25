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
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mohanraj00/koodam/internal/tenanttoken"
)

func tokenKey(t *testing.T, fill byte) ed25519.PrivateKey {
	t.Helper()
	key, err := tenanttoken.PrivateKey(bytes.Repeat([]byte{fill}, tenanttoken.SeedSize))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

const (
	testTenant = "acme"
	tenantNS   = "team-a"
)

// tenantProxy runs a proxy with a gateway that records the headers of each call. The source pod
// is a pod of the AppVersion appV1 of tenantNS. If app is false, it is an Agent pod. keys has
// the token key of each AppVersion. If it is nil, the proxy examines no tenant token.
func tenantProxy(t *testing.T, app bool, keys map[string]ed25519.PrivateKey) (pu *url.URL, sink chanSink, calls *[]http.Header) {
	t.Helper()
	calls = &[]http.Header{}
	gw := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, r.Header.Clone())
	}))
	t.Cleanup(gw.Close)
	gu, _ := url.Parse(gw.URL)
	pu, sink = start(t, true, "443", gu, func(p *Proxy) {
		p.Identify = func(_ *http.Request, sp *Span) error {
			sp.Namespace, sp.Agent = tenantNS, "a1"
			if app {
				sp.Agent, sp.App, sp.AppVersion = "", appName, appV1
			}
			return nil
		}
		if keys != nil {
			p.TenantKey = func(namespace, version string) (ed25519.PublicKey, bool) {
				key, ok := keys[namespace+"/"+version]
				if !ok {
					return nil, false
				}
				pub, _ := key.Public().(ed25519.PublicKey)
				return pub, true
			}
		}
	})
	return pu, sink, calls
}

func modelCall(t *testing.T, pu *url.URL, header map[string]string) *http.Response {
	t.Helper()
	// The body has the label of a different tenant. The gateway reads the header first.
	req, _ := http.NewRequest(http.MethodPost, pu.String()+llmMessages, strings.NewReader(`{"user":"victim"}`))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp
}

func TestTenantTokenBindsTheModelCall(t *testing.T) {
	key := tokenKey(t, 1)
	pu, sink, calls := tenantProxy(t, true, map[string]ed25519.PrivateKey{tenantNS + "/" + appV1: key})
	token := tenanttoken.Sign(key, testTenant, time.Now().Add(time.Minute))
	// The pod sends the token of one tenant and the customer headers of a different tenant.
	resp := modelCall(t, pu, map[string]string{tenanttoken.Header: token,
		"x-litellm-customer-id": "victim", "X-LiteLLM-End-User-Id": "victim"})
	sp := sink.next(t)
	if resp.StatusCode != http.StatusOK || sp.Result != ResultOK || len(*calls) != 1 {
		t.Fatalf("status %d, span %+v, %d gateway calls", resp.StatusCode, sp, len(*calls))
	}
	got := (*calls)[0]
	if v := got.Values(customerHeaders[0]); len(v) != 1 || v[0] != testTenant {
		t.Errorf("The customer header of the gateway call is %q.", v)
	}
	if got.Get(customerHeaders[1]) != "" || got.Get(tenanttoken.Header) != "" {
		t.Errorf("The gateway call has a header of the pod: %v", got)
	}
	var out bytes.Buffer
	NewJSONSink(&out).Write(sp)
	if strings.Contains(out.String(), token) || strings.Contains(out.String(), strings.Split(token, ".")[2]) ||
		strings.Contains(out.String(), testTenant) {
		t.Errorf("The span has the token or the tenant label: %s", out.String())
	}
}

func TestTenantTokenRefusals(t *testing.T) {
	key := tokenKey(t, 1)
	keys := map[string]ed25519.PrivateKey{tenantNS + "/" + appV1: key,
		// The same AppVersion name in a different namespace, and a different version of the App.
		"team-b/" + appV1: tokenKey(t, 2), tenantNS + "/" + appV2: tokenKey(t, 3)}
	end := time.Now().Add(time.Minute)
	good := tenanttoken.Sign(key, testTenant, end)
	dot := strings.LastIndexByte(good, '.')
	other := tenanttoken.Sign(key, "other", end)
	cases := []struct{ name, token, reason string }{
		{"no token", "", ReasonTenantTokenMissing},
		{"a token of the same version name in a different namespace",
			tenanttoken.Sign(keys["team-b/"+appV1], testTenant, end), ReasonTenantTokenInvalid},
		{"a token of a different version", tenanttoken.Sign(keys[tenantNS+"/"+appV2], testTenant, end), ReasonTenantTokenInvalid},
		{"a different tenant with the signature of a token", other[:strings.LastIndexByte(other, '.')] + good[dot:],
			ReasonTenantTokenInvalid},
		{"a forged signature", good[:dot+1] + strings.Repeat("A", len(good)-dot-1), ReasonTenantTokenInvalid},
		{"text that is not a token", testTenant, ReasonTenantTokenInvalid},
		{"a token after its end time", tenanttoken.Sign(key, testTenant, time.Now().Add(-time.Second)), ReasonTenantTokenExpired},
		{"a label that is not ASCII", tenanttoken.Sign(key, "tenant-ü", end), ReasonTenantLabelNotASCII},
		{"a label with a space at its end", tenanttoken.Sign(key, testTenant+" ", end), ReasonTenantLabelNotASCII},
	}
	refused := func(t *testing.T, keys map[string]ed25519.PrivateKey, token, reason string) {
		t.Helper()
		pu, sink, calls := tenantProxy(t, true, keys)
		header := map[string]string{customerHeaders[0]: testTenant}
		if token != "" {
			header[tenanttoken.Header] = token
		}
		resp := modelCall(t, pu, header)
		sp := sink.next(t)
		if resp.StatusCode != http.StatusForbidden || resp.Header.Get(ReasonHeader) != reason ||
			sp.Result != ResultDenied || sp.Reason != reason || sp.Kind != KindLLM {
			t.Errorf("status %d, header %q, span %+v", resp.StatusCode, resp.Header.Get(ReasonHeader), sp)
		}
		if len(*calls) != 0 {
			t.Error("The gateway got a call that the proxy must refuse.")
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { refused(t, keys, c.token, c.reason) })
	}
	// With no key for the AppVersion of the pod, the proxy refuses also a call with a token.
	t.Run("no key for the version", func(t *testing.T) {
		refused(t, map[string]ed25519.PrivateKey{}, good, ReasonTenantTokenNoKey)
	})
}

func TestCallsWithNoTenantPass(t *testing.T) {
	key := tokenKey(t, 1)
	for name, c := range map[string]struct {
		app  bool
		keys map[string]ed25519.PrivateKey
	}{
		"a pod that is not an App pod":    {false, map[string]ed25519.PrivateKey{tenantNS + "/" + appV1: key}},
		"a proxy with no tenant key func": {true, nil},
	} {
		t.Run(name, func(t *testing.T) {
			pu, sink, calls := tenantProxy(t, c.app, c.keys)
			resp := modelCall(t, pu, map[string]string{tenanttoken.Header: "x", customerHeaders[0]: run1})
			if sp := sink.next(t); resp.StatusCode != http.StatusOK || sp.Result != ResultOK || len(*calls) != 1 {
				t.Fatalf("status %d, span %+v", resp.StatusCode, sp)
			}
			// The gateway never gets the token header. The other headers of the pod do not change.
			if got := (*calls)[0]; got.Get(tenanttoken.Header) != "" || got.Get(customerHeaders[0]) != run1 {
				t.Errorf("The gateway call: %v", got)
			}
		})
	}
}

func TestHeaderText(t *testing.T) {
	for s, want := range map[string]bool{testTenant: true, "a b": true, "~!": true, "": false, " a": false, "a ": false,
		"a\tb": false, "a\nb": false, "ü": false, "a\x7f": false} {
		if headerText(s) != want {
			t.Errorf("headerText(%q) must be %v.", s, want)
		}
	}
}
