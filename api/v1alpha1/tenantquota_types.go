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

// The typed reasons of a tenant refusal. The ingress span, the condition of the TenantQuota and
// the log of the controller use these words.
const (
	// ReasonTenantRateLimited tells that the tenant sent more requests than its rate limit.
	ReasonTenantRateLimited = "TenantRateLimited"
	// ReasonTenantQuotaExceeded tells that the tenant has no more quota in the current period.
	ReasonTenantQuotaExceeded = "TenantQuotaExceeded"
	// ReasonUnknownTenant tells that no TenantQuota names the tenant label, and the install
	// refuses such a tenant.
	ReasonUnknownTenant = "UnknownTenant"
)

// The conditions of a TenantQuota.
const (
	// TenantQuotaReady shows that the gateway has the customer of the tenant with the limits of
	// the spec, and that the controller read the use.
	TenantQuotaReady = "Ready"
	// TenantQuotaExceeded shows that the tenant is at a limit of the current period. The claim
	// check refuses each request of the tenant while the status is True.
	TenantQuotaExceeded = "QuotaExceeded"
)

// TenantQuotaPeriod is the length of one quota period.
// +kubebuilder:validation:Enum=Hour;Day
type TenantQuotaPeriod string

const (
	// TenantQuotaPeriodHour is a period of 1 hour.
	TenantQuotaPeriodHour TenantQuotaPeriod = "Hour"
	// TenantQuotaPeriodDay is a period of 1 day.
	TenantQuotaPeriodDay TenantQuotaPeriod = "Day"
)

// TenantQuotaSpec is the quota and the rate limit of one tenant. A tenant is a client group of
// an App. The tenant label comes from the signed claim that the claim check of the App
// verifies. A TenantQuota with no limit only makes the tenant label a known tenant.
type TenantQuotaSpec struct {
	// Tenant is the tenant label of the signed claim. You cannot change it after create. The
	// gateway has one customer for each tenant label of the install, so use one TenantQuota for
	// each tenant label.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.tenant is immutable."
	Tenant string `json:"tenant"`

	// AppRef is the App that the rate limit and the claim check refusals apply to. With no
	// value, they apply to each App of the namespace.
	// +optional
	AppRef *AppReference `json:"appRef,omitempty"`

	// Period is the length of one quota period: Hour or Day. The gateway sets the start of
	// each period.
	// +optional
	// +kubebuilder:default=Day
	Period TenantQuotaPeriod `json:"period,omitempty"`

	// Requests is the maximum count of model calls of the tenant in one period. With no value,
	// the count has no limit.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Requests *int64 `json:"requests,omitempty"`

	// Spend is the maximum spend of the tenant in one period, in the currency of the gateway
	// price table. It must be more than zero. With no value, the spend has no limit.
	// +optional
	// +kubebuilder:validation:XValidation:rule="quantity(string(self)).isGreaterThan(quantity('0'))",message="spend must be more than zero"
	Spend *resource.Quantity `json:"spend,omitempty"`

	// RateLimit is the rate limit of the tenant at the ingress of the App. With no value, the
	// tenant has no rate limit.
	// +optional
	RateLimit *TenantRateLimit `json:"rateLimit,omitempty"`
}

// TenantRateLimit is a token bucket. Each pod of an AppVersion has its own bucket, with the
// rate and the burst divided by the replicas of the version.
type TenantRateLimit struct {
	// RequestsPerSecond is the rate that the tenant can keep, for example "5" or "500m". It
	// must be more than zero.
	// +required
	// +kubebuilder:validation:XValidation:rule="quantity(string(self)).isGreaterThan(quantity('0'))",message="requestsPerSecond must be more than zero"
	// +kubebuilder:validation:XValidation:rule="quantity(string(self)).compareTo(quantity('1000000')) <= 0",message="requestsPerSecond must be 1000000 or less"
	RequestsPerSecond resource.Quantity `json:"requestsPerSecond"`

	// Burst is the count of requests that the tenant can send at one time. With no value, it
	// is the rate, with a minimum of 1.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1000000
	Burst *int32 `json:"burst,omitempty"`
}

// TenantQuotaStatus is the use of the tenant in the current period.
type TenantQuotaStatus struct {
	// Conditions are the conditions of the TenantQuota: Ready and QuotaExceeded.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// PeriodEnd is the time when the gateway starts the next period of the tenant.
	// +optional
	PeriodEnd *metav1.Time `json:"periodEnd,omitempty"`

	// Requests is the count of model calls of the tenant in the current period.
	// +optional
	Requests int64 `json:"requests,omitempty"`

	// Spend is the spend of the tenant in the current period, as a decimal number.
	// +optional
	Spend string `json:"spend,omitempty"`

	// ObservedTime is the time when the controller last wrote a change of the status.
	// +optional
	ObservedTime *metav1.Time `json:"observedTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Tenant",type=string,JSONPath=`.spec.tenant`
// +kubebuilder:printcolumn:name="App",type=string,JSONPath=`.spec.appRef.name`
// +kubebuilder:printcolumn:name="Requests",type=integer,JSONPath=`.status.requests`
// +kubebuilder:printcolumn:name="Spend",type=string,JSONPath=`.status.spend`
// +kubebuilder:printcolumn:name="Exceeded",type=string,JSONPath=`.status.conditions[?(@.type=="QuotaExceeded")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TenantQuota is the quota and the rate limit of one tenant of the Apps of a namespace.
type TenantQuota struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of TenantQuota
	// +required
	Spec TenantQuotaSpec `json:"spec"`

	// status defines the observed state of TenantQuota
	// +optional
	Status TenantQuotaStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// TenantQuotaList contains a list of TenantQuota
type TenantQuotaList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []TenantQuota `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &TenantQuota{}, &TenantQuotaList{})
		return nil
	})
}
