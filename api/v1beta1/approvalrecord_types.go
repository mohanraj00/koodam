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

// ApprovalRecordSpec carries one signed approval decision to the Agent controller, with the 2
// documents that bind the decision to the run. It has no authority fields: no TTL, no approver
// and no reason. The signature of the decision is the check. Thus a writer of this resource with
// no signing key cannot approve a run, and cannot stop it. The spec is immutable. A new decision
// needs a new ApprovalRecord. In JSON and YAML, each bytes field is base64 text.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
type ApprovalRecordSpec struct {
	// Target is the name of the Agent, in the namespace of this resource. It is a selectable
	// field, so the controller lists only the records of one Agent.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-.a-z0-9]*[a-z0-9])?$`
	Target string `json:"target"`

	// SignedRecord is the bytes of the signed decision: 1,000,000 bytes maximum. The maximum
	// length of the base64 text is 1,333,336 characters.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1333336
	// +kubebuilder:validation:XValidation:rule="size(self) <= 1000000",message="spec.signedRecord must have 1,000,000 bytes or fewer"
	SignedRecord []byte `json:"signedRecord"`

	// SnapshotRecord is the stored bytes of the snapshot record that the decision names: 65,536
	// bytes maximum.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=87384
	// +kubebuilder:validation:XValidation:rule="size(self) <= 65536",message="spec.snapshotRecord must have 65,536 bytes or fewer"
	SnapshotRecord []byte `json:"snapshotRecord"`

	// Manifest is the bytes of the content manifest that the snapshot record names: 65,536 bytes
	// maximum. The manifest names the digest of the approval request of the run.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=87384
	// +kubebuilder:validation:XValidation:rule="size(self) <= 65536",message="spec.manifest must have 65,536 bytes or fewer"
	Manifest []byte `json:"manifest"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:selectablefield:JSONPath=`.spec.target`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ApprovalRecord is the carrier of a signed approval decision for one Agent. It has no status:
// the Agent controller verifies the decision and writes the result to the status of the Agent.
type ApprovalRecord struct {
	metav1.TypeMeta `json:",inline"`
	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`
	// spec defines the desired state of ApprovalRecord
	// +required
	Spec ApprovalRecordSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// ApprovalRecordList contains a list of ApprovalRecord
type ApprovalRecordList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ApprovalRecord `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &ApprovalRecord{}, &ApprovalRecordList{})
		return nil
	})
}
