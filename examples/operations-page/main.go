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

// Command operations-page runs the spend API and the operations page on one machine, with no
// cluster and no gateway. A fake gateway gives synthetic spend. A fake Kubernetes API gives
// synthetic Agents and checks the viewer tokens of the example.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
	"github.com/mohanraj00/koodam/internal/gateway"
	"github.com/mohanraj00/koodam/internal/opspage"
	"github.com/mohanraj00/koodam/internal/spendapi"
)

// The viewer tokens of the example. Only the fake Kubernetes API of this example accepts them.
const (
	adminToken = "example-admin"
	teamAToken = "example-team-a"
	masterKey  = "example-master"
	costGroup  = "koodam.dev/cost-group"
	namespaceA = "team-a"
	namespaceB = "team-b"
)

var namespaces = []string{namespaceA, namespaceB}

// agentReader lists the synthetic Agents of one namespace, a page at a time.
type agentReader struct {
	client.Reader
	items   []corev1beta1.Agent
	budgets []corev1beta1.SpendBudget
}

// Get reads a SpendBudget by its name. The spend API reads no other object with Get.
func (r agentReader) Get(_ context.Context, key client.ObjectKey, target client.Object, _ ...client.GetOption) error {
	b, ok := target.(*corev1beta1.SpendBudget)
	if !ok {
		return errors.New("the example reader gets only SpendBudgets")
	}
	for _, item := range r.budgets {
		if item.Namespace == key.Namespace && item.Name == key.Name {
			item.DeepCopyInto(b)
			return nil
		}
	}
	return apierrors.NewNotFound(schema.GroupResource{Group: "core.koodam.dev", Resource: "spendbudgets"}, key.Name)
}

func (r agentReader) List(_ context.Context, target client.ObjectList, options ...client.ListOption) error {
	list, ok := target.(*corev1beta1.AgentList)
	opts := (&client.ListOptions{}).ApplyOptions(options)
	if !ok || opts.Namespace == "" || opts.Limit < 1 {
		return errors.New("the example reader lists Agents in one namespace with a limit")
	}
	var items []corev1beta1.Agent
	for _, item := range r.items {
		if item.Namespace == opts.Namespace {
			items = append(items, item)
		}
	}
	start, err := strconv.Atoi(opts.Continue)
	if opts.Continue == "" {
		start, err = 0, nil
	}
	if err != nil || start < 0 || start > len(items) {
		return errors.New("the continue value is not valid")
	}
	end := min(start+int(opts.Limit), len(items))
	list.Items = items[start:end]
	if end < len(items) {
		list.Continue = strconv.Itoa(end)
	}
	return nil
}

type run struct {
	namespace, name, group, phase string
	budget, spend                 float64
}

// example has the handler of the example and the synthetic key values. The key values are
// only for the check that no response holds a key.
type example struct {
	handler http.Handler
	keys    []string
	close   func()
}

func newExample(now time.Time) (*example, error) {
	runs := []run{
		{namespaceA, "summarize-docs", "research", "Succeeded", 5, 1.25},
		{namespaceA, "triage-issues", "support", "Running", 2, 0.5},
		{namespaceB, "nightly-report", "research", "Failed", 1, 0.75},
	}
	spend, values := map[string]float64{}, map[string]string{}
	ex := &example{}
	agents := make([]corev1beta1.Agent, 0, len(runs))
	for i, r := range runs {
		value, err := gateway.NewKeyValue()
		if err != nil {
			return nil, err
		}
		hash := gateway.HashKey(value)
		ex.keys = append(ex.keys, value, hash)
		spend[hash], values[hash] = r.spend, value
		started := metav1.NewTime(now.Add(-time.Duration(len(runs)-i) * time.Hour))
		status := corev1beta1.AgentStatus{
			Phase: corev1beta1.AgentPhase(r.phase), StartedAt: &started, KeyHash: hash, CostGroup: r.group,
		}
		if r.phase != "Running" {
			ended := metav1.NewTime(started.Add(20 * time.Minute))
			status.CompletedAt = &ended
		}
		agents = append(agents, corev1beta1.Agent{
			ObjectMeta: metav1.ObjectMeta{
				Name: r.name, Namespace: r.namespace, UID: types.UID(fmt.Sprintf("run-%d", i)),
				CreationTimestamp: started, Labels: map[string]string{costGroup: r.group},
			},
			Spec: corev1beta1.AgentSpec{
				Model: "example-model", Budget: resource.MustParse(strconv.FormatFloat(r.budget, 'f', -1, 64)),
			},
			Status: status,
		})
	}
	gw := httptest.NewServer(fakeGateway(spend, values))
	api, err := spendapi.New(spendapi.Config{
		Kube:       agentReader{items: agents, budgets: []corev1beta1.SpendBudget{exampleBudget(now)}},
		Namespaces: namespaces, GatewayURL: gw.URL, MasterKey: masterKey,
		Timeout: 5 * time.Second, Viewers: fakeViewers(),
	})
	if err != nil {
		gw.Close()
		return nil, err
	}
	ex.handler, ex.close = opspage.Routes(api), gw.Close
	return ex, nil
}

