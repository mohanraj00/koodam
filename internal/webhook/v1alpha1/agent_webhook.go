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

// Package v1alpha1 has the admission decision and its HTTP handler for the Agent ingress of #147.
// No manager, server or install path registers or starts the handler yet.
package v1alpha1

import (
	"errors"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/agentingress"
)

// make manifests writes the output of this marker to a temporary directory and deletes it.
// +kubebuilder:webhook:path=/validate-core-koodam-dev-v1alpha1-agent-ingress,mutating=false,failurePolicy=fail,sideEffects=None,groups=core.koodam.dev,resources=agents,verbs=create;update,versions=v1alpha1,name=agent-ingress.admission.koodam.dev,admissionReviewVersions=v1

// The response messages have fixed text. They never contain a value from the request.
const (
	MessageInvalidRequest = "The admission request is not a valid Agent v1alpha1 create or update request."
	MessagePortRange      = "spec.ingressPort must be from 1 to 65535."
	MessageReservedPort   = "When spec.ingressPort is set, no container or init container in spec.podTemplate " +
		"can have a port with the name http."
)

var (
	reviewKind    = admissionv1.SchemeGroupVersion.WithKind("AdmissionReview")
	agentGVK      = corev1alpha1.SchemeGroupVersion.WithKind("Agent")
	agentKind     = metav1.GroupVersionKind(agentGVK)
	agentResource = metav1.GroupVersionResource(corev1alpha1.SchemeGroupVersion.WithResource("agents"))
	strictDecoder = newStrictDecoder()
)

// newStrictDecoder knows only AdmissionReview v1 and the Koodam v1alpha1 kinds. It does not
// convert and does not apply defaults.
func newStrictDecoder() runtime.Decoder {
	scheme := runtime.NewScheme()
	utilruntime.Must(admissionv1.AddToScheme(scheme))
	utilruntime.Must(corev1alpha1.AddToScheme(scheme))
	return json.NewSerializerWithOptions(json.DefaultMetaFactory, scheme, scheme,
		json.SerializerOptions{Strict: true})
}

// Review decides one AdmissionReview v1 request body. The new AdmissionReview has only the request
// UID, the decision and a fixed status: no patch, warnings, audit data or value from the body.
//
// Review permits only a CREATE or an UPDATE of core.koodam.dev/v1alpha1 agents with no
// subresource and no conversion. It strictly decodes the new Agent and, on UPDATE, the old Agent.
// Then it checks the new Agent with agentingress.Check. The CRD rules stay the authority for the
// bounds and the immutability of spec.ingressPort.
func Review(body []byte) *admissionv1.AdmissionReview {
	uid, status := decide(body)
	resp := &admissionv1.AdmissionResponse{UID: uid, Allowed: status == nil, Result: status}
	return &admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: reviewKind.GroupVersion().String(), Kind: reviewKind.Kind},
		Response: resp,
	}
}

// decide returns the UID for the response and nil to permit the request, or a fixed status to
// refuse it. The UID is empty if the body has no valid request.
func decide(body []byte) (types.UID, *metav1.Status) {
	obj, ok := decodeStrict(body, reviewKind)
	review, _ := obj.(*admissionv1.AdmissionReview)
	if !ok || review == nil || review.Request == nil || review.Response != nil || review.Request.UID == "" {
		return "", invalidRequest()
	}
	req := review.Request
	if !supported(req) {
		return req.UID, invalidRequest()
	}
	agent, ok := decodeAgent(req.Object.Raw)
	if !ok {
		return req.UID, invalidRequest()
	}
	switch req.Operation {
	case admissionv1.Create:
		if req.OldObject.Raw != nil {
			return req.UID, invalidRequest()
		}
	case admissionv1.Update:
		if _, ok := decodeAgent(req.OldObject.Raw); !ok {
			return req.UID, invalidRequest()
		}
	}
	err := agentingress.Check(agent.Spec)
	switch {
	case err == nil:
		return req.UID, nil
	case errors.Is(err, agentingress.ErrPortRange):
		return req.UID, forbidden(MessagePortRange)
	default:
		return req.UID, forbidden(MessageReservedPort)
	}
}

// supported is true only for the exact request kind, resource and operations of the guard.
// An original request kind or resource must be absent or the same, so a converted request fails.
func supported(r *admissionv1.AdmissionRequest) bool {
	return r.Kind == agentKind && r.Resource == agentResource &&
		(r.RequestKind == nil || *r.RequestKind == agentKind) &&
		(r.RequestResource == nil || *r.RequestResource == agentResource) &&
		r.SubResource == "" && r.RequestSubResource == "" &&
		(r.Operation == admissionv1.Create || r.Operation == admissionv1.Update)
}

// decodeAgent strictly decodes a complete Agent v1alpha1.
func decodeAgent(raw []byte) (*corev1alpha1.Agent, bool) {
	obj, ok := decodeStrict(raw, agentGVK)
	agent, _ := obj.(*corev1alpha1.Agent)
	return agent, ok && agent != nil
}

// decodeStrict decodes data as the kind want, which data must state. It refuses each error, also a
// strict error that comes with an object. It does not return the error, which can contain input.
func decodeStrict(data []byte, want schema.GroupVersionKind) (runtime.Object, bool) {
	if len(data) == 0 {
		return nil, false
	}
	obj, gvk, err := strictDecoder.Decode(data, nil, nil)
	if err != nil || obj == nil || gvk == nil || *gvk != want {
		return nil, false
	}
	return obj, true
}

func invalidRequest() *metav1.Status {
	return &metav1.Status{Status: metav1.StatusFailure, Code: http.StatusBadRequest,
		Reason: metav1.StatusReasonBadRequest, Message: MessageInvalidRequest}
}

func forbidden(msg string) *metav1.Status {
	return &metav1.Status{Status: metav1.StatusFailure, Code: http.StatusForbidden,
		Reason: metav1.StatusReasonForbidden, Message: msg}
}
