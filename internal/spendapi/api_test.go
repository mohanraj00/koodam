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

package spendapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mohanraj00/koodam/docs"
	"github.com/mohanraj00/koodam/internal/appclaim"
)

const testPeriod = spendPath + "?from=" + testFrom + "&to=" + testTo + "&groupBy=label:"

func (f fixture) call(t testing.TB, method, path, token string, status int) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	f.ServeHTTP(w, r)
	body := w.Body.Bytes()
	for _, credential := range []string{token, f.master, f.token} {
		if credential != "" && bytes.Contains(body, []byte(credential)) {
			t.Fatal("the response body contains a credential")
		}
	}
	for _, key := range f.runKeys {
		if bytes.Contains(body, []byte(key.value)) || bytes.Contains(body, []byte(key.hash)) {
			t.Fatal("the response body contains a key value or hash")
		}
	}
	if w.Code != status {
		t.Fatalf("%s status = %d, want %d", method, w.Code, status)
	}
	if method == http.MethodHead && w.Body.Len() != 0 {
		t.Fatal("HEAD returned a response body")
	}
	return w
}

func decode[T any](t testing.TB, w *httptest.ResponseRecorder) T {
	t.Helper()
	var body T
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestServeRunsAndSpend(t *testing.T) {
	f := newFixture(t, 2)
	f.serveGateway(t)
	p := decode[page[run]](t, f.call(t, http.MethodGet, runsPath+"?limit=1", f.token, http.StatusOK))
	if len(p.Items) != 1 || spendOf(p.Items[0].amounts) != 0.75 || p.NextCursor == "" {
		t.Fatal("want one run and a cursor")
	}
	next := runsPath + "?limit=1&cursor=" + p.NextCursor
	p = decode[page[run]](t, f.call(t, http.MethodGet, next, f.token, http.StatusOK))
	if len(p.Items) != 1 || p.Items[0].Name != "agent-001" || p.NextCursor != "" {
		t.Fatal("the second page does not have the last run")
	}
	f.call(t, http.MethodHead, runsPath, f.token, http.StatusOK)
	g := decode[periodPage](t, f.call(t, http.MethodGet, testPeriod+"team", f.token, http.StatusOK))
	if len(g.Items) != 2 || g.GroupBy != "label:team" || spendOf(g.Items[1].amounts) != 1 || g.NextCursor != "" {
		t.Fatal("the period does not match the spend")
	}
	f.call(t, http.MethodHead, testPeriod+costGroupLabel, f.token, http.StatusOK)
}

func TestMethodsAndQueries(t *testing.T) {
	f := newFixture(t, 0)
	w := f.call(t, http.MethodGet, specPath, f.token, http.StatusOK)
	var spec struct {
		OpenAPI string                                `json:"openapi"`
		Paths   map[string]map[string]json.RawMessage `json:"paths"`
	}
	if w.Body.String() != docs.SpendOpenAPI || json.Unmarshal(w.Body.Bytes(), &spec) != nil ||
		!strings.HasPrefix(spec.OpenAPI, "3.") {
		t.Fatal("want the published OpenAPI 3 document")
	}
	f.call(t, http.MethodHead, specPath, f.token, http.StatusOK)
	for _, path := range []string{runsPath, spendPath, specPath, "/v2/runs"} {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
			w := f.call(t, method, path, "", http.StatusMethodNotAllowed)
			if w.Header().Get("Allow") != "GET, HEAD" {
				t.Fatal("the Allow header does not list the read methods")
			}
			if _, ok := spec.Paths[path][strings.ToLower(method)]; !ok && path != "/v2/runs" {
				t.Fatalf("the OpenAPI document does not list the 405 response of %s %s", method, path)
			}
		}
		f.call(t, http.MethodGet, path, "", http.StatusUnauthorized)
		f.call(t, http.MethodHead, path, rand.Text(), http.StatusUnauthorized)
	}
	f.call(t, http.MethodGet, "/v2/runs", f.token, http.StatusNotFound)
	for _, path := range []string{
		runsPath + "?limit=0", runsPath + "?limit=101", runsPath + "?limit=1&limit=2",
		runsPath + "?cursor=bad!", spendPath, testPeriod + "a/b/c",
		spendPath + "?from=2026-02-01&to=2026-01-01&groupBy=label:team",
	} {
		f.call(t, http.MethodGet, path, f.token, http.StatusBadRequest)
	}
}

type failingReader struct {
	client.Reader
	err error
}

func (r failingReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return r.err
}

