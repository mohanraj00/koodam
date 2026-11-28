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

// The condition types of an AppVersion. The AppVersion controller sets them.
const (
	// AppVersionTrafficAllowed tells if the version can get new sessions. It is False from the
	// time that the version starts to retire.
	AppVersionTrafficAllowed = "TrafficAllowed"
	// AppVersionSessionsEnded tells if the drain time of a retiring version is at its end.
	// Koodam cannot see a session, so the end of the drain time is the end of the sessions.
	AppVersionSessionsEnded = "SessionsEnded"
	// AppVersionRevoking tells that the revoke of the key started. This state is terminal.
	AppVersionRevoking = "Revoking"
	// AppVersionRevoked tells that the gateway blocked the key.
	AppVersionRevoked = "Revoked"
	// AppVersionTracedEgress tells if the egress of the version pods goes through traced egress.
	// It is False while no NetworkPolicy enforces the egress path of the App pods.
	AppVersionTracedEgress = "TracedEgress"
	// AppVersionEvidence tells the state of the evidence gate. A version with no spec.gate does
	// not have this condition. A gated version gets traffic only while the condition is True.
	AppVersionEvidence = "Evidence"
)

// The reasons of the condition Evidence. Each reason of a False condition is terminal for the
// version. To test again, make a new AppVersion.
const (
	// EvidenceWaiting tells that not all pods of the version are Ready. The status is Unknown.
	EvidenceWaiting = "Waiting"
	// EvidenceRunning tells that the gate Job runs. The status is Unknown.
	EvidenceRunning = "Running"
	// EvidencePassed tells that the controller accepted a record with the verdict pass. The
	// status is True.
	EvidencePassed = "Passed"
	// EvidenceFailed tells that the controller accepted a record with the verdict fail.
	EvidenceFailed = "Failed"
	// EvidenceNotVerified tells that the result cannot be used, that the report does not parse,
	// or that the verdict does not agree with the exit code.
	EvidenceNotVerified = "NotVerified"
	// EvidenceSubjectMismatch tells that the subject of the report is not the expected subject.
	EvidenceSubjectMismatch = "SubjectMismatch"
	// EvidenceJobFailed tells that the gate Job failed after its retries, or that its deadline
	// ended.
	EvidenceJobFailed = "JobFailed"
	// EvidenceImageNotPinned tells that the image of the app container has no digest.
	EvidenceImageNotPinned = "ImageNotPinned"
)

// AppVersionSpec is one version of an App. The developer writes it. The AppVersion controller
// makes a Deployment, a Service and a Role from it, and one gateway key if the template declares
// a model.
// +kubebuilder:validation:XValidation:rule="(has(self.template.model) || has(self.template.models)) == has(self.budgetPerDay)",message="budgetPerDay is necessary with a model, and it is not permitted with no model."
// +kubebuilder:validation:XValidation:rule="has(self.gate) == has(oldSelf.gate)",message="spec.gate is immutable. You cannot add it or remove it after create."
// +kubebuilder:validation:XValidation:rule="!(has(self.replicas) && has(self.scale))",message="Set spec.replicas or spec.scale, not the two."
type AppVersionSpec struct {
	// AppRef is the App of this version, in the same namespace. You cannot change it after
	// create.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.appRef is immutable."
	AppRef AppReference `json:"appRef"`

	// Template is the pod and the gateway model of this version. Koodam does not change a
	// template. To change it, make a new AppVersion. The ValidatingAdmissionPolicy of
	// config/apps/admission refuses a template change after the version has a gateway key.
	// +required
	Template AppVersionTemplate `json:"template"`

	// Replicas is the number of pods of this version. You can change it. With no replicas and
	// no scale, the version has 1 pod. You cannot set replicas and scale together.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Replicas *int32 `json:"replicas,omitempty"`

	// Scale makes the number of pods follow the load of the version. The controller makes one
	// HorizontalPodAutoscaler for the version. You can change it, add it or remove it.
	// +optional
	Scale *AppVersionScale `json:"scale,omitempty"`

	// BudgetPerDay is the maximum spend of the version key in one day, in the currency of the
	// gateway price table. It has no default, and it must be more than zero. You can change it.
	// The version keeps the same key. It is necessary if the template declares a model. A
	// version with no model has no key and no budget.
	// +optional
	// +kubebuilder:validation:XValidation:rule="quantity(string(self)).isGreaterThan(quantity('0'))",message="budgetPerDay must be more than zero"
	BudgetPerDay *resource.Quantity `json:"budgetPerDay,omitempty"`

	// Lifecycle is Active or Retiring. A change to Retiring stops new sessions. Then the version
	// drains and revokes its key. You cannot change Retiring back to Active.
	// +optional
	// +kubebuilder:default=Active
	// +kubebuilder:validation:XValidation:rule="oldSelf != 'Retiring' || self == 'Retiring'",message="spec.lifecycle Retiring is terminal. You cannot change it back to Active."
	Lifecycle AppVersionLifecycle `json:"lifecycle,omitempty"`

	// Gate is the evidence gate of the version. With a gate, the version gets no traffic until
	// the controller accepts an evidence record with the verdict pass for this version. A gated
	// version must name the image of the app container by digest. You cannot add, change or
	// remove the gate after create.
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.gate is immutable."
	Gate *AppVersionGate `json:"gate,omitempty"`
}

