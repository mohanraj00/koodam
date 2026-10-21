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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// WorkspaceSpec is the long-lived developer pod. The developer writes it. The controller makes
// 1 pod, 1 PVC for the home and 1 Secret with the SSH host key.
type WorkspaceSpec struct {
	// SSHPublicKeys are the public keys that can log in to the pod with SSH. Each item is one
	// line in the authorized_keys format: the key type, the key data and an optional comment.
	// The API server refuses a private key and a line with a newline.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=8192
	// +kubebuilder:validation:items:Pattern=`^(ssh-ed25519|ecdsa-sha2-nistp256|ecdsa-sha2-nistp384|ecdsa-sha2-nistp521|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com|ssh-rsa) [A-Za-z0-9+/]+={0,2}( [^\r\n]*)?$`
	SSHPublicKeys []string `json:"sshPublicKeys,omitempty"`

	// Harnesses are the coding harnesses in the pod. Each name occurs one time only.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=4
	Harnesses []HarnessSpec `json:"harnesses,omitempty"`

	// Image is the workspace container image. If it is empty, the pod uses the default
	// workspace image of the chart.
	// +optional
	Image string `json:"image,omitempty"`

	// Storage is the size of the PVC for the home.
	// +optional
	// +kubebuilder:default="20Gi"
	Storage resource.Quantity `json:"storage,omitzero"`

	// Suspend stops the pod and blocks the gateway key. The PVC stays.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// Gateway gives the pod a gateway key. If it is empty, the pod gets no gateway key and the
	// harnesses use the subscription login only.
	// +optional
	Gateway *WorkspaceGateway `json:"gateway,omitempty"`
}

// WorkspaceGateway is the gateway key of a Workspace.
type WorkspaceGateway struct {
	// Model is the gateway model name. The key permits only this model.
	// +required
	// +kubebuilder:validation:MinLength=1
	Model string `json:"model"`

	// BudgetPerDay is the maximum spend in one day, in the currency of the gateway price
	// table. The gateway refuses calls after the budget.
	// +required
	BudgetPerDay resource.Quantity `json:"budgetPerDay"`
}

// WorkspacePhase is the lifecycle phase of a Workspace.
// +kubebuilder:validation:Enum=Pending;Running;Suspended;Failed
type WorkspacePhase string

const (
	WorkspacePending   WorkspacePhase = "Pending"
	WorkspaceRunning   WorkspacePhase = "Running"
	WorkspaceSuspended WorkspacePhase = "Suspended"
	WorkspaceFailed    WorkspacePhase = "Failed"
)

// UsageSource tells where a usage count comes from.
// +kubebuilder:validation:Enum=gateway;projected
type UsageSource string

const (
	// UsageFromGateway is a count that the gateway gave for the key.
	UsageFromGateway UsageSource = "gateway"
	// UsageProjected is a count that the controller calculated without the gateway.
	UsageProjected UsageSource = "projected"
)

// WorkspaceUsage is the count for the gateway key of the Workspace.
type WorkspaceUsage struct {
	// Spend is the total spend of the key, as a decimal string.
	// +optional
	Spend string `json:"spend,omitempty"`
	// Calls is the count of successful calls.
	// +optional
	Calls int64 `json:"calls,omitempty"`
	// InputTokens is the sum of the input tokens over the successful calls.
	// +optional
	InputTokens int64 `json:"inputTokens,omitempty"`
	// OutputTokens is the sum of the output tokens over the successful calls.
	// +optional
	OutputTokens int64 `json:"outputTokens,omitempty"`
	// Source tells where the count comes from.
	// +optional
	Source UsageSource `json:"source,omitempty"`
}

// WorkspaceStatus is the observed state of a Workspace.
type WorkspaceStatus struct {
	// Phase is the lifecycle phase.
	// +optional
	Phase WorkspacePhase `json:"phase,omitempty"`

	// Pod is the name of the workspace pod.
	// +optional
	Pod string `json:"pod,omitempty"`

	// HostKey is the public SSH host key of the pod, for known_hosts. The private host key is
	// only in the Secret.
	// +optional
	HostKey string `json:"hostKey,omitempty"`

	// KeyAlias identifies the gateway key. The key value is never in the status.
	// +optional
	KeyAlias string `json:"keyAlias,omitempty"`

	// KeyHash is the SHA-256 hash of the gateway key. The gateway refuses the hash for model
	// calls, so it is not a credential.
	// +optional
	KeyHash string `json:"keyHash,omitempty"`

	// Usage is the count for the gateway key.
	// +optional
	Usage *WorkspaceUsage `json:"usage,omitempty"`

	// Conditions are the conditions of the Workspace.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Pod",type=string,JSONPath=`.status.pod`
// +kubebuilder:printcolumn:name="Suspend",type=boolean,JSONPath=`.spec.suspend`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Workspace is a long-lived developer pod with SSH access, a home PVC and an optional gateway
// key.
type Workspace struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Workspace
	// +required
	Spec WorkspaceSpec `json:"spec"`

	// status defines the observed state of Workspace
	// +optional
	Status WorkspaceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// WorkspaceList contains a list of Workspace
type WorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Workspace `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Workspace{}, &WorkspaceList{})
		return nil
	})
}
