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

// Package conversion has the HTTP handler of the CRD conversion webhook of #511. The handler
// converts the kinds of the group core.koodam.dev between v1alpha1 and v1beta1 with the
// conversion functions of api/v1alpha1.
//
// A CRD conversion has no failure policy: if the handler fails, the API server fails the request.
// Thus the handler converts all the objects of a request or none. Each failure has a fixed reason.
// No response and no report has data from an object or from an error.
package conversion

import (
	"encoding/json"
	"net/http"

	apix "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrlconversion "sigs.k8s.io/controller-runtime/pkg/conversion"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
)

// MaxRequestBytes is the limit of one request body. The API server sends all the objects of a list
// in one request. A larger request fails, and the process stays up for the other requests.
const MaxRequestBytes = 64 << 20

// The reasons of a refusal. They are the only failure text.
const (
	ReasonRequest  = "the request is not a valid ConversionReview"
	ReasonKind     = "the kind or the version is not known"
	ReasonObject   = "an object is not valid"
	ReasonInternal = "internal error"
)

const failure = "The koodam conversion webhook did not convert the objects: "

type handler struct {
	scheme  *runtime.Scheme
	decoder runtime.Decoder
	refused func(reason string)
}

// NewHandler returns the conversion handler. It calls refused with the fixed reason of each
// request that it does not convert.
func NewHandler(refused func(reason string)) http.Handler {
	scheme := runtime.NewScheme()
	utilruntime.Must(corev1alpha1.AddToScheme(scheme))
	utilruntime.Must(corev1beta1.AddToScheme(scheme))
	// The deserializer does not convert and does not apply defaults.
	return &handler{scheme: scheme, decoder: serializer.NewCodecFactory(scheme).UniversalDeserializer(), refused: refused}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	review := &apix.ConversionReview{}
	body := http.MaxBytesReader(w, r.Body, MaxRequestBytes)
	if r.Method != http.MethodPost || json.NewDecoder(body).Decode(review) != nil || review.Request == nil ||
		review.APIVersion != apix.SchemeGroupVersion.String() || review.Kind != "ConversionReview" {
		h.refused(ReasonRequest)
		http.Error(w, ReasonRequest, http.StatusBadRequest)
		return
	}
	review.Response = h.convert(review.Request)
	review.Request = nil
	w.Header().Set("Content-Type", "application/json")
	// A write failure has no other report: the API server fails the request.
	_ = json.NewEncoder(w).Encode(review)
}

// convert returns the response for req. It has all the converted objects, in the order of the
// request, or a failure with no object.
func (h *handler) convert(req *apix.ConversionRequest) (resp *apix.ConversionResponse) {
	fail := func(reason string) *apix.ConversionResponse {
		h.refused(reason)
		return &apix.ConversionResponse{UID: req.UID,
			Result: metav1.Status{Status: metav1.StatusFailure, Message: failure + reason}}
	}
	defer func() {
		if recover() != nil {
			resp = fail(ReasonInternal)
		}
	}()
	want, err := schema.ParseGroupVersion(req.DesiredAPIVersion)
	if err != nil || !h.scheme.IsVersionRegistered(want) {
		return fail(ReasonKind)
	}
	out := make([]runtime.RawExtension, 0, len(req.Objects))
	for _, in := range req.Objects {
		raw, reason := h.convertOne(in.Raw, want)
		if reason != "" {
			return fail(reason)
		}
		out = append(out, runtime.RawExtension{Raw: raw})
	}
	return &apix.ConversionResponse{UID: req.UID, ConvertedObjects: out, Result: metav1.Status{Status: metav1.StatusSuccess}}
}

// convertOne returns the JSON of one object in the version want, or the reason of the refusal.
func (h *handler) convertOne(raw []byte, want schema.GroupVersion) ([]byte, string) {
	src, gvk, err := h.decoder.Decode(raw, nil, nil)
	if err != nil {
		if runtime.IsNotRegisteredError(err) || runtime.IsMissingKind(err) || runtime.IsMissingVersion(err) {
			return nil, ReasonKind
		}
		return nil, ReasonObject
	}
	if gvk.GroupVersion() == want {
		return raw, ""
	}
	dst, err := h.scheme.New(want.WithKind(gvk.Kind))
	if err != nil {
		return nil, ReasonKind
	}
	// v1beta1 is the hub. Each v1alpha1 kind converts to it and from it. A list kind and a kind of
	// the API machinery are not one of the two, so they get a refusal.
	switch src := src.(type) {
	case ctrlconversion.Convertible:
		hub, ok := dst.(ctrlconversion.Hub)
		if !ok {
			return nil, ReasonKind
		}
		err = src.ConvertTo(hub)
	case ctrlconversion.Hub:
		spoke, ok := dst.(ctrlconversion.Convertible)
		if !ok {
			return nil, ReasonKind
		}
		err = spoke.ConvertFrom(src)
	default:
		return nil, ReasonKind
	}
	if err != nil {
		return nil, ReasonObject
	}
	dst.GetObjectKind().SetGroupVersionKind(want.WithKind(gvk.Kind))
	converted, err := json.Marshal(dst)
	if err != nil {
		return nil, ReasonInternal
	}
	return converted, ""
}
