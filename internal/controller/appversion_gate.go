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

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/evidence"
)

// applyGate sets the condition Evidence of a gated version. It makes the gate Job after each pod
// of the version is Ready, reads the report of the Job and writes the evidence record one time.
// deploy is nil if the objects of the version are not equal to the template. A False condition
// and a record are terminal: a later Job of the same version does not change them.
func (r *AppVersionReconciler) applyGate(ctx context.Context, av *corev1alpha1.AppVersion,
	deploy *appsv1.Deployment) error {
	if av.Spec.Gate == nil {
		meta.RemoveStatusCondition(&av.Status.Conditions, corev1alpha1.AppVersionEvidence)
		return nil
	}
	set := func(s metav1.ConditionStatus, reason, msg string) {
		meta.SetStatusCondition(&av.Status.Conditions, metav1.Condition{Type: corev1alpha1.AppVersionEvidence,
			Status: s, Reason: reason, Message: msg, ObservedGeneration: av.Generation})
	}
	if rec := av.Status.Evidence; rec != nil {
		if rec.Verdict == corev1alpha1.EvidencePass {
			set(metav1.ConditionTrue, corev1alpha1.EvidencePassed, "The controller accepted a record with the verdict pass.")
		} else {
			set(metav1.ConditionFalse, corev1alpha1.EvidenceFailed, "The controller accepted a record with the verdict fail.")
		}
		return nil
	}
	if meta.IsStatusConditionFalse(av.Status.Conditions, corev1alpha1.AppVersionEvidence) {
		return nil
	}
	if deploy == nil {
		set(metav1.ConditionUnknown, corev1alpha1.EvidenceWaiting, "The objects of the version are not ready for the gate.")
		return nil
	}
	digest, pinned := evidence.ImageDigest(appContainer(&deploy.Spec.Template.Spec).Image)
	if !pinned {
		set(metav1.ConditionFalse, corev1alpha1.EvidenceImageNotPinned,
			"A gated version must name the image of the app container by a sha256 digest.")
		return nil
	}
	subject, target := evidence.AppVersionSubject(av.Namespace, av.Name, av.UID, digest), gateTarget(av, deploy)
	job := &batchv1.Job{}
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: av.Namespace, Name: gateName(av)}, job)
	switch {
	case apierrors.IsNotFound(err):
		pod := &deploy.Spec.Template.Spec
		policy := gatePolicy(av, r.Claim.claimPorts(pod, appContainer(pod).Ports))
		return r.startGate(ctx, av, policy, gateJob(av, target, subject), set)
	case err != nil:
		return err
	case !metav1.IsControlledBy(job, av):
		set(metav1.ConditionUnknown, corev1alpha1.EvidenceWaiting, "A Job with the name of the gate Job is not from this version.")
		return nil
	}
	for _, c := range job.Status.Conditions {
		switch {
		case c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue:
			set(metav1.ConditionFalse, corev1alpha1.EvidenceJobFailed, "The gate Job failed after its retries, or its deadline ended.")
			return nil
		case c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue:
			return r.checkGate(ctx, av, job, target, subject, set)
		}
	}
	set(metav1.ConditionUnknown, corev1alpha1.EvidenceRunning, "The gate Job runs.")
	return nil
}

// startGate makes the gate Job if each pod of the version is Ready. The NetworkPolicy of the gate
// pod is first, so that the pod does not start before its policy.
func (r *AppVersionReconciler) startGate(ctx context.Context, av *corev1alpha1.AppVersion,
	policy *networkingv1.NetworkPolicy, job *batchv1.Job, set func(metav1.ConditionStatus, string, string)) error {
	d := &appsv1.Deployment{}
	found, err := r.getOwned(ctx, av, d)
	if err != nil {
		return err
	}
	if n := d.Status; !found || d.Spec.Replicas == nil || *d.Spec.Replicas == 0 || n.ObservedGeneration < d.Generation ||
		n.Replicas != *d.Spec.Replicas || n.UpdatedReplicas != *d.Spec.Replicas || n.ReadyReplicas != *d.Spec.Replicas {
		set(metav1.ConditionUnknown, corev1alpha1.EvidenceWaiting, "Not all pods of the version are Ready.")
		return nil
	}
	err = r.applyGatePolicy(ctx, av, policy)
	if errors.Is(err, errNotOwned) {
		set(metav1.ConditionUnknown, corev1alpha1.EvidenceWaiting,
			"A NetworkPolicy with the name of the gate policy is not from this version.")
		return nil
	}
	if err == nil {
		err = r.Create(ctx, job)
	}
	switch {
	case permanent(err):
		set(metav1.ConditionFalse, corev1alpha1.EvidenceJobFailed, "The API server refused the gate Job or its NetworkPolicy.")
		return nil
	case err != nil:
		return err
	}
	set(metav1.ConditionUnknown, corev1alpha1.EvidenceRunning, "The gate Job runs.")
	return nil
}

