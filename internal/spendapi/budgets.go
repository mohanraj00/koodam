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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const budgetsPath = "/v1/budgets"

// budget is the ledger of the SpendBudget of one namespace. Amounts are in USD.
type budget struct {
	Namespace   string    `json:"namespace"`
	Period      string    `json:"period"`
	PeriodStart time.Time `json:"periodStart"`
	PeriodEnd   time.Time `json:"periodEnd"`
	Limit       float64   `json:"limit"`
	Spend       float64   `json:"spend"`
	Reserved    float64   `json:"reserved"`
	Remaining   float64   `json:"remaining"`
	Exceeded    bool      `json:"exceeded"`
	WaitingRuns int32     `json:"waitingRuns"`
}

// currentPeriod gives the UTC day or the UTC month that has t. It uses the same rule as the Agent
// controller.
func currentPeriod(p corev1alpha1.SpendPeriod, t time.Time) (time.Time, time.Time) {
	t = t.UTC()
	if p == corev1alpha1.SpendPeriodMonth {
		start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 1, 0)
	}
	start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 0, 1)
}

func amount(s string) float64 {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0
	}
	return q.AsApproximateFloat64()
}

// budgetItem shows the ledger for the period that has now. If the controller did not move the
// ledger to this period yet, the period has no spend and no reservation. The one exception is a
// status period that overlaps the current period, after a change of spec.period. The controller
// then keeps the old spend and rebuilds the reservations, so the item shows the status values.
func budgetItem(b *corev1alpha1.SpendBudget, now time.Time) budget {
	period := b.Spec.Period
	if period == "" {
		period = corev1alpha1.SpendPeriodDay
	}
	start, end := currentPeriod(period, now)
	item := budget{Namespace: b.Namespace, Period: string(period), PeriodStart: start, PeriodEnd: end,
		Limit: b.Spec.Spend.AsApproximateFloat64()}
	s := b.Status
	current := s.PeriodStart != nil && s.PeriodStart.Time.Equal(start)
	overlaps := s.PeriodStart != nil && s.PeriodEnd != nil && s.PeriodStart.Time.Before(end) &&
		start.Before(s.PeriodEnd.Time)
	if current || overlaps {
		item.Spend, item.Reserved, item.WaitingRuns = amount(s.Spend), amount(s.Reserved), s.WaitingRuns
		item.Exceeded = meta.IsStatusConditionTrue(s.Conditions, corev1alpha1.SpendBudgetExceeded)
	}
	item.Remaining = max(0, item.Limit-item.Spend-item.Reserved)
	return item
}

// budgets reads the SpendBudget of each namespace, in the order of the namespaces. A namespace with
// no SpendBudget has no item.
func (a *api) budgets(ctx context.Context, namespaces []string) (page[budget], error) {
	found := make([]*budget, len(namespaces))
	now := time.Now()
	if a.now != nil {
		now = a.now()
	}
	err := parallel(ctx, len(namespaces), func(ctx context.Context, i int) error {
		b := &corev1alpha1.SpendBudget{}
		key := types.NamespacedName{Namespace: namespaces[i], Name: corev1alpha1.SpendBudgetName}
		err := a.kube.Get(ctx, key, b)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		item := budgetItem(b, now)
		found[i] = &item
		return nil
	})
	result := page[budget]{Items: []budget{}}
	if err != nil {
		return result, err
	}
	for _, item := range found {
		if item != nil {
			result.Items = append(result.Items, *item)
		}
	}
	return result, nil
}
