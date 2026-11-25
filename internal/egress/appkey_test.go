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
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mohanraj00/koodam/internal/gateway"
	"github.com/mohanraj00/koodam/internal/tenanttoken"
)

const (
	authorization = "Authorization"
	teamB         = "team-b"
)

// newKey makes a gateway key as the controller makes it, and its hash.
func newKey(t *testing.T) (key, hash string) {
	t.Helper()
	key, err := gateway.NewKeyValue()
	if err != nil {
		t.Fatal(err)
	}
	return key, gateway.HashKey(key)
}

// keyProxy runs a proxy that examines the App keys. The AppVersion appV1 of tenantNS has the
// gateway key with the hash appHash. identify sets the source pod. The gateway records the
// header and the query of each call.
func keyProxy(t *testing.T, appHash string, identify func(sp *Span)) (pu *url.URL, sink chanSink, calls *[]*http.Request) {
	t.Helper()
	calls = &[]*http.Request{}
	gw := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, r.Clone(r.Context()))
	}))
	t.Cleanup(gw.Close)
	gu, _ := url.Parse(gw.URL)
	token := tokenKey(t, 1)
	pu, sink = start(t, true, "443", gu, func(p *Proxy) {
		p.Identify = func(_ *http.Request, sp *Span) error { identify(sp); return nil }
		p.TenantKey = func(string, string) (ed25519.PublicKey, bool) {
			pub, _ := token.Public().(ed25519.PublicKey)
			return pub, true
		}
		p.AppKeyOwner = func(hash string) (string, string, bool) {
			return tenantNS, appV1, hash == appHash
		}
	})
	return pu, sink, calls
}

func fromAgent(sp *Span) { sp.Namespace, sp.Agent = tenantNS, "a1" }

func fromApp(namespace, version string) func(sp *Span) {
	return func(sp *Span) { sp.Namespace, sp.App, sp.AppVersion = namespace, appName, version }
}

func keyCall(t *testing.T, pu *url.URL, query string, header map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, pu.String()+llmMessages+query, strings.NewReader(`{}`))
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

// keyLocations gives each location and form where the pinned gateway version reads a key. The
// names are the names in the docs.
func keyLocations(key string) map[string]struct {
	query  string
	header map[string]string
} {
	half := len(key) / 2
	basic := base64.StdEncoding.EncodeToString([]byte(key + ":x"))
	h := func(name, value string) map[string]string { return map[string]string{name: value} }
	type call = struct {
		query  string
		header map[string]string
	}
	return map[string]call{
		"Authorization, Bearer":           {header: h(authorization, "Bearer "+key)},
		"Authorization, bearer":           {header: h(authorization, "bearer "+key)},
		"Authorization, Basic":            {header: h(authorization, "Basic "+key)},
		"Authorization, no word":          {header: h(authorization, key)},
		"Authorization, a divided key":    {header: h(authorization, "Bearer "+key[:half]+"Bearer "+key[half:])},
		"Authorization, Basic and base64": {header: h(authorization, "Basic "+basic)},
		"Authorization, base64 with other characters": {
			header: h(authorization, "Basic "+basic[:half]+"-_. "+basic[half:])},
		// The decoder of the gateway ignores a pad character in the first half of a group.
		"Authorization, base64 with a pad character in a group start": {
			header: h(authorization, "Basic "+basic[:8]+"="+basic[8:13]+"=="+basic[13:])},
		"Authorization, base64 with data after the end": {
			header: h(authorization, "Basic "+base64.StdEncoding.EncodeToString([]byte(key+":"))+"0000")},
		"Authorization, an AWS credential with Bearer": {
			header: h(authorization, "AWS4-HMAC-SHA256 Credential=Bearer "+key+"/20260101/us-east-1/bedrock/aws4_request, SignedHeaders=host, Signature=0")},
		"Authorization, an AWS credential": {
			header: h(authorization, "AWS4-HMAC-SHA256 Credential="+key+"/20260101/us-east-1/bedrock/aws4_request, SignedHeaders=host, Signature=0")},
		"x-litellm-api-key":              {header: h("x-litellm-api-key", key)},
		"x-litellm-api-key, Bearer":      {header: h("x-litellm-api-key", "Bearer "+key)},
		"API-Key":                        {header: h("API-Key", key)},
		"x-api-key":                      {header: h("x-api-key", key)},
		"x-goog-api-key":                 {header: h("x-goog-api-key", key)},
		"Ocp-Apim-Subscription-Key":      {header: h("Ocp-Apim-Subscription-Key", key)},
		"litellm_user_api_key":           {header: h("litellm_user_api_key", key)},
		"Sec-WebSocket-Protocol":         {header: h("Sec-WebSocket-Protocol", "realtime, openai-insecure-api-key."+key)},
		"a header name of the operator":  {header: h("X-Team-Key", "Bearer "+key)},
		"the query key":                  {query: "?key=" + key},
		"the query key, with an escape":  {query: "?alt=sse&key=" + strings.Replace(key, "-", "%2D", 1)},
		"a query pair that is not valid": {query: "?a;b=1&key=" + key},
	}
}

