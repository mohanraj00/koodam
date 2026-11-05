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

package ingress

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/api/equality"
)

// The tests never print, serialize or store a dummy credential, and a failure message has fixed
// text. The values are only in memory in this test process. Each accepted credential comes from
// opaque. A fixed string in a test is only a malformed syntax control, never a credential.

const (
	testNamespace  = "team-a"
	testName       = "caller"
	testUID        = "uid-1"
	testUser       = serviceAccountPrefix + testNamespace + ":" + testName
	scheme         = "Bearer "
	forwardedProto = "X-Forwarded-Proto"
	https          = "https"
)

// opaque returns a new random dummy credential. It is not a Kubernetes credential.
func opaque() string {
	return rand.Text()
}

// jwtShaped returns a random dummy credential with the shape of a JWT. Its claims name an other
// ServiceAccount. The checks must not decode them.
func jwtShaped() string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." +
		enc([]byte(`{"sub":"system:serviceaccount:kube-system:forged"}`)) + "." + opaque()
}

// request returns a request on TLS with the headers in h.
func request(h http.Header) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/run/team-a/web/", nil)
	r.Header = h
	r.TLS = &tls.ConnectionState{}
	return r
}

func headersEqual(a, b http.Header) bool {
	return maps.EqualFunc(a, b, slices.Equal)
}

func TestBearer(t *testing.T) {
	tok, jwt := opaque(), jwtShaped()
	// all has each permitted token character: a random prefix, then a fixed suffix with the
	// characters that the prefix may not have. It is a dummy credential only in memory.
	all := opaque() + "az0189-._~+/="
	const lower = "authorization"
	auth := func(vs ...string) http.Header { return http.Header{authorizationHeader: vs} }
	for _, tc := range []struct {
		name   string
		header http.Header
		want   string // The empty string means a refusal.
	}{
		{"one bearer", auth(scheme + tok), tok},
		{"lower case scheme", auth("bearer " + tok), tok},
		{"upper case scheme", auth("BEARER " + tok), tok},
		{"padding", auth(scheme + tok + "=="), tok + "=="},
		{"jwt shape is opaque", auth(scheme + jwt), jwt},
		{"all token characters", auth(scheme + all), all},
		{"lower case key only", http.Header{lower: {scheme + tok}}, tok},
		{"mixed case key only", http.Header{"AUTHorization": {scheme + tok}}, tok},
		{"no header", http.Header{}, ""},
		{"no value", http.Header{authorizationHeader: nil}, ""},
		{"empty value", auth(""), ""},
		{"scheme only", auth(strings.TrimSpace(scheme)), ""},
		{"scheme and space", auth(scheme), ""},
		{"padding only", auth(scheme + "=="), ""},
		{"padding in token", auth(scheme + "a=" + tok), ""},
		{"two spaces", auth(scheme + " " + tok), ""},
		{"leading space", auth(" " + scheme + tok), ""},
		{"trailing space", auth(scheme + tok + " "), ""},
		{"tab", auth(strings.TrimSpace(scheme) + "\t" + tok), ""},
		{"no space", auth(strings.TrimSpace(scheme) + tok), ""},
		{"nul", auth(scheme + tok + "\x00"), ""},
		{"line feed", auth(scheme + tok + "\n"), ""},
		{"delete", auth(scheme + tok + "\x7f"), ""},
		{"non ascii", auth(scheme + tok + "é"), ""},
		{"basic scheme", auth("Basic " + tok), ""},
		{"other scheme", auth("Token " + tok), ""},
		{"comma list", auth(scheme + tok + ", " + scheme + jwt), ""},
		{"comma in token", auth(scheme + tok + "," + jwt), ""},
		{"two tokens", auth(scheme + tok + " " + jwt), ""},
		{"two values", auth(scheme+tok, scheme+jwt), ""},
		{"two equal values", auth(scheme+tok, scheme+tok), ""},
		{"canonical and lower case keys", http.Header{
			authorizationHeader: {scheme + tok}, lower: {scheme + jwt}}, ""},
		{"canonical and empty lower case key", http.Header{
			authorizationHeader: {scheme + tok}, lower: nil}, ""},
		{"lower and mixed case keys", http.Header{
			lower: {scheme + tok}, "AUTHorization": {scheme + tok}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := request(tc.header)
			before := r.Header.Clone()
			got, err := bearer(r)
			if tc.want == "" {
				if err != ErrAuthentication || got != "" {
					t.Fatal("bearer did not refuse the request with ErrAuthentication")
				}
			} else if err != nil || got != tc.want {
				t.Fatal("bearer did not return the one token")
			}
			if !headersEqual(before, r.Header) {
				t.Fatal("bearer changed the request headers")
			}
		})
	}
}

