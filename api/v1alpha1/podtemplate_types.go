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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
)

// PodTemplate changes the pod that Koodam makes for a Workspace or an Agent. The controller starts
// from this template and puts the Koodam parts on top. The main container has the name
// "workspace" or "agent". On the main container, the template can set image, command, args,
// workingDir, env, envFrom, resources and volumeMounts. Koodam owns the pod security fields,
// the service account and the names of its own volumes and containers. The API server refuses
// the pod fields below. The controller refuses the other conflicts with the reason
// InvalidPodTemplate: the Workspace gets Ready=False and the Agent gets Provisioned=False. The
// controller does not change a Workspace pod that exists. The user containers cannot set a
// securityContext or a hostPort, and the volumes cannot use a hostPath. The template cannot refer
// to a Secret with the name prefix "kd-", because these Secrets hold the gateway keys of the runs.
// +kubebuilder:validation:XValidation:rule="!has(self.spec) || ((!has(self.spec.hostNetwork) || !self.spec.hostNetwork) && (!has(self.spec.hostPID) || !self.spec.hostPID) && (!has(self.spec.hostIPC) || !self.spec.hostIPC) && (!has(self.spec.hostUsers) || !self.spec.hostUsers) && (!has(self.spec.shareProcessNamespace) || !self.spec.shareProcessNamespace))",message="The pod template cannot use the host network, PID, IPC or users, or share the process namespace."
// +kubebuilder:validation:XValidation:rule="!has(self.spec) || !(has(self.spec.serviceAccountName) || has(self.spec.serviceAccount) || has(self.spec.automountServiceAccountToken) || has(self.spec.enableServiceLinks) || has(self.spec.securityContext) || has(self.spec.restartPolicy) || has(self.spec.activeDeadlineSeconds) || has(self.spec.ephemeralContainers))",message="Koodam owns serviceAccountName, serviceAccount, automountServiceAccountToken, enableServiceLinks, securityContext, restartPolicy, activeDeadlineSeconds and ephemeralContainers."
type PodTemplate struct {
	// Metadata has the labels and the annotations of the pod.
	// +optional
	Metadata PodTemplateMetadata `json:"metadata,omitzero"`

	// Spec is the pod spec that Koodam merges with its own pod. The containers list is optional.
	// +optional
	Spec corev1.PodSpec `json:"spec,omitzero"`
}

// PodTemplateMetadata is the part of the pod metadata that a user can set. The prefix koodam.dev/
// is reserved for Koodam.
type PodTemplateMetadata struct {
	// Labels are added to the pod labels.
	// +optional
	// +kubebuilder:validation:XValidation:rule="self.all(k, !k.matches('^([^/]*\\\\.)?koodam\\\\.dev/'))",message="The prefixes koodam.dev/ and *.koodam.dev/ are reserved."
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations are added to the pod annotations.
	// +optional
	// +kubebuilder:validation:XValidation:rule="self.all(k, !k.matches('^([^/]*\\\\.)?koodam\\\\.dev/'))",message="The prefixes koodam.dev/ and *.koodam.dev/ are reserved."
	Annotations map[string]string `json:"annotations,omitempty"`
}
