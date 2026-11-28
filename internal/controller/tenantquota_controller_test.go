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
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
	"github.com/mohanraj00/koodam/internal/gateway"
)

// fakeCustomers is a gateway with customers in memory.
type fakeCustomers struct {
	specs   map[string]gateway.CustomerSpec
	usage   map[string]gateway.CustomerUsage
	sets    int
	deletes []string
	err     error
}

func newFakeCustomers() *fakeCustomers {
	return &fakeCustomers{specs: map[string]gateway.CustomerSpec{}, usage: map[string]gateway.CustomerUsage{}}
}

func (f *fakeCustomers) SetCustomer(_ context.Context, s gateway.CustomerSpec) error {
	if f.err != nil {
		return f.err
	}
	f.sets++
	f.specs[s.ID] = s
	return nil
}

func (f *fakeCustomers) CustomerUsage(_ context.Context, id string) (gateway.CustomerUsage, error) {
	if f.err != nil {
		return gateway.CustomerUsage{}, f.err
	}
	if _, ok := f.specs[id]; !ok {
		return gateway.CustomerUsage{}, gateway.ErrCustomerNotFound
	}
	return f.usage[id], nil
}

func (f *fakeCustomers) DeleteCustomer(_ context.Context, id string) error {
	if f.err != nil {
		return f.err
	}
	f.deletes = append(f.deletes, id)
	delete(f.specs, id)
	return nil
}

