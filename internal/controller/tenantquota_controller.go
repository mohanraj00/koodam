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
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/gateway"
)

const (
	// tenantQuotaFinalizer keeps a TenantQuota until the gateway customer of its tenant label is
	// deleted, or a different TenantQuota has the label.
	tenantQuotaFinalizer = "core.koodam.dev/tenantquota-customer"
	// DefaultTenantUsageInterval is the time between 2 reads of the use of a tenant.
	DefaultTenantUsageInterval = 30 * time.Second

	reasonTenantSynced    = "Synced"
	reasonTenantDuplicate = "DuplicateTenant"
	reasonTenantGateway   = "GatewayError"
	reasonTenantWithin    = "WithinQuota"
)

// TenantQuotaReconciler makes the gateway customer of each tenant label and writes the use of
// the current period to the status of the TenantQuota. The condition QuotaExceeded is the mark
// that the claim check of an App reads.
//
// The App role has the grants of this controller. The verb update on tenantquotas adds and
// removes the finalizer. The controller makes no object for a TenantQuota.
type TenantQuotaReconciler struct {
	client.Client
	// Customers is the gateway with a customer for each tenant label.
	Customers gateway.Customers
	// Interval is the time between 2 reads of the use. If it is 0, the controller uses
	// DefaultTenantUsageInterval.
	Interval time.Duration
	// Now is the clock. If it is nil, the controller uses time.Now.
	Now func() time.Time
}

// +kubebuilder:rbac:groups=core.koodam.dev,resources=tenantquotas,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=core.koodam.dev,resources=tenantquotas/status,verbs=update
// +kubebuilder:rbac:groups=core.koodam.dev,resources=tenantquotas/finalizers,verbs=update

// Reconcile makes the gateway customer equal to the spec and reads its use.
func (r *TenantQuotaReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	q := &corev1alpha1.TenantQuota{}
	if err := r.Get(ctx, req.NamespacedName, q); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	owner, err := r.owner(ctx, q.Spec.Tenant)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !q.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.release(ctx, q, owner)
	}
	if !controllerutil.ContainsFinalizer(q, tenantQuotaFinalizer) {
		// The finalizer is in the object before the gateway gets a customer. The update starts the
		// next reconcile.
		controllerutil.AddFinalizer(q, tenantQuotaFinalizer)
		return ctrl.Result{}, r.Update(ctx, q)
	}
	old := q.Status.DeepCopy()
	if owner == nil || owner.UID != q.UID {
		// The gateway has one customer for each tenant label. An older TenantQuota has this label.
		q.Status = corev1alpha1.TenantQuotaStatus{Conditions: q.Status.Conditions}
		meta.RemoveStatusCondition(&q.Status.Conditions, corev1alpha1.TenantQuotaExceeded)
		r.setReady(q, metav1.ConditionFalse, reasonTenantDuplicate,
			"An older TenantQuota of the install has this tenant label. This TenantQuota has no effect.")
		return ctrl.Result{}, r.writeStatus(ctx, q, old)
	}
	usage, err := r.sync(ctx, q)
	if err != nil {
		// The message is fixed text, because a gateway error can have data of the gateway. The
		// condition QuotaExceeded keeps its last value.
		r.setReady(q, metav1.ConditionFalse, reasonTenantGateway, "A call to the gateway failed. The controller tries again.")
		if werr := r.writeStatus(ctx, q, old); werr != nil {
			return ctrl.Result{}, werr
		}
		return ctrl.Result{}, tenantGatewayError(err)
	}
	r.setUsage(ctx, q, usage)
	r.setReady(q, metav1.ConditionTrue, reasonTenantSynced, "The gateway has the customer of the tenant with the limits of the spec.")
	if err := r.writeStatus(ctx, q, old); err != nil {
		return ctrl.Result{}, err
	}
	interval := r.Interval
	if interval <= 0 {
		interval = DefaultTenantUsageInterval
	}
	return ctrl.Result{RequeueAfter: interval}, nil
}

