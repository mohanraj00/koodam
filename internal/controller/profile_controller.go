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
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

// ProfileReconciler writes the bound namespaces of each Profile. It only reads the Profiles and
// the Namespaces. It writes only the Profile status.
type ProfileReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=core.koodam.dev,resources=profiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=core.koodam.dev,resources=profiles/status,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

// Reconcile finds the namespaces that the Profile lists and that have the label
// koodam.dev/profile=<Profile name>. It writes them to status.boundNamespaces.
func (r *ProfileReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	p := &corev1alpha1.Profile{}
	if err := r.Get(ctx, req.NamespacedName, p); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	bound := []string{}
	for _, name := range p.Spec.Namespaces {
		ns := &corev1.Namespace{}
		err := r.Get(ctx, types.NamespacedName{Name: name}, ns)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return ctrl.Result{}, err
		}
		if namespaceBound(p, ns) {
			bound = append(bound, name)
		}
	}
	slices.Sort(bound)

	old := p.Status.DeepCopy()
	p.Status.BoundNamespaces = bound
	meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		Message:            fmt.Sprintf("%d of %d namespaces are bound", len(bound), len(p.Spec.Namespaces)),
		ObservedGeneration: p.Generation,
	})
	if slices.Equal(old.BoundNamespaces, bound) && conditionsEqual(old.Conditions, p.Status.Conditions) {
		return ctrl.Result{}, nil
	}
	if err := r.Status().Update(ctx, p); err != nil {
		return ctrl.Result{}, err
	}
	log.Info("Updated Profile status", "boundNamespaces", bound)
	return ctrl.Result{}, nil
}

// namespaceBound is the binding rule. The namespace is bound to the Profile only if the Profile
// lists the namespace and the namespace has the label koodam.dev/profile=<Profile name>. A label
// alone gives nothing. If the namespace or the Profile is in deletion, the namespace is not bound.
func namespaceBound(p *corev1alpha1.Profile, ns *corev1.Namespace) bool {
	return ns.Labels[corev1alpha1.ProfileLabel] == p.Name && ns.DeletionTimestamp.IsZero() &&
		p.DeletionTimestamp.IsZero() &&
		slices.Contains(p.Spec.Namespaces, ns.Name)
}

// profileBinds reads the namespace and the Profile in its label, and applies namespaceBound.
// It returns false and no error if the namespace, the label or the Profile does not exist.
func profileBinds(ctx context.Context, c client.Reader, namespace string) (bool, error) {
	ns := &corev1.Namespace{}
	if err := c.Get(ctx, types.NamespacedName{Name: namespace}, ns); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	name := ns.Labels[corev1alpha1.ProfileLabel]
	if name == "" {
		return false, nil
	}
	p := &corev1alpha1.Profile{}
	if err := c.Get(ctx, types.NamespacedName{Name: name}, p); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return namespaceBound(p, ns), nil
}

// conditionsEqual compares the conditions without the transition time.
func conditionsEqual(a, b []metav1.Condition) bool {
	return slices.EqualFunc(a, b, func(x, y metav1.Condition) bool {
		return x.Type == y.Type && x.Status == y.Status && x.Reason == y.Reason &&
			x.Message == y.Message && x.ObservedGeneration == y.ObservedGeneration
	})
}

// profileForNamespace maps a Namespace to the Profile in its label. On an update, the handler
// calls it for the old and the new object. Thus a label change reconciles the two Profiles.
func profileForNamespace(_ context.Context, obj client.Object) []reconcile.Request {
	name := obj.GetLabels()[corev1alpha1.ProfileLabel]
	if name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: name}}}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ProfileReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.Profile{}).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(profileForNamespace)).
		Named("profile").
		Complete(r)
}