func TestKubernetesErrors(t *testing.T) {
	f := newFixture(t, 0)
	for status, err := range map[int]error{
		http.StatusBadRequest: apierrors.NewBadRequest("the continue token is not valid"),
		http.StatusGone:       apierrors.NewResourceExpired("the continue token expired"),
		http.StatusBadGateway: errors.New("the API server is not available"),
	} {
		f.kube = failingReader{err: err}
		f.call(t, http.MethodGet, runsPath, f.token, status)
		f.call(t, http.MethodGet, testPeriod+"team", f.token, status)
	}
}

func TestNewRefusesBadConfig(t *testing.T) {
	good := Config{
		Kube: agentReader{}, Namespaces: []string{testNamespace}, GatewayURL: "http://litellm.example",
		MasterKey: "master", Timeout: time.Second, StaticTokens: []string{"token"},
	}
	if _, err := New(good); err != nil {
		t.Fatal(err)
	}
	for i, change := range []func(*Config){
		func(c *Config) { c.Kube = nil },
		func(c *Config) { c.Namespaces = []string{"Not_A_Label"} },
		func(c *Config) { c.Namespaces = nil },
		func(c *Config) { c.Namespaces = []string{testNamespace, testNamespace} },
		func(c *Config) { c.Namespaces = make([]string, maxNamespaces+1) },
		func(c *Config) { c.Timeout = 0 },
		func(c *Config) { c.MasterKey = "" },
		func(c *Config) { c.StaticTokens = nil },
		func(c *Config) { c.Keys = &appclaim.KeySet{} },
		func(c *Config) { c.StaticTokens, c.Keys = nil, &appclaim.KeySet{} },
		func(c *Config) { c.StaticTokens = []string{""} },
		func(c *Config) { c.StaticTokens = []string{"two words"} },
	} {
		c := good
		change(&c)
		if _, err := New(c); err == nil {
			t.Fatalf("New accepted bad configuration %d", i)
		}
	}
}

func TestSignedClaim(t *testing.T) {
	f := newFixture(t, 0)
	f.claims = appclaim.Config{Issuer: "https://issuer.example", Audience: "spend", CallerClaim: "sub"}
	f.tokens = nil
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	point, err := key.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	jwks := fmt.Appendf(nil, `{"keys":[{"kty":"EC","crv":"P-256","x":%q,"y":%q}]}`, enc(point[1:33]), enc(point[33:]))
	f.keys, err = appclaim.ParseKeySet(jwks)
	if err != nil {
		t.Fatal(err)
	}
	sign := func(issuer string) string {
		t.Helper()
		payload := fmt.Appendf(nil, `{"iss":%q,"aud":%q,"sub":"reader","exp":%d}`,
			issuer, f.claims.Audience, time.Now().Add(time.Minute).Unix())
		unsigned := enc([]byte(`{"alg":"ES256"}`)) + "." + enc(payload)
		digest := sha256.Sum256([]byte(unsigned))
		r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		var sig [64]byte
		r.FillBytes(sig[:32])
		s.FillBytes(sig[32:])
		return unsigned + "." + enc(sig[:])
	}
	f.call(t, http.MethodGet, runsPath, sign(f.claims.Issuer), http.StatusOK)
	f.call(t, http.MethodGet, runsPath, sign("https://other.example"), http.StatusUnauthorized)
	f.call(t, http.MethodGet, runsPath, f.token, http.StatusUnauthorized)
}

func TestGatewayFailures(t *testing.T) {
	f := newFixture(t, 1)
	useGateway(t, f.gateway, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, f.runKeys[0].value, f.runKeys[0].hash, f.master)
	})
	f.call(t, http.MethodGet, runsPath, f.token, http.StatusBadGateway)
	f.call(t, http.MethodGet, testPeriod+"team", f.token, http.StatusBadGateway)

	f.timeout = 20 * time.Millisecond
	useGateway(t, f.gateway, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	f.call(t, http.MethodGet, runsPath, f.token, http.StatusGatewayTimeout)
}

func BenchmarkPage100(b *testing.B) {
	f := newFixture(b, 101)
	f.serveGateway(b)
	for name, path := range map[string]string{"runs": runsPath, "spend": testPeriod + "team"} {
		b.Run(name, func(b *testing.B) {
			w := f.call(b, http.MethodGet, path, f.token, http.StatusOK)
			p := decode[page[json.RawMessage]](b, w)
			if len(p.Items) != maxPage || p.NextCursor == "" {
				b.Fatal("want a full page and a cursor")
			}
			b.ReportAllocs()
			for b.Loop() {
				f.call(b, http.MethodGet, path, f.token, http.StatusOK)
			}
		})
	}
}
