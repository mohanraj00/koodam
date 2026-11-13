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
// maxLiveVersions. The App controller makes the routes of the endpoints.
type AppSpec struct {
	// MaxLiveVersions is the maximum number of AppVersions of this App that can have a gateway
	// key at the same time. The count includes the candidate versions, the versions that drain
	// and the versions with an uncertain key. If the limit is full, a new version waits. Koodam
	// does not remove a version to make space.
	// +optional
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=1
	MaxLiveVersions int32 `json:"maxLiveVersions,omitempty"`

	// DrainSeconds is the time that a retiring version keeps its pods and its key. In this
	// time the version gets no new session, and a client that sends the version header stays
	// on the version. Koodam cannot see a session, so the end of this time is the end of the
	// sessions. Then the controller revokes the key.
	// +optional
	// +kubebuilder:default=300
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=604800
	DrainSeconds *int32 `json:"drainSeconds,omitempty"`

	// Endpoints are the named entries of the App. The App controller makes one Gateway API
	// HTTPRoute for each endpoint. The route sends a weight of the traffic to each version.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	Endpoints []AppEndpoint `json:"endpoints,omitempty"`
}

// AppVersionHeader is the HTTP header that keeps a client session on one version. A request
// with this header and the name of a version of the endpoint goes to that version only. The app
// container has the name of its version in the env variable KOODAM_APP_VERSION, so the app can
// give the name to its client.
const AppVersionHeader = "Koodam-App-Version"

// AppEndpoint is one named entry of an App on a Gateway.
type AppEndpoint struct {
	// Name is the name of the endpoint. The HTTPRoute has the name kd-<app>.<endpoint>.
	// +required
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Gateway is the Gateway API Gateway that gets the traffic of the endpoint. The Gateway
	// must permit routes from the namespace of the App.
	// +required
	Gateway AppGatewayReference `json:"gateway"`

	// Hostnames are the host names of the HTTPRoute. If it is empty, the route matches each
	// host name of the Gateway listener.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=253
	Hostnames []string `json:"hostnames,omitempty"`

	// Port is the name of the port of the app container that gets the traffic.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=15
	Port string `json:"port"`

	// Versions are the AppVersions of the endpoint, each with its weight. An HTTPRoute has 16
	// rules at most: 1 rule for the weights and 1 rule for the header of each version.
	// +required
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=15
	Versions []AppEndpointVersion `json:"versions"`
}

// AppGatewayReference is a Gateway API Gateway.
type AppGatewayReference struct {
	// Name is the name of the Gateway.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Namespace is the namespace of the Gateway. If it is empty, it is the namespace of the App.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace,omitempty"`

	// SectionName is the name of one listener of the Gateway. If it is empty, the route
	// attaches to each listener that permits it.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	SectionName string `json:"sectionName,omitempty"`
}

// AppEndpointVersion is the weight of one AppVersion in an endpoint.
type AppEndpointVersion struct {
	// Name is the name of the AppVersion.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Weight is the part of the traffic that the version gets, relative to the sum of the
	// weights of the endpoint. A version with the weight 0 gets no traffic.
	// +required
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1000000
	Weight *int32 `json:"weight"`
}

// The route states of an endpoint.
const (
	// AppRouteAccepted tells that each parent Gateway accepted the route.
	AppRouteAccepted = "Accepted"
	// AppRoutePending tells that no Gateway controller reported the route state.
	AppRoutePending = "Pending"
	// AppRouteNotAccepted tells that a Gateway refused the route or a backend.
	AppRouteNotAccepted = "NotAccepted"
	// AppRouteNoGatewayAPI tells that the cluster does not have the Gateway API HTTPRoute.
	AppRouteNoGatewayAPI = "NoGatewayAPI"
	// AppRouteNameConflict tells that an HTTPRoute with the route name is not from this App.
	AppRouteNameConflict = "NameConflict"
	// AppRouteRefused tells that the API server refused the route.
	AppRouteRefused = "Refused"
)

// AppEndpointStatus is the observed state of one endpoint.
type AppEndpointStatus struct {
	// Name is the name of the endpoint.
	// +required
	Name string `json:"name"`

	// Weights are the weights in effect. A version that cannot serve has the weight 0, and the
	// route sends no new session to it.
	// +optional
	// +listType=atomic
	Weights []AppEndpointVersion `json:"weights,omitempty"`

	// RouteState is the state of the HTTPRoute of the endpoint: Accepted, Pending,
	// NotAccepted, NoGatewayAPI, NameConflict or Refused.
	// +optional
	RouteState string `json:"routeState,omitempty"`

	// Message tells why the route has this state, or which versions cannot serve.
	// +optional
	Message string `json:"message,omitempty"`
}

// AppStatus is the observed state of an App.
type AppStatus struct {
	// Conditions are the conditions of the App.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Endpoints has the weights in effect and the route state of each endpoint.
	// +optional
	// +listType=map
	// +listMapKey=name
	Endpoints []AppEndpointStatus `json:"endpoints,omitempty"`
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
