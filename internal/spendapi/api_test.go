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
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/docs"
	"github.com/mohanraj00/koodam/internal/appclaim"
	"github.com/mohanraj00/koodam/internal/gateway"
)

const (
	testNamespace = "sample"
	testFrom      = "2026-01-01"
	testTo        = "2026-01-31"
	testPeriod    = spendPath + "?from=" + testFrom + "&to=" + testTo + "&groupBy=label:"
)

type agentReader struct {
	client.Reader
	items []corev1alpha1.Agent
}

func (l agentReader) List(_ context.Context, target client.ObjectList, options ...client.ListOption) error {
	list := target.(*corev1alpha1.AgentList)
	opts := (&client.ListOptions{}).ApplyOptions(options)
	if opts.Namespace != testNamespace || opts.Limit < 1 || opts.Limit > maxPage {
		return fmt.Errorf("incorrect Kubernetes list options")
	}
	start := 0
	if opts.Continue != "" {
		var err error
		start, err = strconv.Atoi(opts.Continue)
		if err != nil || start < 0 || start > len(l.items) {
			return errCursor
		}
	}
	end := min(start+int(opts.Limit), len(l.items))
	list.Items = l.items[start:end]
	if end < len(l.items) {
		list.Continue = strconv.Itoa(end)
	}
	return nil
}

type fakeKey struct {
	value, hash string
}

type fixture struct {
	*api
	runKeys       []fakeKey
	keyIndex      map[string]int
	master, token string
}

func newFixture(t testing.TB, count int) fixture {
	t.Helper()
	f := fixture{
		master: rand.Text(), token: rand.Text(),
		runKeys: make([]fakeKey, max(1, count)), keyIndex: make(map[string]int, count),
	}
	items := make([]corev1alpha1.Agent, count)
	for i := range f.runKeys {
		secret, err := gateway.NewKeyValue()
		if err != nil {
			t.Fatal(err)
		}
		hash := gateway.HashKey(secret)
		f.runKeys[i] = fakeKey{value: secret, hash: hash}
		f.keyIndex[hash] = i
		if i >= count {
			break
		}
		name := fmt.Sprintf("agent-%03d", i)
		items[i] = corev1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: testNamespace, UID: types.UID(name),
				CreationTimestamp: metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
				Labels:            map[string]string{"team": name, costGroupLabel: "edited"},
			},
			Spec: corev1alpha1.AgentSpec{Budget: resource.MustParse("2")},
			Status: corev1alpha1.AgentStatus{
				KeyHash: hash, KeyAlias: "run-alias", CostGroup: "saved",
			},
		}
	}
	handler, err := New(Config{
		Kube: agentReader{items: items}, Namespace: testNamespace,
		GatewayURL: "http://litellm.example", MasterKey: f.master, Timeout: 2 * time.Second,
		StaticTokens: []string{f.token},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.api = handler.(*api)
	return f
}

// inMemoryGateway serves the same responses when the test environment cannot bind a socket.
type inMemoryGateway struct{ http.Handler }

func (h inMemoryGateway) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Result(), nil
}

func (f fixture) useGateway(t testing.TB, handler http.HandlerFunc) {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Log("Use an in-process HTTP transport because a loopback listener did not start.")
		f.gateway.HTTP.Transport = inMemoryGateway{handler}
		return
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	server.Start()
	t.Cleanup(server.Close)
	f.gateway.BaseURL = server.URL
}