// AppVersionGate is the evidence gate of an AppVersion. It has one evidence source.
type AppVersionGate struct {
	// Nookku is a scripted Nookku gate run. The controller runs it in a Job after each pod of
	// the version is Ready.
	// +required
	Nookku NookkuGate `json:"nookku"`
}

// NookkuGate is the gate run of Nookku for one AppVersion. The gate image has Nookku, the
// project, the gate file, the scripts and the entries. The entries send the test messages to the
// address in the environment variable KOODAM_GATE_TARGET, which is the Service of the version.
type NookkuGate struct {
	// Image is the gate image. Name it by digest, because the gate trusts the code of this
	// image.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	Image string `json:"image"`

	// Root is the absolute path of the project root in the image. The Job copies it to a work
	// volume before the run.
	// +required
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:Pattern=`^/[A-Za-z0-9._/-]*$`
	// +kubebuilder:validation:XValidation:rule="!self.split('/').exists(p, p == '..')",message="root must not have a .. part."
	Root string `json:"root"`

	// GateFile is the path of the gate file, relative to the project root.
	// +required
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9._][A-Za-z0-9._/-]*$`
	// +kubebuilder:validation:XValidation:rule="!self.split('/').exists(p, p == '..')",message="gateFile must not have a .. part."
	GateFile string `json:"gateFile"`

	// DeadlineSeconds is the maximum time of the gate Job. After it, the gate has the reason
	// JobFailed.
	// +optional
	// +kubebuilder:default=1800
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=86400
	DeadlineSeconds *int32 `json:"deadlineSeconds,omitempty"`
}

// EvidenceSource is the source of an evidence record.
// +kubebuilder:validation:Enum=nookku-gate;approval
type EvidenceSource string

const (
	// EvidenceSourceNookkuGate is a sealed result of a Nookku gate run.
	EvidenceSourceNookkuGate EvidenceSource = "nookku-gate"
	// EvidenceSourceApproval is a signed approval record.
	EvidenceSourceApproval EvidenceSource = "approval"
)

// EvidenceVerdict is the verdict of an evidence record. A denied approval is fail.
// +kubebuilder:validation:Enum=pass;fail
type EvidenceVerdict string

const (
	// EvidencePass is the verdict of a gate run with no failed check, or of an approval.
	EvidencePass EvidenceVerdict = "pass"
	// EvidenceFail is the verdict of a gate run with a failed check, or of a denied approval.
	EvidenceFail EvidenceVerdict = "fail"
)

// EvidenceIntegrity is the integrity check of an evidence record.
// +kubebuilder:validation:Enum=seal;signature
type EvidenceIntegrity string

const (
	// EvidenceSeal is the seal of a Nookku result. A seal has no key.
	EvidenceSeal EvidenceIntegrity = "seal"
	// EvidenceSignature is the signature of an approval record.
	EvidenceSignature EvidenceIntegrity = "signature"
)

// EvidenceRecord is the record that the controller makes after it accepts the evidence of one
// source. It never holds a key value, a token or text from a test.
type EvidenceRecord struct {
	// Source is the evidence source: nookku-gate or approval.
	// +required
	Source EvidenceSource `json:"source"`

	// Subject is the subject digest of the record. For an AppVersion it is
	// koodam.dev/appversion/v1:<namespace>/<name>:<uid>:<image digest>. For an Agent it is the
	// sha256: digest of the approval request of the run.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	Subject string `json:"subject"`

	// Verdict is pass or fail.
	// +required
	Verdict EvidenceVerdict `json:"verdict"`

	// ID is the Nookku gate id, or the identity of the approval record.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	ID string `json:"id"`

	// Digest is the SHA-256 of the bytes of the record: result.json of the gate run, or the
	// approval record.
	// +required
	// +kubebuilder:validation:Pattern=`^sha256:[0-9a-f]{64}$`
	Digest string `json:"digest"`

	// Integrity is seal for a Nookku result, and signature for an approval record.
	// +required
	Integrity EvidenceIntegrity `json:"integrity"`

	// NookkuVersion is the Nookku version of the verify step. Only a nookku-gate record has it.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	NookkuVersion string `json:"nookkuVersion,omitempty"`

	// CheckedAt is the time of the check by the controller.
	// +required
	CheckedAt metav1.Time `json:"checkedAt"`

	// ExpiresAt is the end of an approval record with a TTL.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`
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
// CRD does not check the port names, because that CEL rule is too costly. The
// ValidatingAdmissionPolicy of config/apps/admission refuses a port with no name, a port that is
// not TCP and a host port.
// +kubebuilder:validation:XValidation:rule="has(self.podTemplate.spec) && has(self.podTemplate.spec.containers) && self.podTemplate.spec.containers.exists_one(c, c.name == 'app')",message="The pod template must have 1 container with the name app."
// +kubebuilder:validation:XValidation:rule="!(has(self.model) && has(self.models))",message="Use model or models, not the two."
// +kubebuilder:validation:XValidation:rule="!has(self.podTemplate.spec) || !has(self.podTemplate.spec.containers) || self.podTemplate.spec.containers.all(c, c.name != 'app' || (has(c.image) && size(c.image) > 0 && has(c.readinessProbe) && has(c.ports) && size(c.ports) > 0))",message="The app container must have an image, a readiness probe and 1 or more ports."
type AppVersionTemplate struct {
	// Model is the gateway model name. It is the short form of a models list with 1 item. The
	// version key permits only this model.
	// +optional
	// +kubebuilder:validation:MinLength=1
	Model string `json:"model,omitempty"`

	// Models are the gateway model names that the version key permits. The gateway refuses a
	// call to a different model. If the template has no model and no models, the version gets
	// no gateway key.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MinLength=1
	Models []string `json:"models,omitempty"`

	// Rules are the RBAC rules of the version Role, in the namespace of the AppVersion only. The
	// admission policy in config/apps/admission refuses rules, because an App pod has no path to
	// the API server yet.
	// +optional
	Rules []rbacv1.PolicyRule `json:"rules,omitempty"`

	// PodTemplate is the pod of the version.
	// +required
	PodTemplate PodTemplate `json:"podTemplate"`
}

