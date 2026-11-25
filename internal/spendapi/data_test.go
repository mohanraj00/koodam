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
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/gateway"
)

const (
	testNamespace = "sample"
	testFrom      = "2026-01-01"
	testTo        = "2026-01-31"
	testGateway   = "http://litellm.example"
	testAudience  = "spend"
	firstAgent    = "agent-000"
)

type agentReader struct {
	client.Reader
	items []corev1alpha1.Agent
}

func (l agentReader) List(_ context.Context, target client.ObjectList, options ...client.ListOption) error {
	list := target.(*corev1alpha1.AgentList)
	opts := (&client.ListOptions{}).ApplyOptions(options)
	if opts.Namespace == "" || opts.Limit < 1 || opts.Limit > maxPage {
		return fmt.Errorf("incorrect Kubernetes list options")
	}
	var items []corev1alpha1.Agent
	for _, item := range l.items {
		if item.Namespace == opts.Namespace {
			items = append(items, item)
		}
	}
	start := 0
	if opts.Continue != "" {
		var err error
		start, err = strconv.Atoi(opts.Continue)
		if err != nil || start < 0 || start > len(items) {
			return errCursor
		}
	}
	end := min(start+int(opts.Limit), len(items))
	list.Items = items[start:end]
	if end < len(items) {
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
			Spec:   corev1alpha1.AgentSpec{Budget: resource.MustParse("2")},
			Status: corev1alpha1.AgentStatus{KeyHash: hash, KeyAlias: "run-alias", CostGroup: "saved"},
		}
	}
	handler, err := New(Config{
		Kube: agentReader{items: items}, Namespaces: []string{testNamespace},
		GatewayURL: testGateway, MasterKey: f.master, Timeout: 2 * time.Second,
		StaticTokens: []string{f.token},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.api = handler.(*api)
	return f
}

func (f fixture) serveGateway(t testing.TB) {
	t.Helper()
	useGateway(t, f.gateway, func(w http.ResponseWriter, r *http.Request) {
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

func spendOf(a amounts) float64 {
	if a.Spend == nil {
		return -1
	}
	return *a.Spend
}

func TestRunSpend(t *testing.T) {
	f := newFixture(t, 2)
	f.kube.(agentReader).items[1].Status.RunID = "run-1"
	f.serveGateway(t)
	p, err := f.runs(t.Context(), f.namespaces, 1, "")
	if err != nil || len(p.Items) != 1 || p.NextCursor == "" {
		t.Fatal("want one run and a cursor")
	}
	a := p.Items[0]
	if a.ID != firstAgent || a.Name != firstAgent || a.Namespace != testNamespace || a.CreatedAt.Month() != 1 ||
		spendOf(a.amounts) != 0.75 || *a.Budget != 2 || a.RemainingBudget == nil || *a.RemainingBudget != 1.25 {
		t.Fatal("the run does not match the Agent, the spend and the budget")
	}
	p, err = f.runs(t.Context(), f.namespaces, 1, p.NextCursor)
	if err != nil || len(p.Items) != 1 || p.Items[0].ID != "run-1" || spendOf(p.Items[0].amounts) != 1 ||
		p.NextCursor != "" {
		t.Fatal("the second page does not have the last run")
	}
}

func TestRunSpendWhenKeyIsMissing(t *testing.T) {
	f := newFixture(t, 3)
	items := f.kube.(agentReader).items
	items[0].Status.Usage = &corev1alpha1.AgentUsage{Spend: "3"}
	items[2].Status.KeyHash = ""
	useGateway(t, f.gateway, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"error":{"type":"not_found_error","param":"key","message":%q}}`, f.runKeys[0].hash)
	})
	p, err := f.runs(t.Context(), f.namespaces, maxPage, "")
	if err != nil || len(p.Items) != 3 || spendOf(p.Items[0].amounts) != 3 ||
		p.Items[0].RemainingBudget == nil || *p.Items[0].RemainingBudget != 0 ||
		p.Items[1].Spend != nil || p.Items[1].RemainingBudget != nil || spendOf(p.Items[2].amounts) != 0 {
		t.Fatal("the response does not distinguish saved, unknown and zero spend")
	}
}

func TestRunErrors(t *testing.T) {
	f := newFixture(t, 1)
	f.serveGateway(t)
	for _, cursor := range []string{"bad!", encodeCursor(runsPath+":other", "0"), encodeCursor("/v1/x:sample", "0")} {
		if _, err := f.runs(t.Context(), f.namespaces, 1, cursor); !errors.Is(err, errCursor) {
			t.Fatalf("runs accepted the cursor %q", cursor)
		}
	}
	f.kube.(agentReader).items[0].Status.KeyHash = "not-a-hash"
	if _, err := f.runs(t.Context(), f.namespaces, 1, ""); !errors.Is(err, errGateway) {
		t.Fatal("runs accepted a bad key hash")
	}
}

func TestParallel(t *testing.T) {
	var reads [3 * maxGatewayReads]atomic.Int32
	err := parallel(t.Context(), len(reads), func(_ context.Context, i int) error { reads[i].Add(1); return nil })
	for i := range reads {
		if err != nil || reads[i].Load() != 1 {
			t.Fatal("parallel did not read each item one time")
		}
	}
	failure := errors.New("read failed")
	var count atomic.Int32
	err = parallel(t.Context(), 1000, func(ctx context.Context, i int) error {
		if count.Add(1); i == 0 {
			return failure
		}
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, failure) || count.Load() > maxGatewayReads {
		t.Fatal("parallel did not stop the reads after the first error")
	}
}

func testPeriodOf(t *testing.T, label string) period {
	t.Helper()
	p, ok := parsePeriod(url.Values{"from": {testFrom}, "to": {testTo}, "groupBy": {"label:" + label}})
	if !ok {
		t.Fatal("parsePeriod refused a valid period")
	}
	return p
}

func TestPeriodSpend(t *testing.T) {
	f := newFixture(t, 4)
	items := f.kube.(agentReader).items
	items[1].Labels["team"] = items[0].Labels["team"]
	items[2].Labels["team"] = "red"
	items[3].CreationTimestamp = metav1.NewTime(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	f.serveGateway(t)
	team := testPeriodOf(t, "team")
	p, err := f.spend(t.Context(), f.namespaces, team, 1, "")
	if err != nil || p.From != testFrom || p.To != testTo || p.GroupBy != "label:team" || len(p.Items) != 1 ||
		p.Items[0].LabelValue != items[0].Name || spendOf(p.Items[0].amounts) != 1.75 || p.NextCursor == "" {
		t.Fatal("the first period page does not match the spend")
	}
	if p.Items[0].Budget != nil || p.Items[0].RemainingBudget != nil {
		t.Fatal("the period has undeclared budgets")
	}
	next := p.NextCursor
	p, err = f.spend(t.Context(), f.namespaces, team, 1, next)
	if err != nil || len(p.Items) != 1 || p.Items[0].LabelValue != "red" || spendOf(p.Items[0].amounts) != 1.25 ||
		p.NextCursor != "" {
		t.Fatal("the second period page does not match the spend")
	}
	p, err = f.spend(t.Context(), f.namespaces, testPeriodOf(t, costGroupLabel), maxPage, "")
	if err != nil || len(p.Items) != 1 || p.Items[0].LabelValue != "saved" || spendOf(p.Items[0].amounts) != 3 {
		t.Fatal("the period does not use the saved cost group")
	}
	if _, err := f.spend(t.Context(), f.namespaces, testPeriodOf(t, "project"), 1, next); !errors.Is(err, errCursor) {
		t.Fatal("the period accepted a cursor of another query")
	}
}

func TestPeriodWithNoKey(t *testing.T) {
	f := newFixture(t, 1)
	f.kube.(agentReader).items[0].Status.KeyHash = ""
	p, err := f.spend(t.Context(), f.namespaces, testPeriodOf(t, "team"), maxPage, "")
	if err != nil || len(p.Items) != 1 || spendOf(p.Items[0].amounts) != 0 {
		t.Fatal("a group with no key does not have zero spend")
	}
}

func TestParsePeriod(t *testing.T) {
	for _, query := range []string{
		"", "from=2026-01-01&to=2026-01-31", "from=2026-02-01&to=2026-01-01&groupBy=label:team",
		"from=2026-01-01&to=2026-01-31&groupBy=team", "from=2026-01-01&to=2026-01-31&groupBy=label:a/b/c",
		"from=2026-01-01&to=2026-01-31&groupBy=label:", "from=0000-01-01&to=2026-01-31&groupBy=label:team",
		"from=2026-01-01&to=9999-12-31&groupBy=label:team", "from=2026-1-1&to=2026-01-31&groupBy=label:team",
	} {
		q, err := url.ParseQuery(query)
		if _, ok := parsePeriod(q); err != nil || ok {
			t.Fatalf("parsePeriod accepted %q", query)
		}
	}
}

func TestPeriodErrors(t *testing.T) {
	f := newFixture(t, 1)
	useGateway(t, f.gateway, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, err := f.spend(t.Context(), f.namespaces, testPeriodOf(t, "team"), 1, ""); !errors.Is(err, errGateway) {
		t.Fatal("the period accepted a gateway error")
	}
	f.kube.(agentReader).items[0].Status.KeyHash = "not-a-hash"
	if _, err := f.spend(t.Context(), f.namespaces, testPeriodOf(t, "team"), 1, ""); !errors.Is(err, errGateway) {
		t.Fatal("the period accepted a bad key hash")
	}
}

func TestRunsAndSpendAcrossNamespaces(t *testing.T) {
	f := newFixture(t, 3)
	f.serveGateway(t)
	f.kube.(agentReader).items[2].Namespace = "other"
	f.namespaces = []string{testNamespace, "other"}
	var seen []string
	for cursor, n := "", 0; n == 0 || cursor != ""; n++ {
		p, err := f.runs(t.Context(), f.namespaces, 1, cursor)
		if err != nil || len(p.Items) != 1 || n > 2 {
			t.Fatal("want one run on each of 3 pages", err)
		}
		seen, cursor = append(seen, p.Items[0].Namespace), p.NextCursor
	}
	if strings.Join(seen, ",") != "sample,sample,other" {
		t.Fatalf("the pages give the namespaces %v", seen)
	}
	p, err := f.runs(t.Context(), f.namespaces, 2, "")
	if err != nil || len(p.Items) != 2 || p.NextCursor == "" {
		t.Fatal("a full page at the end of a namespace needs a cursor for the next namespace")
	}
	// A cursor can name only a namespace of the scope.
	if _, err := f.runs(t.Context(), f.namespaces[:1], 1, p.NextCursor); !errors.Is(err, errCursor) {
		t.Fatal("a cursor of a different scope was accepted")
	}
	forged := encodeCursor(runsPath+":"+testNamespace, "other/")
	if _, err := f.runs(t.Context(), f.namespaces[:1], 1, forged); !errors.Is(err, errCursor) {
		t.Fatal("a cursor with a namespace outside the scope was accepted")
	}
	g, err := f.spend(t.Context(), f.namespaces, testPeriodOf(t, "team"), maxPage, "")
	if err != nil || len(g.Items) != 3 || g.Items[2].LabelValue != "agent-002" {
		t.Fatal("the period spend does not include the two namespaces")
	}
}