// A pod that is not a pod of the AppVersion cannot use the gateway key of the AppVersion through
// the proxy. Without this check, the call of an Agent pod with the key of an App has no tenant.
func TestAppKeyOutOfPodIsRefused(t *testing.T) {
	key, hash := newKey(t)
	pods := map[string]func(*Span){
		"an Agent pod":                                fromAgent,
		"a pod of a different version":                fromApp(tenantNS, appV2),
		"a pod of the version name in a different ns": fromApp(teamB, appV1),
	}
	for pod, identify := range pods {
		for location, c := range keyLocations(key) {
			t.Run(pod+"/"+location, func(t *testing.T) {
				pu, sink, calls := keyProxy(t, hash, identify)
				resp := keyCall(t, pu, c.query, c.header)
				sp := sink.next(t)
				if resp.StatusCode != http.StatusForbidden || resp.Header.Get(ReasonHeader) != ReasonAppKeyOutOfPod ||
					sp.Result != ResultDenied || sp.Reason != ReasonAppKeyOutOfPod || sp.Kind != KindLLM {
					t.Errorf("status %d, header %q, span %+v", resp.StatusCode, resp.Header.Get(ReasonHeader), sp)
				}
				if len(*calls) != 0 {
					t.Error("The gateway got a call that the proxy must refuse.")
				}
			})
		}
	}
}

// The pod of the AppVersion uses its key in each location, and the key gets to the gateway with
// no change.
func TestAppKeyInItsPodPasses(t *testing.T) {
	key, hash := newKey(t)
	token := tenanttoken.Sign(tokenKey(t, 1), testTenant, time.Now().Add(time.Minute))
	for location, c := range keyLocations(key) {
		t.Run(location, func(t *testing.T) {
			pu, sink, calls := keyProxy(t, hash, fromApp(tenantNS, appV1))
			header := map[string]string{tenanttoken.Header: token}
			maps.Copy(header, c.header)
			resp := keyCall(t, pu, c.query, header)
			if sp := sink.next(t); resp.StatusCode != http.StatusOK || sp.Result != ResultOK || len(*calls) != 1 {
				t.Fatalf("status %d, span %+v", resp.StatusCode, sp)
			}
			got := (*calls)[0]
			for k, v := range c.header {
				if got.Header.Get(k) != v {
					t.Errorf("The gateway call has a different value in the header %s.", k)
				}
			}
			// ReverseProxy removes a query pair that is not valid. The key pair stays.
			if c.query != "" && got.URL.Query().Get("key") != key {
				t.Error("The gateway call has a different key in the query.")
			}
		})
	}
}

// A key that is not the key of an AppVersion passes from each pod. A proxy with no AppKeyOwner
// examines no key.
func TestOtherKeysPass(t *testing.T) {
	key, hash := newKey(t)
	own, _ := newKey(t)
	pu, sink, calls := keyProxy(t, hash, fromAgent)
	// The second value has the prefix of a key and no more.
	for _, value := range []string{"Bearer " + own, "Bearer " + keyPrefix, "Bearer " + key[:len(key)-1]} {
		resp := keyCall(t, pu, "", map[string]string{authorization: value})
		if sp := sink.next(t); resp.StatusCode != http.StatusOK || sp.Result != ResultOK {
			t.Fatalf("status %d, span %+v", resp.StatusCode, sp)
		}
	}
	if got := (*calls)[0].Header; got.Get(authorization) != "Bearer "+own || got.Get(customerHeaders[0]) != NoTenantCustomer {
		t.Error("The gateway call must have the key of the pod and the reserved customer.")
	}

	pu, sink, _ = tenantProxy(t, false, map[string]ed25519.PrivateKey{})
	resp := keyCall(t, pu, "", map[string]string{authorization: "Bearer " + key})
	if sp := sink.next(t); resp.StatusCode != http.StatusOK || sp.Result != ResultOK {
		t.Fatalf("status %d, span %+v", resp.StatusCode, sp)
	}
}

