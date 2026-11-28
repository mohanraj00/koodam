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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
	"github.com/mohanraj00/koodam/internal/tenanttoken"
)

// The CRD validation of the TenantQuota. These tests do not run a controller. They only send
// objects to the API server.
var _ = Describe("TenantQuota CRD", func() {
	ctx := context.Background()
	const tenant = "acme"

	newQuota := func(change func(*corev1beta1.TenantQuotaSpec)) *corev1beta1.TenantQuota {
		q := &corev1beta1.TenantQuota{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "quota-crd-", Namespace: metav1.NamespaceDefault},
			Spec:       corev1beta1.TenantQuotaSpec{Tenant: tenant},
		}
		if change != nil {
			change(&q.Spec)
		}
		return q
	}
	create := func(q *corev1beta1.TenantQuota) error {
		err := k8sClient.Create(ctx, q)
		if err == nil {
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, q))).To(Succeed()) })
		}
		return err
	}
	rate := func(perSecond string, burst *int32) *corev1beta1.TenantRateLimit {
		return &corev1beta1.TenantRateLimit{RequestsPerSecond: resource.MustParse(perSecond), Burst: burst}
	}

	It("accepts a tenant label with no limit and defaults the period to a day", func() {
		q := newQuota(nil)
		Expect(create(q)).To(Succeed())
		Expect(q.Spec.Period).To(Equal(corev1beta1.TenantQuotaPeriodDay))
		Expect(q.Spec.AppRef).To(BeNil())
	})

	It("accepts each limit", func() {
		q := newQuota(func(s *corev1beta1.TenantQuotaSpec) {
			s.AppRef = &corev1beta1.AppReference{Name: "store"}
			s.Period = corev1beta1.TenantQuotaPeriodHour
			s.Requests = new(int64(100))
			s.Spend = new(resource.MustParse("2500m"))
			s.RateLimit = rate("500m", new(int32(3)))
		})
		Expect(create(q)).To(Succeed())
	})

	It("accepts a tenant label of 253 bytes", func() {
		Expect(create(newQuota(func(s *corev1beta1.TenantQuotaSpec) { s.Tenant = strings.Repeat("é", 126) + "a" }))).To(Succeed())
	})

	DescribeTable("refuses a spec that is not valid", func(change func(*corev1beta1.TenantQuotaSpec), message string) {
		err := create(newQuota(change))
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "the error is not Invalid: %v", err)
		Expect(err.Error()).To(ContainSubstring(message))
	},
		Entry("an empty tenant label", func(s *corev1beta1.TenantQuotaSpec) { s.Tenant = "" }, "spec.tenant"),
		Entry("a long tenant label", func(s *corev1beta1.TenantQuotaSpec) { s.Tenant = strings.Repeat("a", 254) }, "spec.tenant"),
		Entry("the reserved tenant label", func(s *corev1beta1.TenantQuotaSpec) { s.Tenant = tenanttoken.NoTenant },
			"spec.tenant must not be the reserved label"),
		// The claim check counts the bytes of a label, so 127 characters of 2 bytes are too long.
		Entry("a tenant label of 254 bytes", func(s *corev1beta1.TenantQuotaSpec) { s.Tenant = strings.Repeat("é", 127) },
			"253 bytes or less"),
		Entry("an empty App name", func(s *corev1beta1.TenantQuotaSpec) { s.AppRef = &corev1beta1.AppReference{} },
			"spec.appRef.name"),
		Entry("a period that is not Hour or Day", func(s *corev1beta1.TenantQuotaSpec) { s.Period = "Year" }, "spec.period"),
		Entry("a request limit of 0", func(s *corev1beta1.TenantQuotaSpec) { s.Requests = new(int64(0)) }, "spec.requests"),
		Entry("a spend limit of 0", func(s *corev1beta1.TenantQuotaSpec) { s.Spend = new(resource.MustParse("0")) },
			"spend must be more than zero"),
		Entry("a spend limit below 0", func(s *corev1beta1.TenantQuotaSpec) { s.Spend = new(resource.MustParse("-1")) },
			"spend must be more than zero"),
		Entry("a rate of 0", func(s *corev1beta1.TenantQuotaSpec) { s.RateLimit = rate("0", nil) },
			"requestsPerSecond must be more than zero"),
		Entry("a rate above the maximum", func(s *corev1beta1.TenantQuotaSpec) { s.RateLimit = rate("1000001", nil) },
			"requestsPerSecond must be 1000000 or less"),
		Entry("a burst of 0", func(s *corev1beta1.TenantQuotaSpec) { s.RateLimit = rate("1", new(int32(0))) },
			"spec.rateLimit.burst"),
	)

	It("refuses a change of the tenant label and permits a change of a limit", func() {
		q := newQuota(nil)
		Expect(create(q)).To(Succeed())
		q.Spec.Requests = new(int64(5))
		Expect(k8sClient.Update(ctx, q)).To(Succeed())
		q.Spec.Tenant = "globex"
		err := k8sClient.Update(ctx, q)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "the error is not Invalid: %v", err)
		Expect(err.Error()).To(ContainSubstring("spec.tenant is immutable."))
	})

	It("keeps the use in the status subresource", func() {
		q := newQuota(nil)
		Expect(create(q)).To(Succeed())
		q.Status.Requests, q.Status.Spend = 7, "0.125"
		Expect(k8sClient.Status().Update(ctx, q)).To(Succeed())
		got := &corev1beta1.TenantQuota{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(q), got)).To(Succeed())
		Expect(got.Status.Requests).To(Equal(int64(7)))
		Expect(got.Status.Spend).To(Equal(q.Status.Spend))
	})
})
