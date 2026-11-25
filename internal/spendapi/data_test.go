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
	"strconv"
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

const testNamespace = "sample"

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
	runKeys  []fakeKey
	keyIndex map[string]int
	master   string
}

func newFixture(t testing.TB, count int) fixture {
	t.Helper()
	f := fixture{
		master:  rand.Text(),
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
				Labels:            map[string]string{"team": name},
			},
			Spec:   corev1alpha1.AgentSpec{Budget: resource.MustParse("2")},
			Status: corev1alpha1.AgentStatus{KeyHash: hash, KeyAlias: "run-alias"},
		}
	}
	l, err := newLiteLLM("http://litellm.example", f.master)
	if err != nil {
		t.Fatal(err)
	}
	f.api = &api{kube: agentReader{items: items}, namespace: testNamespace, gateway: l}
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
	p, err := f.runs(t.Context(), 1, "")
	if err != nil || len(p.Items) != 1 || p.NextCursor == "" {
		t.Fatal("want one run and a cursor")
	}
	a := p.Items[0]
	if a.ID != "agent-000" || a.Name != "agent-000" || a.Namespace != testNamespace || a.CreatedAt.Month() != 1 ||
		spendOf(a.amounts) != 0.75 || *a.Budget != 2 || a.RemainingBudget == nil || *a.RemainingBudget != 1.25 {
		t.Fatal("the run does not match the Agent, the spend and the budget")
	}
	p, err = f.runs(t.Context(), 1, p.NextCursor)
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
	p, err := f.runs(t.Context(), maxPage, "")
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
		if _, err := f.runs(t.Context(), 1, cursor); !errors.Is(err, errCursor) {
			t.Fatalf("runs accepted the cursor %q", cursor)
		}
	}
	f.kube.(agentReader).items[0].Status.KeyHash = "not-a-hash"
	if _, err := f.runs(t.Context(), 1, ""); !errors.Is(err, errGateway) {
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