// The scan finds a key only with the format of gateway.NewKeyValue. This test fails if that
// format changes.
func TestScanKeysAgreesWithNewKeyValue(t *testing.T) {
	key, _ := newKey(t)
	var got []string
	scanKeys("x"+key+"."+key+keyPrefix, func(k string) { got = append(got, k) })
	if len(got) != 2 || got[0] != key || got[1] != key {
		t.Fatalf("The scan found %d texts.", len(got))
	}
}

// basicText gives the same text as the standard decoder for standard base64 text.
func TestBasicTextDecodesStandardBase64(t *testing.T) {
	for _, text := range []string{"", "a", "ab", "abc", "abcd", "sk-kd-00:x\x00\xff"} {
		if got := basicText(base64.StdEncoding.EncodeToString([]byte(text))); got != text {
			t.Errorf("The decode of %q gives %q.", text, got)
		}
	}
}

// The scan of one call has limits. The proxy refuses a call over a limit, because a key can be
// in the part that it did not examine.
func TestKeyScanLimits(t *testing.T) {
	key, hash := newKey(t)
	texts := func(n int) string {
		var q strings.Builder
		for i := range n {
			fmt.Fprintf(&q, "&a=%s%02x", keyPrefix, i)
		}
		return "?" + q.String()[1:]
	}
	half := len(key) / 2
	long := strings.Repeat("x", maxKeyValue)
	cases := map[string]struct {
		query  string
		header map[string]string
		reason string
	}{
		"the maximum number of key texts": {query: texts(maxKeyTexts)},
		"more key texts than the maximum": {query: texts(maxKeyTexts + 1), reason: ReasonKeyScanLimit},
		"the key of the App after the maximum number of key texts": {
			query: texts(maxKeyTexts) + "&key=" + key, reason: ReasonKeyScanLimit},
		"a long value with no key word": {header: map[string]string{"X-Long": long}},
		"a long value with a key word":  {header: map[string]string{authorization: "Bearer " + long}, reason: ReasonKeyScanLimit},
		"a divided key in a long value": {header: map[string]string{
			authorization: "Bearer " + key[:half] + strings.Repeat("Bearer ", maxKeyValue/7) + key[half:]}, reason: ReasonKeyScanLimit},
		"a long query value with no escape":          {query: "?a=" + long},
		"a long query value with an escape":          {query: "?a=%2D" + long, reason: ReasonKeyScanLimit},
		"the key of the App with a long query value": {query: "?a=%2D" + long + "&key=" + key, reason: ReasonAppKeyOutOfPod},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			pu, sink, calls := keyProxy(t, hash, fromAgent)
			resp := keyCall(t, pu, c.query, c.header)
			sp := sink.next(t)
			if c.reason == "" {
				if resp.StatusCode != http.StatusOK || sp.Result != ResultOK || len(*calls) != 1 {
					t.Errorf("status %d, span result %q, reason %q", resp.StatusCode, sp.Result, sp.Reason)
				}
				return
			}
			if resp.StatusCode != http.StatusForbidden || resp.Header.Get(ReasonHeader) != c.reason ||
				sp.Result != ResultDenied || sp.Reason != c.reason || len(*calls) != 0 {
				t.Errorf("status %d, header %q, span result %q, reason %q", resp.StatusCode,
					resp.Header.Get(ReasonHeader), sp.Result, sp.Reason)
			}
		})
	}
}

// The scan of a long query with many key texts makes no map of the query. It makes only the
// hashes of the first key texts.
func TestKeyScanOfALongQueryAllocatesLittle(t *testing.T) {
	var q strings.Builder
	for i := 0; q.Len() < 1<<20; i++ {
		fmt.Fprintf(&q, "a=%s%06x&", keyPrefix, i)
	}
	raw, sp := q.String(), &Span{}
	owner := func(string) (string, string, bool) { return "", "", false }
	allocs := testing.AllocsPerRun(5, func() {
		s := newKeyScan(owner, sp)
		if s.query(raw); !s.limit {
			t.Error("The scan must be at its limit.")
		}
	})
	if allocs > 4*maxKeyTexts {
		t.Errorf("The scan made %.0f allocations.", allocs)
	}
}
