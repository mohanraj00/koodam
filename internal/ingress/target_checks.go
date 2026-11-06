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

package ingress

import (
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/agentingress"
)

// The functions in this file check one object that the resolver read. They make no API call and
// they do not change the object. Each refusal is ErrTargetAbsent or ErrTargetUnavailable.

// checkAgent checks the Agent a of the route namespace and name. It returns the ingress port and
// the name of the run Job in a.status.job. An Agent with no spec.ingressPort is absent. The Agent
// must have a UID, no deletion, the Running phase and valid ingress fields. A Running opted-in
// Agent with an empty status.job is not ready, thus not available. The Agent name and the Job name
// must be valid label values, because the pod selector uses them.
func checkAgent(a *corev1alpha1.Agent, namespace, name string) (int32, string, error) {
	if a.Namespace != namespace || a.Name != name {
		return 0, "", ErrTargetUnavailable
	}
	if a.Spec.IngressPort == nil {
		return 0, "", ErrTargetAbsent
	}
	if a.UID == "" || a.DeletionTimestamp != nil || a.Status.Phase != corev1alpha1.AgentRunning ||
		agentingress.Check(a.Spec) != nil || len(validation.IsValidLabelValue(a.Name)) > 0 {
		return 0, "", ErrTargetUnavailable
	}
	job := a.Status.Job
	if job == "" || len(apivalidation.NameIsDNSSubdomain(job, false)) > 0 ||
		len(validation.IsValidLabelValue(job)) > 0 {
		return 0, "", ErrTargetUnavailable
	}
	return *a.Spec.IngressPort, job, nil
}

// checkJob checks the run Job j of the partial target t. j must be the Job t.job in t.namespace,
// with a UID and no deletion. Its sole controller owner must be the Agent of t, with the exact API
// version, kind, name and UID. A Job with a true JobComplete or JobFailed condition has ended.
func checkJob(j *batchv1.Job, t target) error {
	if j.Namespace != t.namespace || j.Name != t.job || j.UID == "" || j.DeletionTimestamp != nil {
		return ErrTargetUnavailable
	}
	if !controlledBy(j.OwnerReferences, corev1alpha1.GroupVersion.String(), agentKind, t.agent, t.agentUID) {
		return ErrTargetUnavailable
	}
	for _, c := range j.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			return ErrTargetUnavailable
		}
	}
	return nil
}

// eligiblePod is true if p can be the run pod of the partial target t. p must be in t.namespace,
// with a UID, no deletion and the two reserved labels of t. Its sole controller owner must be the
// Job of t, with the exact API version, kind, name and UID. It must be Running and Ready. A pod
// that is not eligible never becomes a target.
func eligiblePod(p *corev1.Pod, t target) bool {
	if p.Namespace != t.namespace || p.Name == "" || p.UID == "" || p.DeletionTimestamp != nil {
		return false
	}
	if p.Labels[targetAgentLabel] != t.agent || p.Labels[targetRunLabel] != t.job {
		return false
	}
	if !controlledBy(p.OwnerReferences, batchv1.SchemeGroupVersion.String(), jobKind, t.job, t.jobUID) {
		return false
	}
	return p.Status.Phase == corev1.PodRunning && podReady(p.Status.Conditions)
}

// podReady is true if conds has a Ready condition and each Ready condition is true.
func podReady(conds []corev1.PodCondition) bool {
	ready := false
	for _, c := range conds {
		if c.Type != corev1.PodReady {
			continue
		}
		if c.Status != corev1.ConditionTrue {
			return false
		}
		ready = true
	}
	return ready
}

// controlledBy is true if refs has exactly one controller reference, and if it has the exact API
// version, kind, name and UID. uid must not be empty.
func controlledBy(refs []metav1.OwnerReference, apiVersion, kind, name string, uid types.UID) bool {
	var owner *metav1.OwnerReference
	for i := range refs {
		if refs[i].Controller == nil || !*refs[i].Controller {
			continue
		}
		if owner != nil {
			return false
		}
		owner = &refs[i]
	}
	return owner != nil && uid != "" && owner.APIVersion == apiVersion && owner.Kind == kind &&
		owner.Name == name && owner.UID == uid
}
