/*
Copyright 2026.

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
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/gateway"
)

// A quarantine blocks the gateway key of a workload and keeps its pods for an investigation. The
// egress proxy refuses each call of the pods. The controller adds quarantineFinalizer, so a delete
// of the workload waits for the release. The release changes the quarantine to a stop.

// quarantined is true if the status of the workload records a complete quarantine.
func quarantined(conds []metav1.Condition) bool {
	c := meta.FindStatusCondition(conds, corev1alpha1.HoldHeld)
	return c != nil && c.Status == metav1.ConditionTrue && c.Reason == reasonQuarantined
}

// agentQuarantined is true if the key of the run is blocked and its Job has no deadline, or if the
// run ended and its revoke is complete.
func agentQuarantined(a *corev1alpha1.Agent) bool {
	return quarantined(a.Status.Conditions) || agentStopped(a)
}

// workspaceQuarantined is true if the key of the Workspace is blocked and its pod stays.
func workspaceQuarantined(ws *corev1alpha1.Workspace) bool {
	c := meta.FindStatusCondition(ws.Status.Conditions, condReady)
	return quarantined(ws.Status.Conditions) && c != nil && c.Reason == reasonQuarantined
}

// appVersionQuarantined is true if the key of the version is blocked and its pods stay, or if
// its revoke is complete.
func appVersionQuarantined(av *corev1alpha1.AppVersion) bool {
	return quarantined(av.Status.Conditions) || appVersionStopped(av)
}

// applyQuarantine applies a Quarantine Hold to the run, or the release of one. It returns
// handled=false if no quarantine applies, and the reconcile continues.
func (r *AgentReconciler) applyQuarantine(ctx context.Context, a *corev1alpha1.Agent) (handled bool, err error) {
	ended := a.Status.Phase == corev1alpha1.AgentSucceeded || a.Status.Phase == corev1alpha1.AgentFailed
	held := controllerutil.ContainsFinalizer(a, quarantineFinalizer)
	if ended && !held {
		return false, nil
	}
	h, err := quarantineHold(ctx, r.Client, corev1alpha1.HoldTargetAgent, a.Namespace, a.Name, "")
	if err != nil {
		return true, err
	}
	switch {
	case h != nil && !ended:
		// The quarantine comes before the delete, the end and the Stop Holds, so that no revoke
		// deletes the Job.
		return true, r.quarantine(ctx, a, h)
	case !held:
		return false, nil
	}
	// The release: the run stops, as with a Stop Hold. The revoke deletes the Job.
	if !ended {
		meta.RemoveStatusCondition(&a.Status.Conditions, corev1alpha1.HoldHeld)
		if err := r.fail(ctx, a, corev1alpha1.AgentFailed, reasonStopped); err != nil {
			return true, err
		}
	} else if controllerutil.ContainsFinalizer(a, finalizer) {
		if err := r.revoke(ctx, a); err != nil {
			return true, err
		}
	}
	controllerutil.RemoveFinalizer(a, quarantineFinalizer)
	logf.FromContext(ctx).Info("Stopped the run after the release of the quarantine")
	return true, r.Update(ctx, a)
}

// quarantine adds the finalizer, blocks the key and removes the deadline of the Job. Then the
// status records the quarantine. A run that waits does not start.
func (r *AgentReconciler) quarantine(ctx context.Context, a *corev1alpha1.Agent, h *corev1alpha1.Hold) error {
	// Kubernetes refuses a new finalizer after the delete starts. Then the finalizer of the run
	// keeps the Agent and its Job until the release.
	deleting := !a.DeletionTimestamp.IsZero()
	if !deleting && controllerutil.AddFinalizer(a, quarantineFinalizer) {
		if err := r.Update(ctx, a); err != nil {
			return err
		}
		quarantineEvent(r.Recorder, a, h)
	}
	if quarantined(a.Status.Conditions) {
		return nil
	}
	if deleting {
		quarantineEvent(r.Recorder, a, h)
	}
	if a.Status.KeyHash != "" {
		if err := r.Gateway.BlockKey(ctx, a.Status.KeyHash); err != nil {
			return err
		}
	}
	// The Job controller stops the pods at the deadline. The pods must stay. A Job can exist
	// before the status shows Running, so the phase does not matter.
	job := &batchv1.Job{}
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: runName(a)}, job)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil && metav1.IsControlledBy(job, a) && job.Spec.ActiveDeadlineSeconds != nil {
		job.Spec.ActiveDeadlineSeconds = nil
		if err := r.Update(ctx, job); err != nil {
			return err
		}
	}
	meta.SetStatusCondition(&a.Status.Conditions, metav1.Condition{Type: corev1alpha1.HoldHeld,
		Status: metav1.ConditionTrue, Reason: reasonQuarantined, ObservedGeneration: a.Generation,
		Message: fmt.Sprintf("The Hold %s quarantines the run. The key is blocked, and the pods stay.", h.Name)})
	return r.Status().Update(ctx, a)
}

// applyQuarantine sets the condition Held from the Quarantine Holds. While a quarantine is
// active, Held has the reason Quarantined: the key is blocked, and the pod and the home PVC stay.
// After the release, Held has the reason Stopped until the pod is stopped. Then the controller
// removes the finalizer, and the Workspace starts again with a new key and a new pod. It returns
// done=false if no quarantine applies.
func (r *WorkspaceReconciler) applyQuarantine(ctx context.Context, ws *corev1alpha1.Workspace) (done bool,
	err error) {
	h, err := quarantineHold(ctx, r.Client, corev1alpha1.HoldTargetWorkspace, ws.Namespace, ws.Name, "")
	if err != nil {
		return true, err
	}
	if h != nil {
		if controllerutil.AddFinalizer(ws, quarantineFinalizer) {
			// The status of the response is the status of the read, because the reconcile did not
			// change it yet.
			if err := r.Update(ctx, ws); err != nil {
				return true, err
			}
			quarantineEvent(r.Recorder, ws, h)
		}
		meta.SetStatusCondition(&ws.Status.Conditions, metav1.Condition{Type: corev1alpha1.HoldHeld,
			Status: metav1.ConditionTrue, Reason: reasonQuarantined, ObservedGeneration: ws.Generation,
			Message: fmt.Sprintf("The Hold %s quarantines the Workspace.", h.Name)})
		return true, nil
	}
	if !controllerutil.ContainsFinalizer(ws, quarantineFinalizer) {
		return false, nil
	}
	if workspaceStopped(ws) && !quarantined(ws.Status.Conditions) {
		// The status of the last reconcile shows the stop after the release. The StatefulSet can
		// show 0 replicas before its pod stops. The quarantined pod must stop before a new pod starts.
		left, err := r.workspacePodLeft(ctx, ws)
		if err != nil {
			return true, err
		}
		if left {
			return true, nil
		}
		controllerutil.RemoveFinalizer(ws, quarantineFinalizer)
		if err := r.Update(ctx, ws); err != nil {
			return true, err
		}
		logf.FromContext(ctx).Info("Stopped the Workspace after the release of the quarantine")
		return false, nil
	}
	meta.SetStatusCondition(&ws.Status.Conditions, metav1.Condition{Type: corev1alpha1.HoldHeld,
		Status: metav1.ConditionTrue, Reason: reasonStopped, ObservedGeneration: ws.Generation,
		Message: "The release of the quarantine stops the pod. Then the Workspace starts again with a new key."})
	return true, nil
}

// podGoneRecheck is the time between the reads of a Workspace pod after the release of a
// quarantine.
const podGoneRecheck = 2 * time.Second

// workspacePodLeft is true while a pod of the Workspace exists, also a pod that stops. It reads
// the API server, because the controller has no cache of the pods.
func (r *WorkspaceReconciler) workspacePodLeft(ctx context.Context, ws *corev1alpha1.Workspace) (bool, error) {
	pods := &corev1.PodList{}
	if err := r.APIReader.List(ctx, pods, client.InNamespace(ws.Namespace),
		client.MatchingLabels{WorkspaceLabel: ws.Name}); err != nil {
		return false, err
	}
	return len(pods.Items) > 0, nil
}

// releasingQuarantine is true while the release of a quarantine waits for the stop of the pod.
func releasingQuarantine(ws *corev1alpha1.Workspace) bool {
	c := meta.FindStatusCondition(ws.Status.Conditions, corev1alpha1.HoldHeld)
	return controllerutil.ContainsFinalizer(ws, quarantineFinalizer) && c != nil && c.Reason == reasonStopped
}

// quarantineHeld blocks the key of a quarantined Workspace and keeps its pod. The TestKey
// controller blocks the test keys, because it reads Held=True.
func (r *WorkspaceReconciler) quarantineHeld(ctx context.Context, ws *corev1alpha1.Workspace) (
	corev1alpha1.WorkspacePhase, string, string, error) {
	secret, err := r.readKey(ctx, ws)
	if err != nil && !errors.Is(err, errNotOwned) {
		return "", "", "", err
	}
	if err := r.revokeKey(ctx, ws, secret); err != nil {
		return "", "", "", err
	}
	switch err := r.waitTestKeyBlocks(ctx, ws); {
	case errors.Is(err, errTestKeysUnblocked):
		return corev1alpha1.WorkspacePending, reasonTestKeysBlocking, "The quarantine waits for the block of each test key.",
			nil
	case err != nil:
		return "", "", "", err
	}
	return corev1alpha1.WorkspacePending, reasonQuarantined,
		"A Hold quarantines the pod. The key is blocked, and the egress proxy refuses each call. " +
			"The pod and the home PVC stay.", nil
}

// releaseDeleted removes the quarantine finalizer of a deleted Workspace after the release.
func (r *WorkspaceReconciler) releaseDeleted(ctx context.Context, ws *corev1alpha1.Workspace) error {
	if !controllerutil.ContainsFinalizer(ws, quarantineFinalizer) {
		return nil
	}
	h, err := quarantineHold(ctx, r.Client, corev1alpha1.HoldTargetWorkspace, ws.Namespace, ws.Name, "")
	if h != nil || err != nil {
		return err
	}
	controllerutil.RemoveFinalizer(ws, quarantineFinalizer)
	return r.Update(ctx, ws)
}

// applyQuarantine applies a Quarantine Hold to the version, or the release of one. The release
// revokes the version with the reason Stopped, as a Stop Hold does. It returns handled=false if no
// quarantine applies.
func (r *AppVersionReconciler) applyQuarantine(ctx context.Context, av *corev1alpha1.AppVersion) (handled bool,
	err error) {
	revoking := meta.FindStatusCondition(av.Status.Conditions, corev1alpha1.AppVersionRevoking) != nil
	held := controllerutil.ContainsFinalizer(av, quarantineFinalizer)
	if revoking && !held {
		return false, nil
	}
	h, err := quarantineHold(ctx, r.Client, corev1alpha1.HoldTargetAppVersion, av.Namespace, av.Name,
		av.Spec.AppRef.Name)
	if err != nil {
		return true, err
	}
	switch {
	case h != nil && !revoking:
		// The quarantine comes before the delete, the retire and the Stop Holds, so that no revoke
		// deletes the pods.
		return true, r.quarantine(ctx, av, h)
	case !held:
		return false, nil
	}
	meta.RemoveStatusCondition(&av.Status.Conditions, corev1alpha1.HoldHeld)
	if err := r.revoke(ctx, av, reasonStopped); err != nil {
		return true, err
	}
	controllerutil.RemoveFinalizer(av, quarantineFinalizer)
	logf.FromContext(ctx).Info("Revoked the version after the release of the quarantine")
	return true, r.Update(ctx, av)
}

// quarantine adds the finalizer and blocks the key. The pods stay, and the controller does not
// renew the key. Then the status records the quarantine.
func (r *AppVersionReconciler) quarantine(ctx context.Context, av *corev1alpha1.AppVersion,
	h *corev1alpha1.Hold) error {
	// Kubernetes refuses a new finalizer after the delete starts. Then the finalizer of the version
	// keeps it and its pods until the release.
	deleting := !av.DeletionTimestamp.IsZero()
	if !deleting && controllerutil.AddFinalizer(av, quarantineFinalizer) {
		if err := r.Update(ctx, av); err != nil {
			return err
		}
		quarantineEvent(r.Recorder, av, h)
	}
	if quarantined(av.Status.Conditions) {
		// The key is blocked, and the controller does not renew it during the quarantine.
		return nil
	}
	if deleting {
		quarantineEvent(r.Recorder, av, h)
	}
	secret, err := r.readSecret(ctx, av)
	if err != nil && !errors.Is(err, errNotOwned) {
		return err
	}
	hash := av.Status.KeyHash
	if hash == "" && secret != nil {
		// The Secret exists, but the status write before CreateKey did not complete.
		hash = gateway.HashKey(string(secret.Data[appKeyField]))
	}
	if hash != "" {
		if err := r.Gateway.BlockKey(ctx, hash); err != nil {
			return err
		}
		r.forgetRenewed(hash)
	}
	meta.SetStatusCondition(&av.Status.Conditions, metav1.Condition{Type: corev1alpha1.HoldHeld,
		Status: metav1.ConditionTrue, Reason: reasonQuarantined, ObservedGeneration: av.Generation,
		Message: fmt.Sprintf("The Hold %s quarantines the version. The key is blocked, and the pods stay.", h.Name)})
	return r.Status().Update(ctx, av)
}
