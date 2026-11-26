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

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

// The CRD validation of the SpendBudget. These tests do not run a controller. They only send
// objects to the API server. Each test uses a new namespace, because the name is fixed.
var _ = Describe("SpendBudget CRD", func() {
	ctx := context.Background()

	newBudget := func(change func(*corev1alpha1.SpendBudget)) *corev1alpha1.SpendBudget {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "budget-crd-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, ns))).To(Succeed()) })
		b := &corev1alpha1.SpendBudget{
			ObjectMeta: metav1.ObjectMeta{Name: corev1alpha1.SpendBudgetName, Namespace: ns.Name},
			Spec: corev1alpha1.SpendBudgetSpec{PeriodLimits: corev1alpha1.PeriodLimits{
				Spend: resource.MustParse("10"),
			}},
		}
		if change != nil {
			change(b)
		}
		return b
	}
	create := func(b *corev1alpha1.SpendBudget) error {
		err := k8sClient.Create(ctx, b)
		if err == nil {
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, b))).To(Succeed()) })
		}
		return err
	}

	It("accepts the name default and defaults the period to a day", func() {
		b := newBudget(nil)
		Expect(create(b)).To(Succeed())
		Expect(b.Spec.Period).To(Equal(corev1alpha1.SpendPeriodDay))
	})

	It("accepts a month period", func() {
		Expect(create(newBudget(func(b *corev1alpha1.SpendBudget) { b.Spec.Period = corev1alpha1.SpendPeriodMonth }))).
			To(Succeed())
	})

	DescribeTable("refuses an object that is not valid", func(change func(*corev1alpha1.SpendBudget), message string) {
		err := create(newBudget(change))
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "the error is not Invalid: %v", err)
		Expect(err.Error()).To(ContainSubstring(message))
	},
		Entry("a name that is not default", func(b *corev1alpha1.SpendBudget) { b.Name = "team" },
			"the name of a SpendBudget must be default"),
		Entry("a period that is not Day or Month", func(b *corev1alpha1.SpendBudget) { b.Spec.Period = "Hour" }, "spec.period"),
		Entry("a spend limit of 0", func(b *corev1alpha1.SpendBudget) { b.Spec.Spend = resource.MustParse("0") },
			"spend must be more than zero"),
		Entry("a spend limit below 0", func(b *corev1alpha1.SpendBudget) { b.Spec.Spend = resource.MustParse("-1") },
			"spend must be more than zero"),
	)

	It("keeps the ledger in the status subresource", func() {
		b := newBudget(nil)
		Expect(create(b)).To(Succeed())
		b.Status.Spend, b.Status.Reserved, b.Status.WaitingRuns = "1.5", "6", 2
		b.Status.Reservations = []corev1alpha1.SpendReservation{{RunID: "uid-1", Agent: "run-1", Budget: "6"}}
		Expect(k8sClient.Status().Update(ctx, b)).To(Succeed())
		got := &corev1alpha1.SpendBudget{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(b), got)).To(Succeed())
		Expect(got.Status.Spend).To(Equal("1.5"))
		Expect(got.Status.Reservations).To(Equal(b.Status.Reservations))

		got.Spec.Spend = resource.MustParse("20")
		got.Status.Spend = "0"
		Expect(k8sClient.Update(ctx, got)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(b), got)).To(Succeed())
		Expect(got.Status.Spend).To(Equal("1.5"), "a spec update must not change the status")
	})
})
