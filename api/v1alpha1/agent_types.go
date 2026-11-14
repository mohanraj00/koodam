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
// +kubebuilder:validation:XValidation:rule="has(self.ingressPort) == has(oldSelf.ingressPort) && (!has(self.ingressPort) || self.ingressPort == oldSelf.ingressPort)",message="spec.ingressPort is immutable. You cannot add, remove or change it after create."
// +kubebuilder:validation:XValidation:rule="!has(self.harness) || !has(self.harness.plugins) || !('nookku' in self.harness.plugins) || (has(self.podTemplate) && has(self.podTemplate.spec) && has(self.podTemplate.spec.volumes) && self.podTemplate.spec.volumes.exists(v, v.name == 'nookku-tests' && has(v.persistentVolumeClaim) && size(v.persistentVolumeClaim.claimName) > 0))",message="The nookku plugin needs the volume nookku-tests in spec.podTemplate.spec.volumes, with a persistentVolumeClaim and a claimName."
// +kubebuilder:validation:XValidation:rule="has(self.model) != has(self.models)",message="Set model or models, and not the two."
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

	// Model is the gateway model name. It is the short form of a models list with 1 item. The
	// key permits only this model.
	// +optional
	// +kubebuilder:validation:MinLength=1
	Model string `json:"model,omitempty"`

	// Models are the gateway model names that the key permits. The gateway refuses a call to a
	// different model. The first model is the model of the harness. Set model or models, and
	// not the two. The list is atomic, so an apply replaces it and keeps its order.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:XValidation:rule="self.all(m, self.exists_one(n, n == m))",message="models must not have a model two times"
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=253
	Models []string `json:"models,omitempty"`

	// Budget is the maximum spend of the run, in the currency of the gateway price table.
	// The gateway refuses calls after the budget.
	// +required
	Budget resource.Quantity `json:"budget"`

	// User is the person or the team that owns the run. It goes into the key metadata.
	// +optional
	User string `json:"user,omitempty"`

	// Output is the one output file of the run. When the run ends, the status shows the
	// SHA-256 digest and the size of this file. The file must be in a volume of spec.podTemplate
	// that the agent container mounts. A change after the run starts has no effect: the status
	// shows the path that the run used.
	// +optional
	Output *AgentOutput `json:"output,omitempty"`

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

	// IngressPort is the TCP port in the agent container for the traced ingress. If it is empty,
	// the Agent has no ingress. You cannot add, remove or change it after create. The default Agent
	// CRD has the ingress feature disabled and refuses this field.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	IngressPort *int32 `json:"ingressPort,omitempty"`
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

	// Plugins are the harness presets. The only value is nookku. At this stage, Koodam installs
	// no Nookku file. It gives only the Nookku test folder to the pod. For an Agent, nookku needs
	// a pod template volume with the name nookku-tests and a persistentVolumeClaim with a
	// claimName. The user owns that PVC. The API server does not look up the PVC. The controller
	// mounts the volume at /koodam/nookku-tests and sets NOOKKU_TESTS_DIR. For a Workspace, the
	// image makes /home/dev/nookku-tests on the home PVC and exports NOOKKU_TESTS_DIR in a login
	// shell. MaxItems is equal to the count of values in the enum. Make it larger
	// when you add a value.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=1
	Plugins []HarnessPlugin `json:"plugins,omitempty"`
}

// HarnessPlugin is a harness preset.
// +kubebuilder:validation:Enum=nookku
type HarnessPlugin string

// HarnessPluginNookku is the Nookku test preset.
const HarnessPluginNookku HarnessPlugin = "nookku"

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

// ModelList gives back the models that the key permits. The first model is the model of the
// harness.
func (s *AgentSpec) ModelList() []string {
	if s.Model != "" {
		return []string{s.Model}
	}
	return s.Models
}

// AgentOutput is the output file of an Agent run.
type AgentOutput struct {
	// Path is the absolute path of the file in the agent container. It has no "." part and no
	// ".." part.
	// +required
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:XValidation:rule="self.startsWith('/') && !self.endsWith('/') && !self.split('/').exists(p, p == '.' || p == '..')",message="path must be an absolute file path with no . part and no .. part."
	Path string `json:"path"`
}

// AgentOutputStatus is the digest of the output file of a run. It never has the content of the
// file.
type AgentOutputStatus struct {
	// Path is the path of the file that the run used. It is the spec.output.path at the start of
	// the run.
	// +optional
	Path string `json:"path,omitempty"`

	// SHA256 is the SHA-256 digest of the file, as 64 lower-case hex characters.
	// +optional
	SHA256 string `json:"sha256,omitempty"`

	// Size is the size of the file in bytes.
	// +optional
	Size *int64 `json:"size,omitempty"`

	// Reason tells why the run has no digest: NotFound, NotRegularFile, ReadError or NoResult.
	// It is empty if the status has a digest.
	// +optional
	Reason string `json:"reason,omitempty"`
}

// AgentStatus is the observed state of an Agent.
type AgentStatus struct {
	// RunID is the identity of the run. It is the UID of the Agent, so it is the same after a
	// controller restart and for each pod of the run. An external system can record it with
	// the output digest.
	// +optional
	RunID string `json:"runID,omitempty"`

	// Output is the digest of the file of spec.output. The controller writes it one time, when
	// the run ends.
	// +optional
	Output *AgentOutputStatus `json:"output,omitempty"`

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

	// Conditions: Provisioned, Revoked, TracedEgress.
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