// AppVersionScale is the range of pods of a version and the load metric of the scale.
// +kubebuilder:validation:XValidation:rule="self.maxReplicas >= self.minReplicas",message="scale.maxReplicas must be equal to or more than scale.minReplicas."
type AppVersionScale struct {
	// MinReplicas is the smallest number of pods. A version with no load has this number. A
	// version in its drain also has this number.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	MinReplicas *int32 `json:"minReplicas,omitempty"`

	// MaxReplicas is the largest number of pods.
	// +required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1000
	MaxReplicas int32 `json:"maxReplicas"`

	// Metric is the load metric. Set cpu or pods, not the two.
	// +required
	Metric AppVersionScaleMetric `json:"metric"`
}

// AppVersionScaleMetric is the load metric of a version. It has one member.
// +kubebuilder:validation:XValidation:rule="has(self.cpu) != has(self.pods)",message="Set metric.cpu or metric.pods, not the two."
type AppVersionScaleMetric struct {
	// CPU scales on the CPU use of the pods, as a percent of the CPU request of the pods. The
	// cluster needs the resource metrics API, for example metrics-server.
	// +optional
	CPU *AppVersionCPUMetric `json:"cpu,omitempty"`

	// Pods scales on a metric that each pod of the version exports, for example the requests in
	// flight. The cluster needs a custom metrics adapter that serves the metric.
	// +optional
	Pods *AppVersionPodsMetric `json:"pods,omitempty"`
}

