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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// HoldHeld is the condition of a Hold that shows that the controller applied the action to each
// workload of the target.
const HoldHeld = "Held"

// HoldAction is the incident action of a Hold.
// +kubebuilder:validation:Enum=Stop;Quarantine
type HoldAction string

const (
	// HoldStop blocks the gateway key of each workload of the target, and then stops its pods.
	HoldStop HoldAction = "Stop"
	// HoldQuarantine blocks the gateway key of each workload of the target, and the egress proxy
	// refuses each call of its pods. The pods and the PVCs stay for an investigation.
	HoldQuarantine HoldAction = "Quarantine"
)

// HoldTargetKind is the kind of the target of a Hold.
// +kubebuilder:validation:Enum=Agent;Workspace;App;AppVersion;Profile
type HoldTargetKind string

const (
	HoldTargetAgent      HoldTargetKind = "Agent"
	HoldTargetWorkspace  HoldTargetKind = "Workspace"
	HoldTargetApp        HoldTargetKind = "App"
	HoldTargetAppVersion HoldTargetKind = "AppVersion"
	// HoldTargetProfile is each Agent, Workspace and AppVersion in the bound namespaces of the
	// Profile.
	HoldTargetProfile HoldTargetKind = "Profile"
)

// HoldTarget is the workload of a Hold. A Profile is cluster-scoped, so a Profile target has no
// namespace. Each other kind needs a namespace.
// +kubebuilder:validation:XValidation:rule="self.kind == 'Profile' ? !has(self.__namespace__) : has(self.__namespace__)",message="target.namespace is necessary for each kind other than Profile, and a Profile target has no namespace"
type HoldTarget struct {
	// Kind is the kind of the workload: Agent, Workspace, App, AppVersion or Profile.
	// +required
	Kind HoldTargetKind `json:"kind"`
	// Namespace is the namespace of the workload. A Profile target has no namespace.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string `json:"namespace,omitempty"`
	// Name is the name of the workload.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-.a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
}

// HoldRelease is the end of a Hold. A key that a Hold blocked stays blocked after the release.
type HoldRelease struct {
	// User is the user that released the Hold. The admission policy of the Hold refuses a value
	// that is not the user of the request.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	User string `json:"user"`
	// Reason tells why the Hold ends.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	Reason string `json:"reason"`
}

// HoldSpec is an incident control on one workload or on the workloads of a Profile. Only an
// admin writes it: the owner of the workload cannot clear it. The target, the action, the reason
// and the user are immutable. The release can be set one time, and then it is immutable.
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.release) || (has(self.release) && self.release == oldSelf.release)",message="spec.release is immutable after it is set"
type HoldSpec struct {
	// Target is the workload of the Hold.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.target is immutable"
	Target HoldTarget `json:"target"`
	// Action is Stop or Quarantine.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.action is immutable"
	Action HoldAction `json:"action"`
	// Reason tells why the Hold starts. The event of the Hold and its span have this text.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.reason is immutable"
	Reason string `json:"reason"`
	// User is the user that made the Hold. The admission policy of the Hold refuses a value that
	// is not the user of the request.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.user is immutable"
	User string `json:"user"`
	// Release ends the Hold. The Hold stays as the record of the incident.
	// +optional
	Release *HoldRelease `json:"release,omitempty"`
}

// HoldStatus is the observed state of a Hold.
type HoldStatus struct {
	// Conditions are the conditions of the Hold: Held.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.target.kind`
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.spec.target.namespace`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target.name`
// +kubebuilder:printcolumn:name="Action",type=string,JSONPath=`.spec.action`
// +kubebuilder:printcolumn:name="User",type=string,JSONPath=`.spec.user`
// +kubebuilder:printcolumn:name="Released",type=string,JSONPath=`.spec.release.user`
// +kubebuilder:printcolumn:name="Held",type=string,JSONPath=`.status.conditions[?(@.type=="Held")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Hold is an incident control: a kill switch (Stop) or a quarantine of a workload. It is
// cluster-scoped, and only an admin role can write it.
type Hold struct {
	metav1.TypeMeta `json:",inline"`
	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`
	// spec defines the desired state of Hold
	// +required
	Spec HoldSpec `json:"spec"`
	// status defines the observed state of Hold
	// +optional
	Status HoldStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// HoldList contains a list of Hold
type HoldList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Hold `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Hold{}, &HoldList{})
		return nil
	})
}
