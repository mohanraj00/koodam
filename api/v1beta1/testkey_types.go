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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

// TestKeySpec asks for one gateway key for one test run in a Workspace. All the fields but models are
// necessary. The API server refuses a change to a spec value after the create.
// No controller uses this type at this time. The API server does not do a check on the
// Workspace UID, the limits of the Workspace or the count of active keys.
type TestKeySpec struct {
	// WorkspaceRef is the Workspace in the same namespace that gets the key.
	// +required
	WorkspaceRef TestKeyWorkspaceRef `json:"workspaceRef"`

	// TestRunID is the ID of the test run, from the test tool. It is a letter or a digit, then
	// letters, digits, "_" or "-". It has no length limit. The controller makes a bounded key
	// alias from the ID and the TestKey UID.
	// +required
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9][A-Za-z0-9_-]*$`
	TestRunID string `json:"testRunID"`

	// Budget is the maximum spend of this test key, in the currency of the gateway price table.
	// It must be more than zero. There is no default.
	// +required
	// +kubebuilder:validation:XValidation:rule="quantity(string(self)).isGreaterThan(quantity('0'))",message="budget must be more than zero"
	Budget resource.Quantity `json:"budget"`

	// TTL is the maximum life of the key, for example "30m". It must be more than zero.
	// +required
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:XValidation:rule="duration(self) > duration('0s')",message="ttl must be more than zero"
	TTL metav1.Duration `json:"ttl"`

	// Models are the models that the key permits. Each model must be in the gateway of the
	// Workspace. If the list is empty, the key permits only the first model of the Workspace.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=253
	// +kubebuilder:validation:XValidation:rule="self.all(m, self.exists_one(n, n == m))",message="Each model must be in the list only one time."
	Models []string `json:"models,omitempty"`
}

// TestKeyWorkspaceRef identifies one Workspace in the namespace of the TestKey.
type TestKeyWorkspaceRef struct {
	// Name is the name of the Workspace.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Name string `json:"name"`

	// UID is the UID of the Workspace. It stops a key for a new Workspace with the same name.
	// +required
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	UID types.UID `json:"uid"`
}

// TestKeyStatus is the observed state of a TestKey. It never holds the key value.
type TestKeyStatus struct {
	// KeyAlias identifies the gateway key. It comes from the test ID and the TestKey UID.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	KeyAlias string `json:"keyAlias,omitempty"`

	// KeyHash is the SHA-256 hash of the gateway key, as 64 hexadecimal characters. The gateway
	// refuses the hash for model calls, so it is not a credential.
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	KeyHash string `json:"keyHash,omitempty"`

	// ExpiresAt is the time when the key stops.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`

	// RevokedAt is the time of the revoke of the key.
	// +optional
	RevokedAt *metav1.Time `json:"revokedAt,omitempty"`

	// Usage is the gateway count after the revoke.
	// +optional
	Usage *AgentUsage `json:"usage,omitempty"`

	// Conditions are the conditions of the TestKey. A message must not hold a key value or an
	// upstream error text.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Workspace",type=string,JSONPath=`.spec.workspaceRef.name`
// +kubebuilder:printcolumn:name="Test",type=string,JSONPath=`.spec.testRunID`
// +kubebuilder:printcolumn:name="Budget",type=string,JSONPath=`.spec.budget`
// +kubebuilder:printcolumn:name="Expires",type=date,JSONPath=`.status.expiresAt`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TestKey is a request for one short-lived gateway key for one test run in a Workspace.
type TestKey struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of TestKey. It is immutable. The API server compares the
	// budget and the TTL by value, thus "0.50" and "500m" are the same budget.
	// +required
	// +kubebuilder:validation:XValidation:rule="self.workspaceRef == oldSelf.workspaceRef && self.testRunID == oldSelf.testRunID && quantity(string(self.budget)).compareTo(quantity(string(oldSelf.budget))) == 0 && duration(self.ttl) == duration(oldSelf.ttl) && (has(self.models) ? self.models : []) == (has(oldSelf.models) ? oldSelf.models : [])",message="spec is immutable"
	Spec TestKeySpec `json:"spec"`

	// status defines the observed state of TestKey
	// +optional
	Status TestKeyStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// TestKeyList contains a list of TestKey
type TestKeyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []TestKey `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &TestKey{}, &TestKeyList{})
		return nil
	})
}