func TestBearerNeedsTLS(t *testing.T) {
	if got, err := bearer(nil); err != ErrAuthentication || got != "" {
		t.Fatal("bearer did not refuse a nil request")
	}
	r := request(http.Header{
		authorizationHeader: {scheme + opaque()},
		forwardedProto:      {https},
		"X-Forwarded-Ssl":   {"on"},
		"Forwarded":         {"proto=https"},
	})
	r.TLS = nil
	if got, err := bearer(r); err != ErrAuthentication || got != "" {
		t.Fatal("bearer accepted a forwarded protocol header in place of TLS")
	}
}

func TestCallerFrom(t *testing.T) {
	user := func(name, uid string) authenticationv1.UserInfo {
		return authenticationv1.UserInfo{Username: name, UID: uid,
			Groups: []string{"system:serviceaccounts"}, Extra: map[string]authenticationv1.ExtraValue{
				"authentication.kubernetes.io/pod-name": {"pod"}}}
	}
	sa := func(namespace, name string) string { return serviceAccountPrefix + namespace + ":" + name }
	for _, tc := range []struct {
		name string
		user authenticationv1.UserInfo
		want Caller // The empty Caller means a refusal.
	}{
		{"service account", user(testUser, testUID), Caller{testNamespace, testName, testUID}},
		{"dotted name", user(sa(testNamespace, "a.b"), testUID), Caller{testNamespace, "a.b", testUID}},
		{"empty uid", user(testUser, ""), Caller{}},
		{"three parts", user(serviceAccountPrefix+testNamespace, testUID), Caller{}},
		{"five parts", user(testUser+":extra", testUID), Caller{}},
		{"empty namespace", user(sa("", testName), testUID), Caller{}},
		{"empty name", user(sa(testNamespace, ""), testUID), Caller{}},
		{"upper case namespace", user(sa("Team-A", testName), testUID), Caller{}},
		{"dotted namespace", user(sa("team.a", testName), testUID), Caller{}},
		{"long namespace", user(sa(strings.Repeat("a", 64), testName), testUID), Caller{}},
		{"upper case name", user(sa(testNamespace, "Caller"), testUID), Caller{}},
		{"long name", user(sa(testNamespace, strings.Repeat("a", 254)), testUID), Caller{}},
		{"plural prefix", user("system:serviceaccounts:"+testNamespace+":"+testName, testUID), Caller{}},
		{"upper case prefix", user("System:serviceaccount:"+testNamespace+":"+testName, testUID), Caller{}},
		{"leading space", user(" "+testUser, testUID), Caller{}},
		{"node", user("system:node:node-1", testUID), Caller{}},
		{"four part non service account", user("system:node:"+testNamespace+":"+testName, testUID), Caller{}},
		{"plain user", user("alice", testUID), Caller{}},
		{"empty user", user("", testUID), Caller{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.user.DeepCopy()
			got, err := callerFrom(tc.user)
			if tc.want == (Caller{}) {
				if err != ErrAuthentication || got != (Caller{}) {
					t.Fatal("callerFrom did not refuse the user with ErrAuthentication and an empty Caller")
				}
			} else if err != nil || got != tc.want {
				t.Fatal("callerFrom did not return the ServiceAccount identity")
			}
			if !equality.Semantic.DeepEqual(*before, tc.user) {
				t.Fatal("callerFrom changed the user")
			}
		})
	}
}

// TestCallerFields makes sure that Caller has only the proved identity.
func TestCallerFields(t *testing.T) {
	typ := reflect.TypeFor[Caller]()
	got := make([]string, 0, typ.NumField())
	for f := range typ.Fields() {
		got = append(got, f.Name)
	}
	if !slices.Equal(got, []string{"Namespace", "Name", "UID"}) {
		t.Fatal("Caller has fields other than Namespace, Name and UID")
	}
}