var _ = Describe("TenantQuota Controller", func() {
	ctx := context.Background()
	periodEnd := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	var (
		gw *fakeCustomers
		r  *TenantQuotaReconciler
		ns string
	)

	BeforeEach(func() {
		gw = newFakeCustomers()
		r = &TenantQuotaReconciler{Client: k8sClient, Customers: gw, Interval: time.Minute}
		n := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "tq-"}}
		Expect(k8sClient.Create(ctx, n)).To(Succeed())
		ns = n.Name
	})

	// newQuota makes a TenantQuota. The cleanup removes the finalizer, because no controller runs.
	newQuota := func(name, tenant string, change func(*corev1beta1.TenantQuotaSpec)) *corev1beta1.TenantQuota {
		q := &corev1beta1.TenantQuota{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: corev1beta1.TenantQuotaSpec{Tenant: tenant}}
		if change != nil {
			change(&q.Spec)
		}
		Expect(k8sClient.Create(ctx, q)).To(Succeed())
		DeferCleanup(func() {
			cur := &corev1beta1.TenantQuota{}
			if k8sClient.Get(ctx, client.ObjectKeyFromObject(q), cur) == nil {
				cur.Finalizers = nil
				Expect(k8sClient.Update(ctx, cur)).To(Succeed())
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, cur))).To(Succeed())
			}
		})
		return q
	}
	// run does one reconcile of a TenantQuota and gives the object back. A new TenantQuota first
	// gets its finalizer in a reconcile of its own.
	run := func(q *corev1beta1.TenantQuota) (*corev1beta1.TenantQuota, reconcile.Result, error) {
		req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(q)}
		if len(q.Finalizers) == 0 && q.DeletionTimestamp.IsZero() {
			_, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
		}
		res, err := r.Reconcile(ctx, req)
		got := &corev1beta1.TenantQuota{}
		if gerr := k8sClient.Get(ctx, req.NamespacedName, got); gerr != nil {
			return nil, res, err
		}
		return got, res, err
	}
	cond := func(q *corev1beta1.TenantQuota, t string) *metav1.Condition {
		return meta.FindStatusCondition(q.Status.Conditions, t)
	}

	It("makes the gateway customer and writes the use", func() {
		const tenant = "tq-sync"
		q := newQuota("sync", tenant, func(s *corev1beta1.TenantQuotaSpec) {
			s.Period = corev1beta1.TenantQuotaPeriodHour
			s.Requests = new(int64(10))
			s.Spend = new(resource.MustParse("2500m"))
			s.RateLimit = &corev1beta1.TenantRateLimit{RequestsPerSecond: resource.MustParse("500m")}
		})
		gw.usage[tenant] = gateway.CustomerUsage{Spend: 0.1234567, Requests: 4, PeriodEnd: periodEnd}
		got, res, err := run(q)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(time.Minute))
		Expect(got.Finalizers).To(ContainElement(tenantQuotaFinalizer))
		Expect(gw.specs[tenant]).To(Equal(gateway.CustomerSpec{ID: tenant, Budget: 2.5, Period: time.Hour,
			RequestsPerMinute: 30}))
		Expect(got.Status.Requests).To(Equal(int64(4)))
		Expect(got.Status.Spend).To(Equal("0.123457"))
		Expect(got.Status.PeriodEnd.Time.Equal(periodEnd)).To(BeTrue())
		Expect(got.Status.ObservedTime).NotTo(BeNil())
		Expect(cond(got, corev1beta1.TenantQuotaReady).Reason).To(Equal(reasonTenantSynced))
		exceeded := cond(got, corev1beta1.TenantQuotaExceeded)
		Expect(exceeded.Status).To(Equal(metav1.ConditionFalse))
		Expect(exceeded.Reason).To(Equal(reasonTenantWithin))

		// The next reconcile only reads the use, and a status with no change is not written.
		version := got.ResourceVersion
		got, _, err = run(got)
		Expect(err).NotTo(HaveOccurred())
		Expect(gw.sets).To(Equal(1))
		Expect(got.ResourceVersion).To(Equal(version))
	})

	DescribeTable("marks a tenant at a limit with the typed reason",
		func(usage gateway.CustomerUsage, exceeded bool, message string) {
			const tenant = "tq-limit"
			q := newQuota("limit", tenant, func(s *corev1beta1.TenantQuotaSpec) {
				s.Requests = new(int64(10))
				s.Spend = new(resource.MustParse("1"))
			})
			usage.PeriodEnd = periodEnd
			gw.usage[tenant] = usage
			got, _, err := run(q)
			Expect(err).NotTo(HaveOccurred())
			c := cond(got, corev1beta1.TenantQuotaExceeded)
			if !exceeded {
				Expect(c.Status).To(Equal(metav1.ConditionFalse))
				return
			}
			Expect(c.Status).To(Equal(metav1.ConditionTrue))
			Expect(c.Reason).To(Equal(corev1beta1.ReasonTenantQuotaExceeded))
			Expect(c.Message).To(ContainSubstring(message))
			// A new period removes the mark.
			gw.usage[tenant] = gateway.CustomerUsage{PeriodEnd: periodEnd.Add(24 * time.Hour)}
			got, _, err = run(got)
			Expect(err).NotTo(HaveOccurred())
			Expect(cond(got, corev1beta1.TenantQuotaExceeded).Status).To(Equal(metav1.ConditionFalse))
			Expect(got.Status.Requests).To(BeZero())
		},
		Entry("below each limit", gateway.CustomerUsage{Requests: 9, Spend: 0.99}, false, ""),
		Entry("at the request limit", gateway.CustomerUsage{Requests: 10}, true, "requests"),
		Entry("at the spend limit", gateway.CustomerUsage{Spend: 1}, true, "spend"),
		Entry("above the 2 limits", gateway.CustomerUsage{Requests: 11, Spend: 1.5}, true, "requests, spend"),
	)

	It("gives no mark for a capped count below the request limit", func() {
		const tenant = "tq-capped"
		q := newQuota("capped", tenant, func(s *corev1beta1.TenantQuotaSpec) { s.Requests = new(int64(10)) })
		gw.usage[tenant] = gateway.CustomerUsage{Requests: 9, RequestsCapped: true, PeriodEnd: periodEnd}
		got, _, err := run(q)
		Expect(err).NotTo(HaveOccurred())
		c := cond(got, corev1beta1.TenantQuotaExceeded)
		Expect(c.Status).To(Equal(metav1.ConditionUnknown))
		Expect(c.Reason).To(Equal("RequestCountCapped"))
		Expect(got.Status.Requests).To(Equal(int64(9)))
		// A capped count at the limit is sufficient for the mark.
		gw.usage[tenant] = gateway.CustomerUsage{Requests: 10, RequestsCapped: true, PeriodEnd: periodEnd}
		got, _, err = run(got)
		Expect(err).NotTo(HaveOccurred())
		Expect(cond(got, corev1beta1.TenantQuotaExceeded).Status).To(Equal(metav1.ConditionTrue))
	})

	It("gives a tenant with no limit a customer with a day period and no mark", func() {
		const tenant = "tq-open"
		got, _, err := run(newQuota("open", tenant, nil))
		Expect(err).NotTo(HaveOccurred())
		Expect(gw.specs[tenant]).To(Equal(gateway.CustomerSpec{ID: tenant, Period: 24 * time.Hour}))
		Expect(cond(got, corev1beta1.TenantQuotaExceeded).Status).To(Equal(metav1.ConditionFalse))
	})

	It("sets the limits again after a spec change, and makes a lost customer again", func() {
		const tenant = "tq-change"
		got, _, err := run(newQuota("change", tenant, nil))
		Expect(err).NotTo(HaveOccurred())
		got.Spec.Spend = new(resource.MustParse("3"))
		Expect(k8sClient.Update(ctx, got)).To(Succeed())
		got, _, err = run(got)
		Expect(err).NotTo(HaveOccurred())
		Expect(gw.specs[tenant].Budget).To(Equal(3.0))
		Expect(cond(got, corev1beta1.TenantQuotaReady).ObservedGeneration).To(Equal(got.Generation))
		sets := gw.sets
		delete(gw.specs, tenant)
		_, _, err = run(got)
		Expect(err).NotTo(HaveOccurred())
		Expect(gw.sets).To(Equal(sets + 1))
		Expect(gw.specs).To(HaveKey(tenant))
	})

	It("shows a gateway error with fixed text and keeps the mark", func() {
		const tenant = "tq-error"
		q := newQuota("error", tenant, func(s *corev1beta1.TenantQuotaSpec) { s.Requests = new(int64(1)) })
		gw.usage[tenant] = gateway.CustomerUsage{Requests: 1, PeriodEnd: periodEnd}
		got, _, err := run(q)
		Expect(err).NotTo(HaveOccurred())
		gw.err = &gateway.HTTPError{Path: "/customer/info", Status: 500, Body: "the gateway answer has sk-secret-data"}
		got, _, err = run(got)
		// controller-runtime writes the returned error to the log, so it has no text of the gateway.
		Expect(err).To(MatchError(errTenantGateway))
		Expect(err.Error()).To(Equal("a call to the gateway failed: HTTP 500"))
		ready := cond(got, corev1beta1.TenantQuotaReady)
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal(reasonTenantGateway))
		Expect(ready.Message).NotTo(ContainSubstring("sk-"))
		Expect(cond(got, corev1beta1.TenantQuotaExceeded).Status).To(Equal(metav1.ConditionTrue))
	})

	It("gives a tenant label to the oldest TenantQuota only", func() {
		const tenant = "tq-twice"
		first := newQuota("first", tenant, func(s *corev1beta1.TenantQuotaSpec) { s.Spend = new(resource.MustParse("1")) })
		// The API server keeps the creation time in seconds, and the name decides for the same second.
		second := newQuota("second", tenant, func(s *corev1beta1.TenantQuotaSpec) { s.Spend = new(resource.MustParse("9")) })
		_, _, err := run(first)
		Expect(err).NotTo(HaveOccurred())
		got, res, err := run(second)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(BeZero())
		Expect(gw.specs[tenant].Budget).To(Equal(1.0))
		ready := cond(got, corev1beta1.TenantQuotaReady)
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal(reasonTenantDuplicate))
		Expect(cond(got, corev1beta1.TenantQuotaExceeded)).To(BeNil())
		Expect(r.sameTenant(ctx, first)).To(ConsistOf(reconcile.Request{NamespacedName: client.ObjectKeyFromObject(second)}))

		// A delete of the owner keeps the customer and its spend, and the second one gets the label.
		Expect(k8sClient.Delete(ctx, first)).To(Succeed())
		_, _, err = run(first)
		Expect(err).NotTo(HaveOccurred())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(first), first))).To(BeTrue())
		Expect(gw.deletes).To(BeEmpty())
		got, _, err = run(second)
		Expect(err).NotTo(HaveOccurred())
		Expect(gw.specs[tenant].Budget).To(Equal(9.0))
		Expect(cond(got, corev1beta1.TenantQuotaReady).Status).To(Equal(metav1.ConditionTrue))
	})

	It("deletes the gateway customer before it removes the finalizer", func() {
		const tenant = "tq-delete"
		q := newQuota("delete", tenant, nil)
		got, _, err := run(q)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Delete(ctx, got)).To(Succeed())
		// A gateway error keeps the TenantQuota.
		gw.err = errors.New("the gateway is down with sk-secret-data")
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(q)})
		Expect(err).To(MatchError(errTenantGateway))
		Expect(err.Error()).To(Equal("a call to the gateway failed"))
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(q), got)).To(Succeed())
		gw.err = nil
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(q)})
		Expect(err).NotTo(HaveOccurred())
		Expect(gw.deletes).To(Equal([]string{tenant}))
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(q), got))).To(BeTrue())
	})
})
