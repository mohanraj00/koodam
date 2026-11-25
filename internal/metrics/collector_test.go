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

package metrics

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const (
	modelA    = "model-a"
	modelB    = "model-b"
	nsA       = "alpha"
	nsB       = "beta"
	nsLabel   = "namespace"
	errorText = `# HELP koodam_metrics_scrape_errors_total Number of failed Agent lists during a metrics scrape.
# TYPE koodam_metrics_scrape_errors_total counter
koodam_metrics_scrape_errors_total %d
`
)

func TestCollector(t *testing.T) {
	start := metav1.NewTime(time.Unix(100, 0))
	end := metav1.NewTime(start.Add(4 * time.Second))
	later := metav1.NewTime(start.Add(8 * time.Second))
	a := &corev1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Namespace: nsA, Name: "first"},
		Spec: corev1alpha1.AgentSpec{
			Models: []string{modelA, modelB}, Budget: resource.MustParse("1250m"), User: "fixture-user",
		},
		Status: corev1alpha1.AgentStatus{
			Phase: corev1alpha1.AgentSucceeded, StartedAt: &start, CompletedAt: &end,
			Usage: &corev1alpha1.AgentUsage{Spend: "0.25", Models: []corev1alpha1.ModelUsage{
				{Model: modelB, Spend: "0.25"},
			}},
		},
	}
	b := &corev1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Namespace: nsA, Name: "second"},
		Spec:       corev1alpha1.AgentSpec{Model: modelA, Budget: resource.MustParse("750m")},
		Status: corev1alpha1.AgentStatus{
			Phase: corev1alpha1.AgentSucceeded, StartedAt: &start, CompletedAt: &later,
			Usage: &corev1alpha1.AgentUsage{Spend: "0.5"},
		},
	}
	c := &corev1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Namespace: nsB, Name: "third"},
		Spec:       corev1alpha1.AgentSpec{Model: modelA, Budget: resource.MustParse("3")},
		Status: corev1alpha1.AgentStatus{
			Phase: corev1alpha1.AgentFailed, StartedAt: &start, CompletedAt: &end,
			Usage: &corev1alpha1.AgentUsage{Spend: "0.125"},
		},
	}
	d := &corev1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Namespace: nsB, Name: "fourth"},
		Spec:       corev1alpha1.AgentSpec{Model: modelA, Budget: resource.MustParse("1")},
		Status:     corev1alpha1.AgentStatus{Phase: corev1alpha1.AgentRunning, StartedAt: &start},
	}
	e := &corev1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Namespace: nsB, Name: "fifth"},
		Spec:       corev1alpha1.AgentSpec{Model: modelB, Budget: resource.MustParse("0")},
		Status: corev1alpha1.AgentStatus{
			Phase: corev1alpha1.AgentFailed, StartedAt: &end, CompletedAt: &start,
			Usage: &corev1alpha1.AgentUsage{Spend: "NaN"},
		},
	}

	reader := fakeBuilder(t, a, b, c, d, e).Build()
	registry := testRegistry(reader)
	const want = `# HELP koodam_agent_runs Number of Agents by namespace and phase.
# TYPE koodam_agent_runs gauge
koodam_agent_runs{namespace="alpha",phase="Succeeded"} 2
koodam_agent_runs{namespace="beta",phase="Failed"} 2
koodam_agent_runs{namespace="beta",phase="Running"} 1
# HELP koodam_agent_run_duration_seconds Duration of finished Agent runs with recorded start and end times.
# TYPE koodam_agent_run_duration_seconds summary
koodam_agent_run_duration_seconds_sum{namespace="alpha",phase="Succeeded"} 12
koodam_agent_run_duration_seconds_count{namespace="alpha",phase="Succeeded"} 2
koodam_agent_run_duration_seconds_sum{namespace="beta",phase="Failed"} 4
koodam_agent_run_duration_seconds_count{namespace="beta",phase="Failed"} 1
# HELP koodam_agent_spend_usd Total recorded Agent spend in USD by namespace and model.
# TYPE koodam_agent_spend_usd gauge
koodam_agent_spend_usd{model="model-a",namespace="alpha"} 0.75
koodam_agent_spend_usd{model="model-a",namespace="beta"} 0.125
# HELP koodam_agent_budget_usd Total declared Agent budget in USD by namespace and model.
# TYPE koodam_agent_budget_usd gauge
koodam_agent_budget_usd{model="model-a",namespace="alpha"} 2
koodam_agent_budget_usd{model="model-a",namespace="beta"} 4
koodam_agent_budget_usd{model="model-b",namespace="beta"} 0
`
	compareMetrics(t, registry, want+fmt.Sprintf(errorText, 0))
	checkLabels(t, registry)

	for _, agent := range []*corev1alpha1.Agent{a, b, c, d, e} {
		if err := reader.Delete(t.Context(), agent); err != nil {
			t.Fatal(err)
		}
	}
	compareMetrics(t, registry, fmt.Sprintf(errorText, 0))
}

func TestCollectorListError(t *testing.T) {
	agent := &corev1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: nsA, Name: "first"}}
	reader := fakeBuilder(t, agent).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
				t.Error("Agent list must have a deadline of at most five seconds")
			}
			if err := c.List(ctx, list, opts...); err != nil {
				return err
			}
			return errors.New("list failed after filling the result")
		},
	}).Build()
	registry := testRegistry(reader)
	for _, count := range []int{1, 2} {
		compareMetrics(t, registry, fmt.Sprintf(errorText, count))
	}
}

func fakeBuilder(t *testing.T, objects ...client.Object) *fake.ClientBuilder {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...)
}

func testRegistry(reader client.Reader) *prometheus.Registry {
	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(NewCollector(reader))
	return registry
}

func compareMetrics(t *testing.T, registry *prometheus.Registry, want string) {
	t.Helper()
	if err := testutil.GatherAndCompare(registry, strings.NewReader(want)); err != nil {
		t.Fatal(err)
	}
}

func checkLabels(t *testing.T, registry *prometheus.Registry) {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"koodam_agent_runs":                  {nsLabel, "phase"},
		"koodam_agent_run_duration_seconds":  {nsLabel, "phase"},
		"koodam_agent_spend_usd":             {"model", nsLabel},
		"koodam_agent_budget_usd":            {"model", nsLabel},
		"koodam_metrics_scrape_errors_total": nil,
	}
	for _, family := range families {
		name := family.GetName()
		labels, ok := want[name]
		if !ok {
			t.Errorf("unexpected metric %s", name)
		}
		for _, metric := range family.GetMetric() {
			got := make([]string, 0, len(metric.GetLabel()))
			for _, label := range metric.GetLabel() {
				got = append(got, label.GetName())
			}
			if !slices.Equal(got, labels) {
				t.Errorf("%s labels = %v, want %v", name, got, labels)
			}
		}
	}
}