// exampleBudget is the SpendBudget of the first namespace. The ended run has a spend of 1.25, and the
// running run reserves its budget of 2.
func exampleBudget(now time.Time) corev1beta1.SpendBudget {
	start := now.UTC().Truncate(24 * time.Hour)
	return corev1beta1.SpendBudget{
		ObjectMeta: metav1.ObjectMeta{Name: corev1beta1.SpendBudgetName, Namespace: namespaceA},
		Spec:       corev1beta1.SpendBudgetSpec{PeriodLimits: corev1beta1.PeriodLimits{Spend: resource.MustParse("10")}},
		Status: corev1beta1.SpendBudgetStatus{
			PeriodStart: &metav1.Time{Time: start}, PeriodEnd: &metav1.Time{Time: start.AddDate(0, 0, 1)},
			Spend: "1.25", Reserved: "2",
			Reservations: []corev1beta1.SpendReservation{{RunID: "run-1", Agent: "triage-issues", Budget: "2"}},
		},
	}
}

// fakeGateway answers the two gateway reads of the spend API. Like the real gateway, its key
// read gives the key value, so the example can check that the spend API drops it.
func fakeGateway(spend map[string]float64, values map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Header.Get("Authorization") != "Bearer "+masterKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body any
		switch r.URL.Path {
		case "/key/info":
			s, ok := spend[q.Get("key")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			body = map[string]any{"key": values[q.Get("key")], "info": map[string]any{"spend": s, "token": q.Get("key")}}
		case "/spend/logs":
			s, ok := spend[q.Get("api_key")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			body = []map[string]any{{"startTime": q.Get("start_date"), "spend": s, q.Get("api_key"): s}}
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})
}

// fakeViewers accepts the two example tokens. The admin can list Agents in each namespace. The
// team-a viewer can list Agents only in team-a.
func fakeViewers() *spendapi.Viewers {
	users := map[string]string{adminToken: "admin", teamAToken: "team-a-viewer"}
	clients := fake.NewClientset()
	clients.PrependReactor("create", "tokenreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		review := a.(k8stesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
		name, ok := users[review.Spec.Token]
		review.Spec.Token = ""
		review.Status = authenticationv1.TokenReviewStatus{Authenticated: ok, User: authenticationv1.UserInfo{Username: name}}
		return true, review, nil
	})
	clients.PrependReactor("create", "subjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		review := a.(k8stesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		user, ns := review.Spec.User, review.Spec.ResourceAttributes.Namespace
		review.Status.Allowed = user == "admin" || (user == "team-a-viewer" && ns == namespaceA)
		return true, review, nil
	})
	return &spendapi.Viewers{
		Tokens: clients.AuthenticationV1().TokenReviews(), Access: clients.AuthorizationV1().SubjectAccessReviews(),
	}
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "the address of the page and the API")
	flag.Parse()
	ex, err := newExample(time.Now().UTC().Truncate(time.Second))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer ex.close()
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Open http://%s/ and give one of the example tokens in the README.\n", listener.Addr())
	srv := &http.Server{Handler: ex.handler, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.Serve(listener); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
