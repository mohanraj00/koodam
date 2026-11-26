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
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/tenanttoken"
)

// reservedQuota makes a TenantQuota with the reserved label, as an object from before the API
// rule. It has the finalizer and the status of a TenantQuota that had an effect.
func reservedQuota(t *testing.T) (client.Client, *TenantQuotaReconciler, *fakeCustomers, reconcile.Request) {
	t.Helper()
	q := &corev1alpha1.TenantQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "reserved", Namespace: testTeam, Finalizers: []string{tenantQuotaFinalizer}},
		Spec:       corev1alpha1.TenantQuotaSpec{Tenant: tenanttoken.NoTenant, Requests: new(int64(10))},
		Status: corev1alpha1.TenantQuotaStatus{Requests: 12, Spend: "1", Conditions: []metav1.Condition{{
			Type: corev1alpha1.TenantQuotaExceeded, Status: metav1.ConditionTrue, Reason: "TenantQuotaExceeded"}}},
	}
	c := fake.NewClientBuilder().WithScheme(ingWorld(t).Scheme()).WithObjects(q).
		WithStatusSubresource(&corev1alpha1.TenantQuota{}).Build()
	customers := newFakeCustomers()
	return c, &TenantQuotaReconciler{Client: c, Customers: customers}, customers,
		reconcile.Request{NamespacedName: client.ObjectKeyFromObject(q)}
}

// A TenantQuota with the reserved label gets no finalizer, no use and no gateway call. Limits on
// the customer of this label would stop the model calls of each pod with no tenant.
func TestTenantQuotaWithTheReservedLabelHasNoEffect(t *testing.T) {
	c, r, customers, req := reservedQuota(t)
	for range 3 {
		if res, err := r.Reconcile(t.Context(), req); err != nil || res != (reconcile.Result{}) {
			t.Fatalf("Reconcile gave the result %+v and the error %v.", res, err)
		}
	}
	q := &corev1alpha1.TenantQuota{}
	if err := c.Get(t.Context(), req.NamespacedName, q); err != nil {
		t.Fatal(err)
	}
	ready := meta.FindStatusCondition(q.Status.Conditions, corev1alpha1.TenantQuotaReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != reasonTenantReserved {
		t.Errorf("The condition Ready is %+v.", ready)
	}
	if meta.FindStatusCondition(q.Status.Conditions, corev1alpha1.TenantQuotaExceeded) != nil ||
		q.Status.Requests != 0 || q.Status.Spend != "" {
		t.Errorf("The status has the use of a tenant: %+v.", q.Status)
	}
	if len(q.Finalizers) != 0 {
		t.Errorf("The object has the finalizers %v.", q.Finalizers)
	}
	if customers.sets != 0 || len(customers.deletes) != 0 {
		t.Errorf("The gateway got %d set calls and the delete calls %v.", customers.sets, customers.deletes)
	}
}

// The delete of a TenantQuota with the reserved label does not delete the gateway customer,
// which has the count of each model call with no tenant.
func TestDeleteOfATenantQuotaWithTheReservedLabelKeepsTheCustomer(t *testing.T) {
	c, r, customers, req := reservedQuota(t)
	q := &corev1alpha1.TenantQuota{}
	if err := c.Get(t.Context(), req.NamespacedName, q); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := r.Reconcile(t.Context(), req); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Get(t.Context(), req.NamespacedName, q); !apierrors.IsNotFound(err) {
		t.Errorf("The object is not gone after its delete: %v.", err)
	}
	if customers.sets != 0 || len(customers.deletes) != 0 {
		t.Errorf("The gateway got %d set calls and the delete calls %v.", customers.sets, customers.deletes)
	}
}