// owner gives back the TenantQuota that has the tenant label in the gateway: the oldest one that
// is not in delete. The namespace and the name decide between 2 objects with the same time.
func (r *TenantQuotaReconciler) owner(ctx context.Context, tenant string) (*corev1alpha1.TenantQuota, error) {
	list := &corev1alpha1.TenantQuotaList{}
	if err := r.List(ctx, list); err != nil {
		return nil, err
	}
	return tenantOwner(list.Items, tenant), nil
}

// tenantOwner gives back the owner of a tenant label in a list of TenantQuotas, or nil.
func tenantOwner(all []corev1alpha1.TenantQuota, tenant string) *corev1alpha1.TenantQuota {
	var owner *corev1alpha1.TenantQuota
	for i := range all {
		q := &all[i]
		if q.Spec.Tenant != tenant || !q.DeletionTimestamp.IsZero() {
			continue
		}
		if owner == nil || tenantQuotaOrder(q, owner) < 0 {
			owner = q
		}
	}
	return owner
}

func tenantQuotaOrder(a, b *corev1alpha1.TenantQuota) int {
	if c := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); c != 0 {
		return c
	}
	if c := strings.Compare(a.Namespace, b.Namespace); c != 0 {
		return c
	}
	return strings.Compare(a.Name, b.Name)
}

// release deletes the gateway customer of a TenantQuota in delete, then removes the finalizer.
// If a different TenantQuota has the tenant label, the customer stays with its spend, and that
// TenantQuota sets the limits.
func (r *TenantQuotaReconciler) release(ctx context.Context, q, owner *corev1alpha1.TenantQuota) error {
	if !controllerutil.ContainsFinalizer(q, tenantQuotaFinalizer) {
		return nil
	}
	if owner == nil {
		if err := r.Customers.DeleteCustomer(ctx, q.Spec.Tenant); err != nil {
			return tenantGatewayError(err)
		}
		logf.FromContext(ctx).Info("Deleted the gateway customer of a tenant", "tenant", q.Spec.Tenant)
	}
	controllerutil.RemoveFinalizer(q, tenantQuotaFinalizer)
	return r.Update(ctx, q)
}

// sync makes the gateway customer equal to the spec and reads the use of the current period.
// After the gateway accepted the limits of a generation, a reconcile only reads the use. If the
// gateway then has no customer, the controller makes the customer again.
func (r *TenantQuotaReconciler) sync(ctx context.Context, q *corev1alpha1.TenantQuota) (gateway.CustomerUsage, error) {
	ready := meta.FindStatusCondition(q.Status.Conditions, corev1alpha1.TenantQuotaReady)
	if ready != nil && ready.Status == metav1.ConditionTrue && ready.ObservedGeneration == q.Generation {
		usage, err := r.Customers.CustomerUsage(ctx, q.Spec.Tenant)
		if !errors.Is(err, gateway.ErrCustomerNotFound) {
			return usage, err
		}
	}
	if err := r.Customers.SetCustomer(ctx, tenantCustomer(q)); err != nil {
		return gateway.CustomerUsage{}, err
	}
	return r.Customers.CustomerUsage(ctx, q.Spec.Tenant)
}

// errTenantGateway is the error of a gateway call that failed, with no text of the gateway.
var errTenantGateway = errors.New("a call to the gateway failed")

// tenantGatewayError gives back the error that a reconcile returns for a gateway error. The text
// of a gateway error can have data of the gateway, and controller-runtime writes a returned error
// to the manager log. Thus the result has fixed text and, for an HTTP answer, the status.
func tenantGatewayError(err error) error {
	if he, ok := errors.AsType[*gateway.HTTPError](err); ok {
		return fmt.Errorf("%w: HTTP %d", errTenantGateway, he.Status)
	}
	return errTenantGateway
}