// stopGate deletes the gate Job of a version in the revoke, with its pods. The AppVersion can
// stay after a revoke, so the garbage collector does not delete the Job. A gate with no end state
// gets the state Waiting, so that the status does not say that the Job runs.
func (r *AppVersionReconciler) stopGate(ctx context.Context, av *corev1alpha1.AppVersion) error {
	if av.Spec.Gate == nil {
		return nil
	}
	job := &batchv1.Job{}
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: av.Namespace, Name: gateName(av)}, job)
	if client.IgnoreNotFound(err) != nil {
		return err
	}
	if err == nil && metav1.IsControlledBy(job, av) {
		// The UID precondition stops the delete if the Job changed after the read.
		uid := job.UID
		if err := r.Delete(ctx, job, client.Preconditions{UID: &uid},
			client.PropagationPolicy(metav1.DeletePropagationBackground)); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	if c := meta.FindStatusCondition(av.Status.Conditions, corev1alpha1.AppVersionEvidence); c != nil &&
		c.Status == metav1.ConditionUnknown {
		meta.SetStatusCondition(&av.Status.Conditions, metav1.Condition{Type: corev1alpha1.AppVersionEvidence,
			Status: metav1.ConditionUnknown, Reason: corev1alpha1.EvidenceWaiting, ObservedGeneration: av.Generation,
			Message: "The version has no key, so its gate does not run."})
	}
	return nil
}

// checkGate reads the report of a gate Job that completed, and does the check of the design. It
// writes the record only for a report that the controller accepts. The messages have fixed text
// only, never text of the report.
func (r *AppVersionReconciler) checkGate(ctx context.Context, av *corev1alpha1.AppVersion, job *batchv1.Job,
	target, subject string, set func(metav1.ConditionStatus, string, string)) error {
	pods := &corev1.PodList{}
	if err := r.APIReader.List(ctx, pods, client.InNamespace(av.Namespace),
		client.MatchingLabels{batchv1.JobNameLabel: job.Name}); err != nil {
		return err
	}
	// A Job that completed has one pod that succeeded. With more than one report, the controller
	// cannot tell which one is the report of the gate, so it accepts none.
	var reports []string
	for i := range pods.Items {
		p := &pods.Items[i]
		if !metav1.IsControlledBy(p, job) || p.Status.Phase != corev1.PodSucceeded || !isGatePod(&p.Spec, av, target) {
			continue
		}
		for _, s := range p.Status.ContainerStatuses {
			if s.Name == gateVerifyContainer && s.State.Terminated != nil && s.State.Terminated.ExitCode == 0 {
				reports = append(reports, s.State.Terminated.Message)
			}
		}
	}
	msg := ""
	if len(reports) == 1 {
		msg = reports[0]
	}
	rep, err := evidence.ParseReport(msg)
	if err != nil {
		set(metav1.ConditionFalse, corev1alpha1.EvidenceNotVerified, "The report of the gate Job is not valid: "+err.Error()+".")
		return nil
	}
	reason, verdict := rep.Check(subject)
	logf.FromContext(ctx).Info("The evidence gate of the AppVersion ended", "reason", reason)
	switch reason {
	case corev1alpha1.EvidencePassed, corev1alpha1.EvidenceFailed:
		av.Status.Evidence = &corev1alpha1.EvidenceRecord{
			Source: corev1alpha1.EvidenceSourceNookkuGate, Subject: rep.Subject, Verdict: verdict, ID: rep.Gate,
			Digest: "sha256:" + rep.SHA256, Integrity: corev1alpha1.EvidenceSeal, NookkuVersion: rep.Nookku,
			CheckedAt: metav1.NewTime(r.now().UTC().Truncate(time.Second)),
		}
		return r.applyGate(ctx, av, nil)
	case corev1alpha1.EvidenceSubjectMismatch:
		set(metav1.ConditionFalse, reason, "The subject of the report is not the subject of this version.")
	default:
		set(metav1.ConditionFalse, reason,
			"The gate result cannot be used, or its verdict does not agree with the exit code of the verify step.")
	}
	return nil
}