// AppVersionCPUMetric is the CPU target of a version.
type AppVersionCPUMetric struct {
	// AverageUtilization is the target CPU use of each pod, as a percent of its CPU request.
	// +required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1000
	AverageUtilization int32 `json:"averageUtilization"`
}

// AppVersionPodsMetric is the target of a pod metric of a version.
type AppVersionPodsMetric struct {
	// Name is the name of the metric in the custom metrics API.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-zA-Z_:][a-zA-Z0-9_:]*$`
	Name string `json:"name"`

	// AverageValue is the target value of the metric for each pod.
	// +required
	// +kubebuilder:validation:XValidation:rule="quantity(string(self)).isGreaterThan(quantity('0'))",message="averageValue must be more than zero"
	AverageValue resource.Quantity `json:"averageValue"`
}

// ModelList gives back the models that the version key permits. It is empty for a version with
// no model.
func (t *AppVersionTemplate) ModelList() []string {
	if t.Model != "" {
		return []string{t.Model}
	}
	return t.Models
}

// AppVersionStatus is the observed state of an AppVersion. It never holds a key value, a
// session identifier, a token, an environment value or a header.
type AppVersionStatus struct {
	// BoundAppUID is the UID of the App that this version is bound to. The version never binds
	// to a different App with the same name.
	// +optional
	BoundAppUID types.UID `json:"boundAppUID,omitempty"`

	// KeyAlias identifies the gateway key. The key value is never in the status. A version with
	// no model has no key and no alias.
	// +optional
	KeyAlias string `json:"keyAlias,omitempty"`

	// KeyHash is the SHA-256 hash of the gateway key. The gateway refuses the hash for model
	// calls, so it is not a credential.
	// +optional
	KeyHash string `json:"keyHash,omitempty"`

	// CostGroup is the cost group of the key, from the label koodam.dev/cost-group when the
	// controller made the key.
	// +optional
	CostGroup string `json:"costGroup,omitempty"`

	// TenantTokenKey is the public key of the tenant token of the version. The claim check
	// container of each pod signs the tenant token with the private key. The egress proxy reads
	// this field, and it accepts a model call of a pod of the version only with a token that
	// this key verifies. With no tenant token, the field is empty.
	// +optional
	TenantTokenKey string `json:"tenantTokenKey,omitempty"`

	// Evidence is the evidence record of the gate. The controller writes it one time, after it
	// accepts the report of the gate Job. A later Job of the same version cannot change it. A
	// version with no spec.gate has no record.
	// +optional
	Evidence *EvidenceRecord `json:"evidence,omitempty"`

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
// +kubebuilder:printcolumn:name="Max",type=integer,JSONPath=`.spec.scale.maxReplicas`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.status) || !has(oldSelf.status.evidence) || (has(self.status) && has(self.status.evidence) && self.status.evidence == oldSelf.status.evidence)",message="status.evidence is written one time. You cannot change it or remove it."

// AppVersion is one version of an App, with its own pods and its own gateway key. The rule for
// status.evidence is on the resource and not on the status, because status is optional: a rule
// on the status does not run for an update that removes the full status.
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
