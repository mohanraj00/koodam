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

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ProfileLabel is the namespace label that binds a namespace to a Profile. The value is the
// Profile name.
const ProfileLabel = "koodam.dev/profile"

// CostGroupLabel is the label that declares the cost group of a key.
const CostGroupLabel = "koodam.dev/cost-group"

// ProfileSpec lists the namespaces and the cost groups that the Profile permits. An admin writes
// it. The Profile holds no credential, no login and no file. The key set of spec.approval has
// public keys only.
type ProfileSpec struct {
	// Namespaces are the namespaces that this Profile permits. A namespace is bound only if this
	// list has it and the namespace has the label koodam.dev/profile=<Profile name>.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespaces []string `json:"namespaces,omitempty"`

	// CostGroups are the cost groups that this Profile permits for its bound namespaces.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	CostGroups []string `json:"costGroups,omitempty"`

	// Approval is the trust of the approval records for the Agents of the bound namespaces. An
	// Agent with spec.approval in a namespace of a Profile with no approval fails with the reason
	// NoKeySet.
	// +optional
	Approval *ProfileApproval `json:"approval,omitempty"`
}

// ProfileApproval is the key set, the purpose and the maximum age of a signed approval record. Only
// the admin sets them. A key set from the namespace of the record gives no protection, so the
// Agent and the ApprovalRecord cannot name one.
type ProfileApproval struct {
	// KeySet is the JSON text of the trusted key set of the issuer. It has public keys only.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=65536
	KeySet string `json:"keySet"`

	// Scope is the scope that the signed record must have.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Scope string `json:"scope"`

	// Function is the function that the signed record must have.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Function string `json:"function"`

	// MaxAgeSeconds is the maximum age of a decision at the check, from its time decided_at. An
	// older decision gives the reason Expired. The from and until dates of a decision are the
	// date range of the approved revision, and not an expiry, so the admin sets the expiry here.
	// It has no default.
	// +required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=31536000
	MaxAgeSeconds int64 `json:"maxAgeSeconds"`
}

// ProfileStatus is the binding that the controller found.
type ProfileStatus struct {
	// BoundNamespaces are the namespaces that the Profile permits and that have the label
	// koodam.dev/profile=<Profile name>.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=64
	BoundNamespaces []string `json:"boundNamespaces,omitempty"`

	// Conditions: Ready.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="Namespaces",type=string,JSONPath=`.spec.namespaces`
// +kubebuilder:printcolumn:name="Bound",type=string,JSONPath=`.status.boundNamespaces`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Profile is a developer or a team role. It permits a set of namespaces. A namespace that the
// Profile permits and that has the Profile label is bound to the Profile.
type Profile struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Profile
	// +required
	Spec ProfileSpec `json:"spec"`

	// status defines the observed state of Profile
	// +optional
	Status ProfileStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ProfileList contains a list of Profile
type ProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Profile `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Profile{}, &ProfileList{})
		return nil
	})
}