// tenantCustomer gives back the gateway customer of a TenantQuota.
func tenantCustomer(q *corev1alpha1.TenantQuota) gateway.CustomerSpec {
	spec := gateway.CustomerSpec{ID: q.Spec.Tenant, Period: 24 * time.Hour}
	if q.Spec.Period == corev1alpha1.TenantQuotaPeriodHour {
		spec.Period = time.Hour
	}
	if q.Spec.Spend != nil {
		spec.Budget = q.Spec.Spend.AsApproximateFloat64()
	}
	if q.Spec.RateLimit != nil {
		// The gateway counts for each minute. A rate below 1 for each minute is 1.
		perMinute := q.Spec.RateLimit.RequestsPerSecond.AsApproximateFloat64() * 60
		spec.RequestsPerMinute = int64(math.Max(1, math.Ceil(perMinute)))
	}
	return spec
}

// setUsage writes the use to the status and sets the condition QuotaExceeded. A change of this
// condition goes to the log with the typed reason.
func (r *TenantQuotaReconciler) setUsage(ctx context.Context, q *corev1alpha1.TenantQuota, u gateway.CustomerUsage) {
	q.Status.Requests = u.Requests
	// The gateway adds small float values. 6 decimal places keep the status stable.
	q.Status.Spend = strconv.FormatFloat(math.Round(u.Spend*1e6)/1e6, 'f', -1, 64)
	q.Status.PeriodEnd = &metav1.Time{Time: u.PeriodEnd.Truncate(time.Second)}
	var limits []string
	if q.Spec.Requests != nil && u.Requests >= *q.Spec.Requests {
		limits = append(limits, "requests")
	}
	if q.Spec.Spend != nil && u.Spend >= q.Spec.Spend.AsApproximateFloat64() {
		limits = append(limits, "spend")
	}
	cond := metav1.Condition{Type: corev1alpha1.TenantQuotaExceeded, Status: metav1.ConditionFalse,
		Reason: reasonTenantWithin, Message: "The tenant is below each limit of the current period.",
		ObservedGeneration: q.Generation}
	if len(limits) > 0 {
		cond.Status, cond.Reason = metav1.ConditionTrue, corev1alpha1.ReasonTenantQuotaExceeded
		cond.Message = "The tenant is at this limit of the current period: " + strings.Join(limits, ", ") + "."
	}
	if meta.SetStatusCondition(&q.Status.Conditions, cond) {
		logf.FromContext(ctx).Info("The quota state of a tenant changed", "reason", cond.Reason,
			"tenant", q.Spec.Tenant, "requests", q.Status.Requests, "spend", q.Status.Spend)
	}
}

func (r *TenantQuotaReconciler) setReady(q *corev1alpha1.TenantQuota, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&q.Status.Conditions, metav1.Condition{Type: corev1alpha1.TenantQuotaReady, Status: status,
		Reason: reason, Message: message, ObservedGeneration: q.Generation})
}

// writeStatus writes the status only if it changed. ObservedTime is the time of this write.
func (r *TenantQuotaReconciler) writeStatus(ctx context.Context, q *corev1alpha1.TenantQuota,
	old *corev1alpha1.TenantQuotaStatus) error {
	q.Status.ObservedTime = old.ObservedTime
	if equality.Semantic.DeepEqual(*old, q.Status) {
		return nil
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	q.Status.ObservedTime = &metav1.Time{Time: now().Truncate(time.Second)}
	return r.Status().Update(ctx, q)
}

// sameTenant gives the other TenantQuotas with the tenant label of a TenantQuota that changed.
// Thus a TenantQuota gets the label when the owner of the label goes.
func (r *TenantQuotaReconciler) sameTenant(ctx context.Context, obj client.Object) []reconcile.Request {
	q, ok := obj.(*corev1alpha1.TenantQuota)
	list := &corev1alpha1.TenantQuotaList{}
	if !ok || r.List(ctx, list) != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		if o := &list.Items[i]; o.Spec.Tenant == q.Spec.Tenant && o.UID != q.UID {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(o)})
		}
	}
	return reqs
}

// SetupWithManager sets up the controller with the Manager.
func (r *TenantQuotaReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.TenantQuota{}).
		Watches(&corev1alpha1.TenantQuota{}, handler.EnqueueRequestsFromMapFunc(r.sameTenant)).
		Named("tenantquota").
		Complete(r)
}
