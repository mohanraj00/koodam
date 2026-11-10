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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/gateway"
)

// TestKeyReconciler manages gateway keys for individual Workspace tests.
type TestKeyReconciler struct {
	client.Client
	APIReader client.Reader
	Gateway   gateway.Gateway
}

// Reconcile writes the condition Active=False when the Workspace refuses the key. It removes
// an old refusal when the Workspace permits the key again.
func (r *TestKeyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	key := &corev1alpha1.TestKey{}
	if err := r.APIReader.Get(ctx, req.NamespacedName, key); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	workspace := &corev1alpha1.Workspace{}
	ref := client.ObjectKey{Namespace: key.Namespace, Name: key.Spec.WorkspaceRef.Name}
	var reason, message string
	switch err := r.APIReader.Get(ctx, ref, workspace); {
	case apierrors.IsNotFound(err):
		reason, message = "WorkspaceNotFound", "The Workspace does not exist."
	case err != nil:
		return ctrl.Result{}, err
	default:
		reason, message = testKeyRefusal(key, workspace)
	}
	if reason == "" {
		// The Workspace now permits the key, so an old refusal is not true now.
		if meta.RemoveStatusCondition(&key.Status.Conditions, "Active") {
			return ctrl.Result{}, r.Status().Update(ctx, key)
		}
		return ctrl.Result{}, nil
	}
	if meta.SetStatusCondition(&key.Status.Conditions, metav1.Condition{
		Type: "Active", Status: metav1.ConditionFalse, ObservedGeneration: key.Generation,
		Reason: reason, Message: message,
	}) {
		return ctrl.Result{}, r.Status().Update(ctx, key)
	}
	return ctrl.Result{}, nil
}

func testKeyRefusal(key *corev1alpha1.TestKey, workspace *corev1alpha1.Workspace) (reason, message string) {
	policy := workspace.Spec.TestKeys
	switch {
	case workspace.UID != key.Spec.WorkspaceRef.UID:
		return "WorkspaceUIDMismatch", "The Workspace UID differs from the reference."
	case workspace.Spec.Suspend || !workspace.DeletionTimestamp.IsZero():
		return "WorkspaceSuspended", "The Workspace is suspended or is being deleted."
	case policy == nil:
		return "TestKeysDisabled", "The Workspace does not permit test keys."
	case key.Spec.Budget.Cmp(policy.MaxBudget) > 0:
		return "BudgetLimitExceeded", "The test key budget exceeds the Workspace limit."
	case key.Spec.TTL.Duration > policy.MaxTTL.Duration:
		return "TTLLimitExceeded", "The test key TTL exceeds the Workspace limit."
	}
	return "", ""
}
