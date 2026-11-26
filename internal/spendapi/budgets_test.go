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
	"errors"
	"net/http"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

var budgetNow = time.Date(2026, time.November, 25, 10, 0, 0, 0, time.UTC)

func testBudget(start time.Time) corev1alpha1.SpendBudget {
	return corev1alpha1.SpendBudget{
		ObjectMeta: metav1.ObjectMeta{Name: corev1alpha1.SpendBudgetName, Namespace: testNamespace},
		Spec:       corev1alpha1.SpendBudgetSpec{PeriodLimits: corev1alpha1.PeriodLimits{Spend: resource.MustParse("10")}},
		Status: corev1alpha1.SpendBudgetStatus{
			PeriodStart: &metav1.Time{Time: start}, PeriodEnd: &metav1.Time{Time: start.AddDate(0, 0, 1)},
			Spend: "4", Reserved: "2.5", WaitingRuns: 1,
			Reservations: []corev1alpha1.SpendReservation{{RunID: "uid-1", Agent: "agent-000", Budget: "2.5"}},
			Conditions: []metav1.Condition{{Type: corev1alpha1.SpendBudgetExceeded, Status: metav1.ConditionTrue,
				Reason: corev1alpha1.ReasonPeriodBudgetExceeded, LastTransitionTime: metav1.Now()}},
		},
	}
}

func TestServeBudgets(t *testing.T) {
	f := newFixture(t, 1)
	f.now = func() time.Time { return budgetNow }
	reader := f.kube.(agentReader)

	if got := decode[page[budget]](t, f.call(t, http.MethodGet, budgetsPath, f.token, http.StatusOK)); got.Items == nil ||
		len(got.Items) != 0 {
		t.Fatalf("a namespace with no budget must give an empty list, got %+v", got)
	}

	reader.budgets = []corev1alpha1.SpendBudget{testBudget(time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC))}
	f.kube = reader
	got := decode[page[budget]](t, f.call(t, http.MethodGet, budgetsPath, f.token, http.StatusOK))
	want := budget{Namespace: testNamespace, Period: "Day", PeriodStart: time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC),
		PeriodEnd: time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC), Limit: 10, Spend: 4, Reserved: 2.5, Remaining: 3.5,
		Exceeded: true, WaitingRuns: 1}
	if len(got.Items) != 1 || got.Items[0] != want || got.NextCursor != "" {
		t.Fatalf("got %+v, want %+v", got.Items, want)
	}
	f.call(t, http.MethodHead, budgetsPath, f.token, http.StatusOK)
	f.call(t, http.MethodGet, budgetsPath, "", http.StatusUnauthorized)

	reader.getErr = errors.New("the API server is not available")
	f.kube = reader
	f.call(t, http.MethodGet, budgetsPath, f.token, http.StatusBadGateway)
}

// A ledger of an old period shows the current period with no spend, because the controller drops
// the old reservations at the next check.
func TestBudgetOfAnOldPeriod(t *testing.T) {
	b := testBudget(time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC))
	got := budgetItem(&b, budgetNow)
	if !got.PeriodStart.Equal(time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)) || got.Spend != 0 || got.Reserved != 0 ||
		got.Remaining != 10 || got.Exceeded || got.WaitingRuns != 0 {
		t.Fatalf("got %+v", got)
	}
	b.Spec.Period = corev1alpha1.SpendPeriodMonth
	b.Status.PeriodStart = &metav1.Time{Time: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
	b.Status.Spend, b.Status.Reserved = "9", "3"
	got = budgetItem(&b, budgetNow)
	if got.Period != "Month" || !got.PeriodEnd.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) || got.Remaining != 0 {
		t.Fatalf("got %+v", got)
	}
}

// After a change of spec.period, a status period that overlaps the current period keeps its spend,
// as the controller does. The item must not show the full limit as remaining.
func TestBudgetAfterAPeriodChange(t *testing.T) {
	b := testBudget(time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC))
	b.Spec.Period = corev1alpha1.SpendPeriodMonth
	got := budgetItem(&b, budgetNow)
	if !got.PeriodStart.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) || got.Spend != 4 ||
		got.Reserved != 2.5 || got.Remaining != 3.5 || !got.Exceeded || got.WaitingRuns != 1 {
		t.Fatalf("a day ledger in the current month must keep its spend, got %+v", got)
	}

	b.Spec.Period = corev1alpha1.SpendPeriodDay
	b.Status.PeriodStart = &metav1.Time{Time: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
	b.Status.PeriodEnd = &metav1.Time{Time: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)}
	got = budgetItem(&b, budgetNow)
	if !got.PeriodStart.Equal(time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)) || got.Spend != 4 || got.Remaining != 3.5 {
		t.Fatalf("a month ledger that has today must keep its spend, got %+v", got)
	}
}
