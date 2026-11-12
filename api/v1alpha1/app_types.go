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

// AppSpec is the desired state of an App. An App is an application that serves clients. Its
// AppVersions hold the pods. The AppVersion controller binds each version to its App and reads
// maxLiveVersions.
type AppSpec struct {
	// MaxLiveVersions is the maximum number of AppVersions of this App that can have a gateway
	// key at the same time. The count includes the candidate versions, the versions that drain
	// and the versions with an uncertain key. If the limit is full, a new version waits. Koodam
	// does not remove a version to make space.
	// +optional
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=1
	MaxLiveVersions int32 `json:"maxLiveVersions,omitempty"`
}

// AppStatus is the observed state of an App.
type AppStatus struct {
	// Conditions are the conditions of the App.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Max live",type=integer,JSONPath=`.spec.maxLiveVersions`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// App is an application that serves clients. Each AppVersion of the App has its own pods and
// its own gateway key.
type App struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of App
	// +required
	Spec AppSpec `json:"spec"`

	// status defines the observed state of App
	// +optional
	Status AppStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AppList contains a list of App
type AppList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []App `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &App{}, &AppList{})
		return nil
	})
}
