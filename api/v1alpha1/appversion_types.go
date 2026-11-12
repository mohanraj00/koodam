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
	"k8s.io/apimachinery/pkg/types"
)

// AppContainerName is the name of the main container of an AppVersion pod.
const AppContainerName = "app"

// AppVersionLifecycle is the lifecycle state of an AppVersion. It is not the Kubernetes delete.
// +kubebuilder:validation:Enum=Active;Retiring
type AppVersionLifecycle string

const (
	// AppVersionActive is a version that can serve new sessions.
	AppVersionActive AppVersionLifecycle = "Active"
	// AppVersionRetiring is a version that stops new sessions, drains and then revokes its key.
	// This state is terminal.
	AppVersionRetiring AppVersionLifecycle = "Retiring"
)

// The condition types of an AppVersion. The AppVersion controller sets Revoking, Revoked and
// TracedEgress. No controller sets TrafficAllowed and SessionsEnded at this time.
const (
	// AppVersionTrafficAllowed tells if the version can get public traffic.
	AppVersionTrafficAllowed = "TrafficAllowed"
	// AppVersionSessionsEnded tells if all sessions of a retiring version are ended.
	AppVersionSessionsEnded = "SessionsEnded"
	// AppVersionRevoking tells that the revoke of the key started. This state is terminal.
	AppVersionRevoking = "Revoking"
	// AppVersionRevoked tells that the gateway blocked the key.
	AppVersionRevoked = "Revoked"
	// AppVersionTracedEgress tells if the model calls of the version go through traced egress.
	AppVersionTracedEgress = "TracedEgress"
)

// AppVersionSpec is one version of an App. The developer writes it. The AppVersion controller
// makes a Deployment, a Service, a Role and one gateway key from it.
type AppVersionSpec struct {
	// AppRef is the App of this version, in the same namespace. You cannot change it after
	// create.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.appRef is immutable."
	AppRef AppReference `json:"appRef"`

	// Template is the pod and the gateway model of this version. Koodam does not change a
	// template. To change it, make a new AppVersion. The API server does not refuse a template
	// change at this time. A later admission check refuses it.
	// +required
	Template AppVersionTemplate `json:"template"`

	// Replicas is the number of pods of this version. You can change it.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	Replicas *int32 `json:"replicas,omitempty"`

	// BudgetPerDay is the maximum spend of the version key in one day, in the currency of the
	// gateway price table. It has no default, and it must be more than zero. You can change it.
	// The version keeps the same key.
	// +required
	// +kubebuilder:validation:XValidation:rule="quantity(string(self)).isGreaterThan(quantity('0'))",message="budgetPerDay must be more than zero"
	BudgetPerDay resource.Quantity `json:"budgetPerDay"`

	// Lifecycle is Active or Retiring. A change to Retiring stops new sessions. Then the version
	// drains and revokes its key. You cannot change Retiring back to Active.
	// +optional
	// +kubebuilder:default=Active
	// +kubebuilder:validation:XValidation:rule="oldSelf != 'Retiring' || self == 'Retiring'",message="spec.lifecycle Retiring is terminal. You cannot change it back to Active."
	Lifecycle AppVersionLifecycle `json:"lifecycle,omitempty"`
}

// AppReference is the name of an App in the same namespace.
type AppReference struct {
	// Name is the name of the App.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// AppVersionTemplate is the part of an AppVersion that does not change. The pod template has 1
// main container with the name "app". This container has the image, the command, the
// environment, the ports and the probes of the version. The shared PodTemplate rules apply. The
// API server does not check the port names, because that CEL rule is too costly. A later
// admission check refuses a port with no name or a port that is not TCP.
// +kubebuilder:validation:XValidation:rule="has(self.podTemplate.spec) && has(self.podTemplate.spec.containers) && self.podTemplate.spec.containers.exists_one(c, c.name == 'app')",message="The pod template must have 1 container with the name app."
// +kubebuilder:validation:XValidation:rule="!has(self.podTemplate.spec) || !has(self.podTemplate.spec.containers) || self.podTemplate.spec.containers.all(c, c.name != 'app' || (has(c.image) && size(c.image) > 0 && has(c.readinessProbe) && has(c.ports) && size(c.ports) > 0))",message="The app container must have an image, a readiness probe and 1 or more ports."
type AppVersionTemplate struct {
	// Model is the gateway model name. The version key permits only this model.
	// +required
	// +kubebuilder:validation:MinLength=1
	Model string `json:"model"`

	// Rules are the RBAC rules of the version Role, in the namespace of the AppVersion only.
	// +optional
	Rules []rbacv1.PolicyRule `json:"rules,omitempty"`

	// PodTemplate is the pod of the version.
	// +required
	PodTemplate PodTemplate `json:"podTemplate"`
}

// AppVersionStatus is the observed state of an AppVersion. It never holds a key value, a
// session identifier, a token, an environment value or a header.
type AppVersionStatus struct {
	// BoundAppUID is the UID of the App that this version is bound to. The version never binds
	// to a different App with the same name.
	// +optional
	BoundAppUID types.UID `json:"boundAppUID,omitempty"`

	// KeyAlias identifies the gateway key. The key value is never in the status.
	// +optional
	KeyAlias string `json:"keyAlias,omitempty"`

	// KeyHash is the SHA-256 hash of the gateway key. The gateway refuses the hash for model
	// calls, so it is not a credential.
	// +optional
	KeyHash string `json:"keyHash,omitempty"`

	// Conditions are the conditions of the AppVersion.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="App",type=string,JSONPath=`.spec.appRef.name`
// +kubebuilder:printcolumn:name="Lifecycle",type=string,JSONPath=`.spec.lifecycle`
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.spec.replicas`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AppVersion is one version of an App, with its own pods and its own gateway key.
type AppVersion struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of AppVersion
	// +required
	Spec AppVersionSpec `json:"spec"`

	// status defines the observed state of AppVersion
	// +optional
	Status AppVersionStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AppVersionList contains a list of AppVersion
type AppVersionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []AppVersion `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &AppVersion{}, &AppVersionList{})
		return nil
	})
}
