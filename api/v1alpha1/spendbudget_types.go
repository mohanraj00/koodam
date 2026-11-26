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

// SpendBudgetName is the one name that a SpendBudget can have. Thus a namespace has at most one
// SpendBudget.
const SpendBudgetName = "default"

// ReasonPeriodBudgetExceeded tells that a run waits, because its budget is more than the
// remaining budget of the period. The Agent condition Provisioned and the controller log use it.
const ReasonPeriodBudgetExceeded = "PeriodBudgetExceeded"

// The conditions of a SpendBudget.
const (
	// SpendBudgetReady shows that the controller read the Agent runs of the namespace and wrote
	// the ledger of the current period.
	SpendBudgetReady = "Ready"
	// SpendBudgetExceeded shows that at least one run of the namespace waits for the next period.
	SpendBudgetExceeded = "BudgetExceeded"
)

// SpendPeriod is the length of one budget period. Each period starts at a UTC calendar boundary.
// +kubebuilder:validation:Enum=Day;Month
type SpendPeriod string

const (
	// SpendPeriodDay is a period that starts at 00:00 UTC each day.
	SpendPeriodDay SpendPeriod = "Day"
	// SpendPeriodMonth is a period that starts at 00:00 UTC on the first day of each month.
	SpendPeriodMonth SpendPeriod = "Month"
)

// PeriodLimits is the spend limit for each period. The SpendBudget uses it, and a later budget of
// a different scope can use it too.
type PeriodLimits struct {
	// Period is the length of one budget period: Day or Month. Koodam starts each period at a
	// UTC calendar boundary.
	// +optional
	// +kubebuilder:default=Day
	Period SpendPeriod `json:"period,omitempty"`
	// Spend is the maximum spend in one period, in the currency of the gateway price table. It
	// must be more than zero.
	// +required
	// +kubebuilder:validation:XValidation:rule="quantity(string(self)).isGreaterThan(quantity('0'))",message="spend must be more than zero"
	Spend resource.Quantity `json:"spend"`
}

// SpendBudgetSpec is the period budget of the Agent runs of one namespace. Only an admin writes
// it: the roles of Koodam give the users of a namespace only get, list and watch.
type SpendBudgetSpec struct {
	PeriodLimits `json:",inline"`
}

// SpendReservation is the budget of one active run that the ledger holds for the current period.
type SpendReservation struct {
	// RunID is the run ID of the Agent, which is its UID.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	RunID string `json:"runID"`
	// Agent is the name of the Agent.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Agent string `json:"agent"`
	// Budget is the run budget that the ledger holds, as a decimal number.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Budget string `json:"budget"`
}

// SpendBudgetStatus is the ledger of the current period. Only the Agent runs of the namespace
// count. A run counts in the period where it started.
type SpendBudgetStatus struct {
	// Conditions are the conditions of the SpendBudget: Ready and BudgetExceeded.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// PeriodStart is the start of the current period.
	// +optional
	PeriodStart *metav1.Time `json:"periodStart,omitempty"`
	// PeriodEnd is the start of the next period.
	// +optional
	PeriodEnd *metav1.Time `json:"periodEnd,omitempty"`
	// Spend is the settled spend of the runs that started in the current period and ended, as
	// a decimal number.
	// +optional
	Spend string `json:"spend,omitempty"`
	// Reserved is the sum of the budgets of the active runs that started in the current
	// period, as a decimal number.
	// +optional
	Reserved string `json:"reserved,omitempty"`
	// Reservations are the active runs that the ledger holds. A run that cannot get a place in
	// this list waits.
	// +listType=map
	// +listMapKey=runID
	// +kubebuilder:validation:MaxItems=500
	// +optional
	Reservations []SpendReservation `json:"reservations,omitempty"`
	// WaitingRuns is the count of the runs of the namespace that wait for the next period.
	// +optional
	WaitingRuns int32 `json:"waitingRuns,omitempty"`
	// ObservedTime is the time when the controller last wrote a change of the status.
	// +optional
	ObservedTime *metav1.Time `json:"observedTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'default'",message="the name of a SpendBudget must be default, so that a namespace has only one."
// +kubebuilder:printcolumn:name="Period",type=string,JSONPath=`.spec.period`
// +kubebuilder:printcolumn:name="Limit",type=string,JSONPath=`.spec.spend`
// +kubebuilder:printcolumn:name="Spend",type=string,JSONPath=`.status.spend`
// +kubebuilder:printcolumn:name="Reserved",type=string,JSONPath=`.status.reserved`
// +kubebuilder:printcolumn:name="Waiting",type=integer,JSONPath=`.status.waitingRuns`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SpendBudget is the period budget of the Agent runs of a namespace. A run starts only if the
// spend and the reservations of the period, with the budget of the run, are not more than the
// limit. Otherwise the run waits for the next period.
type SpendBudget struct {
	metav1.TypeMeta `json:",inline"`
	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`
	// spec defines the desired state of SpendBudget
	// +required
	Spec SpendBudgetSpec `json:"spec"`
	// status defines the observed state of SpendBudget
	// +optional
	Status SpendBudgetStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// SpendBudgetList contains a list of SpendBudget
type SpendBudgetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []SpendBudget `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &SpendBudget{}, &SpendBudgetList{})
		return nil
	})
}
