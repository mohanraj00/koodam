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
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// AgentSpec is what the agent needs. The controller makes a gateway key with a budget and a TTL,
// a ServiceAccount with a Role, and then a Job. When the Job ends or the Agent is deleted, the
// controller revokes all of it and writes the spend to the status.
// +kubebuilder:validation:XValidation:rule="!has(self.podTemplate) || !has(self.podTemplate.spec) || !has(self.podTemplate.spec.containers) || !self.podTemplate.spec.containers.exists(c, c.name == 'agent' && (has(c.image) || has(c.command)))",message="Set the image and the command in spec.image and spec.command, not in the pod template."
type AgentSpec struct {
	// Image is the agent container image.
	// +required
	Image string `json:"image"`

	// Command is the entrypoint of the agent container.
	// +required
	// +kubebuilder:validation:MinItems=1
	Command []string `json:"command"`

	// Env is added to the agent container, after the credentials that the controller supplies.
	// +optional
	Env map[string]string `json:"env,omitempty"`

	// Model is the gateway model name. The key permits only this model.
	// +required
	// +kubebuilder:validation:MinLength=1
	Model string `json:"model"`

	// Budget is the maximum spend of the run, in the currency of the gateway price table.
	// The gateway refuses calls after the budget.
	// +required
	Budget resource.Quantity `json:"budget"`

	// User is the person or the team that owns the run. It goes into the key metadata.
	// +optional
	User string `json:"user,omitempty"`

	// DeadlineSeconds stops the Job. The key TTL is this value plus a grace period.
	// +optional
	// +kubebuilder:default=900
	// +kubebuilder:validation:Minimum=1
	DeadlineSeconds int64 `json:"deadlineSeconds,omitempty"`

	// Rules are the RBAC rules of the run Role, in the namespace of the Agent only.
	// +optional
	Rules []rbacv1.PolicyRule `json:"rules,omitempty"`

	// Harness installs a coding harness binary in the pod before the agent starts. The binary is
	// in /koodam/bin, which is first on the PATH of the agent container. If Env has a PATH, the
	// controller does not change it, so that PATH must contain /koodam/bin.
	// +optional
	Harness *HarnessSpec `json:"harness,omitempty"`

	// PodTemplate changes the run pod, for example its resources, runtime class or node selector.
	// The main container has the name "agent". It cannot set image or command, because the
	// Agent spec sets them.
	// +optional
	PodTemplate *PodTemplate `json:"podTemplate,omitempty"`
}

// HarnessSpec selects the harness binary. An init container downloads it from the vendor and
// checks its sha256. Koodam does not keep or redistribute a harness binary.
type HarnessSpec struct {
	// Name is the harness. The binary is claude for claude-code and codex for codex.
	// +required
	// +kubebuilder:validation:Enum=claude-code;codex
	Name string `json:"name"`

	// Version pins the harness version. If it is empty, the pod gets the latest version.
	// +optional
	// +kubebuilder:validation:Pattern=`^([0-9]+\.[0-9]+\.[0-9]+)?$`
	Version string `json:"version,omitempty"`

	// Libc is the C library of the agent image. It selects the Claude Code build. The Codex
	// build is static.
	// +optional
	// +kubebuilder:default=glibc
	// +kubebuilder:validation:Enum=glibc;musl
	Libc string `json:"libc,omitempty"`
}

// AgentPhase is the lifecycle phase of an Agent.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
type AgentPhase string

const (
	AgentPending   AgentPhase = "Pending"
	AgentRunning   AgentPhase = "Running"
	AgentSucceeded AgentPhase = "Succeeded"
	AgentFailed    AgentPhase = "Failed"
)

// AgentUsage is the gateway count for the key of the run, from successful calls only.
type AgentUsage struct {
	// Spend is the total spend of the key, as a decimal string.
	Spend string `json:"spend,omitempty"`
	// Calls is the count of successful calls.
	Calls int64 `json:"calls,omitempty"`
	// FailedCalls is the count of refused or failed calls.
	FailedCalls int64 `json:"failedCalls,omitempty"`
	// InputTokens and OutputTokens are the sums over the successful calls.
	InputTokens  int64 `json:"inputTokens,omitempty"`
	OutputTokens int64 `json:"outputTokens,omitempty"`
}

// AgentStatus is the observed state of an Agent.
type AgentStatus struct {
	// Phase is the lifecycle phase.
	// +optional
	Phase AgentPhase `json:"phase,omitempty"`

	// Job is the name of the run Job.
	// +optional
	Job string `json:"job,omitempty"`

	// KeyAlias identifies the gateway key. The key value is never in the status.
	// +optional
	KeyAlias string `json:"keyAlias,omitempty"`

	// KeyHash is the SHA-256 hash of the key. The controller writes it before it makes the key,
	// and uses it to block the key and to read the usage. The gateway refuses the hash for
	// model calls, so it is not a credential.
	// +optional
	KeyHash string `json:"keyHash,omitempty"`

	// StartedAt is the start time of the run Job. The controller sets it one time only.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt is the end time of the run Job. For a failed Job, it is the time of the Failed
	// condition. The controller sets it one time only.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// RevokedAt is the time when the controller blocked the key and deleted the credentials.
	// +optional
	RevokedAt *metav1.Time `json:"revokedAt,omitempty"`

	// Usage is the gateway count after the revoke.
	// +optional
	Usage *AgentUsage `json:"usage,omitempty"`

	// Conditions: Provisioned, Revoked.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.model`
// +kubebuilder:printcolumn:name="Harness",type=string,JSONPath=`.spec.harness.name`
// +kubebuilder:printcolumn:name="Budget",type=string,JSONPath=`.spec.budget`
// +kubebuilder:printcolumn:name="Spend",type=string,JSONPath=`.status.usage.spend`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Agent is one agent run with its own credentials.
type Agent struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Agent
	// +required
	Spec AgentSpec `json:"spec"`

	// status defines the observed state of Agent
	// +optional
	Status AgentStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AgentList contains a list of Agent
type AgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Agent `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Agent{}, &AgentList{})
		return nil
	})
}