func (f fixture) serveGateway(t testing.TB) {
	t.Helper()
	f.useGateway(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+f.master {
			t.Error("the gateway did not receive an authenticated GET")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/key/info":
			hash := q.Get("key")
			i, ok := f.keyIndex[hash]
			if !ok {
				t.Error("the gateway did not receive the key hash")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = fmt.Fprintf(w, `{"key":%q, "info":{"spend":%g,"token":%q}}`,
				f.runKeys[i].value, 0.75+float64(i)/4, hash)
		case "/spend/logs":
			hash := q.Get("api_key")
			i, ok := f.keyIndex[hash]
			if !ok || q.Get("start_date") != testFrom ||
				q.Get("end_date") != "2026-02-01" || q.Get("summarize") != "true" {
				t.Error("the gateway did not receive the expected spend query")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = fmt.Fprintf(w, `[
				{"startTime":%q, "spend":%g, %q:%g},
				{"startTime":%q, "spend":0.5, %q:0.5},
				{"startTime":"2026-02-01", "spend":5, %q:5}
			]`, testFrom, 0.25+float64(i)/4, hash, 0.25+float64(i)/4, testTo, hash, hash)
		default:
			t.Error("the gateway received an unexpected path")
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

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

func TestRunSpend(t *testing.T) {
	f := newFixture(t, 2)
	f.serveGateway(t)
	first := f.call(t, http.MethodGet, runsPath+"?limit=1", f.token, http.StatusOK)
	p := decode[page[run]](t, first)
	if len(p.Items) != 1 || p.NextCursor == "" {
		t.Fatal("want one run and a cursor")
	}
	a := p.Items[0]
	if a.ID != "agent-000" || a.Spend == nil || *a.Spend != 0.75 || a.Budget == nil || *a.Budget != 2 ||
		a.RemainingBudget == nil || *a.RemainingBudget != 1.25 {
		t.Fatal("the run does not match the spend and budget")
	}
	next := runsPath + "?limit=1&cursor=" + p.NextCursor
	p = decode[page[run]](t, f.call(t, http.MethodGet, next, f.token, http.StatusOK))
	if len(p.Items) != 1 || p.Items[0].Name != "agent-001" || spendOf(p.Items[0].amounts) != 1 || p.NextCursor != "" {
		t.Fatal("the second page does not have the last run")
	}
	f.call(t, http.MethodHead, runsPath, f.token, http.StatusOK)
}

func TestPeriodSpend(t *testing.T) {
	f := newFixture(t, 4)
	items := f.kube.(agentReader).items
	items[1].Labels["team"] = items[0].Labels["team"]
	items[2].Labels["team"] = "red"
	items[3].CreationTimestamp = metav1.NewTime(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	f.serveGateway(t)
	path := testPeriod + "team&limit=1"
	first := f.call(t, http.MethodGet, path, f.token, http.StatusOK)
	p := decode[periodPage](t, first)
	if p.From != testFrom || p.To != testTo || p.GroupBy != "label:team" || len(p.Items) != 1 ||
		p.Items[0].LabelValue != "agent-000" || spendOf(p.Items[0].amounts) != 1.75 || p.NextCursor == "" {
		t.Fatal("the first period page does not match the spend")
	}
	if bytes.Contains(first.Body.Bytes(), []byte("budget")) ||
		bytes.Contains(first.Body.Bytes(), []byte("remainingBudget")) {
		t.Fatal("the response has undeclared period budgets")
	}
	next := path + "&cursor=" + p.NextCursor
	p = decode[periodPage](t, f.call(t, http.MethodGet, next, f.token, http.StatusOK))
	if len(p.Items) != 1 || p.Items[0].LabelValue != "red" || spendOf(p.Items[0].amounts) != 1.25 || p.NextCursor != "" {
		t.Fatal("the second period page does not match the spend")
	}
	saved := testPeriod + costGroupLabel
	w := f.call(t, http.MethodGet, saved, f.token, http.StatusOK)
	p = decode[periodPage](t, w)
	if len(p.Items) != 1 || p.Items[0].LabelValue != "saved" || spendOf(p.Items[0].amounts) != 3 {
		t.Fatal("the period does not use the saved cost group")
	}
	f.call(t, http.MethodHead, saved, f.token, http.StatusOK)
	f.call(t, http.MethodGet, strings.Replace(next, "team", "project", 1), f.token, http.StatusBadRequest)
}

func spendOf(a amounts) float64 {
	if a.Spend == nil {
		return -1
	}
	return *a.Spend
}

func TestRunSpendWhenKeyIsMissing(t *testing.T) {
	f := newFixture(t, 3)
	items := f.kube.(agentReader).items
	items[0].Status.Usage = &corev1alpha1.AgentUsage{Spend: "3"}
	items[2].Status.KeyHash = ""
	f.useGateway(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"error":{"type":"not_found_error","param":"key","message":%q}}`, f.runKeys[0].hash)
	})
	p := decode[page[run]](t, f.call(t, http.MethodGet, runsPath, f.token, http.StatusOK))
	if len(p.Items) != 3 || spendOf(p.Items[0].amounts) != 3 ||
		p.Items[0].RemainingBudget == nil || *p.Items[0].RemainingBudget != 0 ||
		p.Items[1].Spend != nil || p.Items[1].RemainingBudget != nil || spendOf(p.Items[2].amounts) != 0 {
		t.Fatal("the response does not distinguish saved, unknown and zero spend")
	}
}

func TestMethodsAndQueries(t *testing.T) {
	f := newFixture(t, 0)
	w := f.call(t, http.MethodGet, specPath, f.token, http.StatusOK)
	var spec struct {
		OpenAPI string `json:"openapi"`
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
	f.useGateway(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, f.runKeys[0].value, f.runKeys[0].hash, f.master)
	})
	f.call(t, http.MethodGet, runsPath, f.token, http.StatusBadGateway)
	f.call(t, http.MethodGet, testPeriod+"team", f.token, http.StatusBadGateway)

	f.timeout = 20 * time.Millisecond
	f.useGateway(t, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
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
