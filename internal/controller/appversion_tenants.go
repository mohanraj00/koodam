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
	"encoding/json"
	"errors"
	"math"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/appclaim"
)

const (
	// tenantsVolume is the name of the volume with the limits file in an App pod.
	tenantsVolume = "koodam-tenants"
	// tenantsDir has the limits file in the check container.
	tenantsDir = "/etc/koodam-tenants"
	// tenantsFile is the key of the limits file in the ConfigMap.
	tenantsFile = "tenants.json"
)

// AppTenantLimits is the configuration of the install for a tenant label with no TenantQuota.
type AppTenantLimits struct {
	// DefaultRate is the rate limit of such a tenant, in requests for each second. 0 gives no
	// rate limit.
	DefaultRate float64
	// DefaultBurst is the burst of such a tenant. If it is 0, it is the rate, with a minimum of 1.
	DefaultBurst int32
	// RefuseUnknown refuses such a tenant with the reason UnknownTenant.
	RefuseUnknown bool
}

// tenantsEnabled tells if the check container reads a limits file. The check has a tenant label
// only with a tenant claim.
func (c AppClaimCheck) tenantsEnabled() bool { return c.enabled() && c.TenantClaim != "" }

func tenantLimitsName(av *corev1alpha1.AppVersion) string { return appVersionName(av) + "-tenants" }

// podLimit gives back the limit of one pod: the rate and the burst divided by the replicas. Thus
// the pods of a version permit the rate of the tenant together. The burst of a pod is 1 or more,
// so the sum of the bursts can be more than the burst of the tenant.
func podLimit(rate float64, burst, replicas int32) appclaim.TenantLimit {
	if rate <= 0 {
		return appclaim.TenantLimit{}
	}
	if burst <= 0 {
		burst = int32(math.Min(math.Max(1, math.Ceil(rate)), math.MaxInt32))
	}
	n := float64(max(replicas, 1))
	return appclaim.TenantLimit{Rate: rate / n, Burst: int(math.Max(1, math.Ceil(float64(burst)/n)))}
}

// tenantOwners gives back the owner of each tenant label in a list of TenantQuotas. It reads the
// list one time, so the limits file of an App with many tenants uses little time. It must agree
// with tenantOwner.
func tenantOwners(all []corev1alpha1.TenantQuota) map[string]*corev1alpha1.TenantQuota {
	owners := make(map[string]*corev1alpha1.TenantQuota, len(all))
	for i := range all {
		q := &all[i]
		if !q.DeletionTimestamp.IsZero() {
			continue
		}
		if owner := owners[q.Spec.Tenant]; owner == nil || tenantQuotaOrder(q, owner) < 0 {
			owners[q.Spec.Tenant] = q
		}
	}
	return owners
}

// limits gives back the limits file of a version. It has each TenantQuota of the namespace that
// has its tenant label in the install and that applies to the App of the version.
func (c AppClaimCheck) limits(av *corev1alpha1.AppVersion, quotas []corev1alpha1.TenantQuota) appclaim.TenantLimits {
	replicas := int32(1)
	if av.Spec.Replicas != nil {
		replicas = *av.Spec.Replicas
	}
	out := appclaim.TenantLimits{RefuseUnknown: c.Tenants.RefuseUnknown, Tenants: map[string]appclaim.TenantLimit{}}
	if c.Tenants.DefaultRate > 0 {
		out.Default = new(podLimit(c.Tenants.DefaultRate, c.Tenants.DefaultBurst, replicas))
	}
	owners := tenantOwners(quotas)
	for i := range quotas {
		q := &quotas[i]
		if q.Namespace != av.Namespace || (q.Spec.AppRef != nil && q.Spec.AppRef.Name != av.Spec.AppRef.Name) {
			continue
		}
		if owners[q.Spec.Tenant] != q {
			continue
		}
		var limit appclaim.TenantLimit
		if rl := q.Spec.RateLimit; rl != nil {
			burst := int32(0)
			if rl.Burst != nil {
				burst = *rl.Burst
			}
			limit = podLimit(rl.RequestsPerSecond.AsApproximateFloat64(), burst, replicas)
		}
		limit.Exceeded = meta.IsStatusConditionTrue(q.Status.Conditions, corev1alpha1.TenantQuotaExceeded)
		out.Tenants[q.Spec.Tenant] = limit
	}
	return out
}

// applyTenantLimits writes the ConfigMap with the limits file of a version. The kubelet updates
// the file in the pods, and the check container reads it again. It gives back errNotOwned if a
// ConfigMap with the name is not from the version.
func applyTenantLimits(ctx context.Context, c client.Client, reader client.Reader, claim AppClaimCheck,
	av *corev1alpha1.AppVersion) error {
	if !claim.tenantsEnabled() {
		return nil
	}
	list := &corev1alpha1.TenantQuotaList{}
	if err := c.List(ctx, list); err != nil {
		return err
	}
	data, err := json.Marshal(claim.limits(av, list.Items))
	if err != nil {
		return err
	}
	m := appVersionMeta(av)
	m.Name = tenantLimitsName(av)
	want := &corev1.ConfigMap{ObjectMeta: m, Data: map[string]string{tenantsFile: string(data)}}
	// The cache of the manager has only the ConfigMaps of the Workspaces.
	have := &corev1.ConfigMap{}
	err = reader.Get(ctx, types.NamespacedName{Namespace: m.Namespace, Name: m.Name}, have)
	switch {
	case apierrors.IsNotFound(err):
		return c.Create(ctx, want)
	case err != nil:
		return err
	case !metav1.IsControlledBy(have, av):
		return errNotOwned
	case have.Data[tenantsFile] == want.Data[tenantsFile] && len(have.Data) == 1:
		return nil
	}
	have.Data = want.Data
	return c.Update(ctx, have)
}

// TenantLimitsReconciler keeps the limits file of each AppVersion equal to the TenantQuotas. It
// is a separate controller, because a change of a TenantQuota must not start the key work of the
// AppVersion controller. The base role has the ConfigMap grants that it uses.
type TenantLimitsReconciler struct {
	client.Client
	// APIReader reads the ConfigMap directly, not from the cache.
	APIReader client.Reader
	// Claim is the signed claim check at the ingress of each App.
	Claim AppClaimCheck
}

// Reconcile writes the limits file of one AppVersion.
func (r *TenantLimitsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	av := &corev1alpha1.AppVersion{}
	if err := r.Get(ctx, req.NamespacedName, av); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !av.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	err := applyTenantLimits(ctx, r.Client, r.APIReader, r.Claim, av)
	if errors.Is(err, errNotOwned) {
		// The AppVersion controller shows this state in the condition Provisioned.
		logf.FromContext(ctx).Info("A ConfigMap with the name of the tenant limits is not from the AppVersion")
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, err
}

// allVersions gives each AppVersion after a change of a TenantQuota. A TenantQuota of one
// namespace can take a tenant label from a TenantQuota of a different namespace.
func (r *TenantLimitsReconciler) allVersions(ctx context.Context, _ client.Object) []reconcile.Request {
	list := &corev1alpha1.AppVersionList{}
	if err := r.List(ctx, list); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list the AppVersions for the tenant limits")
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return reqs
}

// SetupWithManager sets up the controller with the Manager.
func (r *TenantLimitsReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.AppVersion{}).
		Watches(&corev1alpha1.TenantQuota{}, handler.EnqueueRequestsFromMapFunc(r.allVersions)).
		Named("tenantlimits").
		Complete(r)
}
