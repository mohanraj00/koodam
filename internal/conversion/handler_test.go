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

package conversion

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	apix "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const (
	alpha = "core.koodam.dev/v1alpha1"
	beta  = "core.koodam.dev/v1beta1"
	// marker is in each test object. No response of a failure and no report can have it.
	marker = "object-data-marker"
	uid    = "request-1"
	// run is a test ID.
	run = "run-1"
	// The keys of an object.
	keyVersion = "apiVersion"
	keyName    = "name"
	// The name of the TestKey field in each version.
	oldID = "testID"
	newID = "testRunID"
)

var kinds = []string{"Agent", "App", "AppVersion", "ApprovalRecord", "Hold", "Profile", "SpendBudget", "TenantQuota",
	"TestKey", "Workspace"}

type object = map[string]any

func newObject(apiVersion, kind string, spec object) object {
	return object{keyVersion: apiVersion, "kind": kind,
		"metadata": object{keyName: "one", "namespace": "ns", "labels": object{"data": marker}}, "spec": spec}
}

func reviewOf(desired string, objects ...any) object {
	return object{keyVersion: "apiextensions.k8s.io/v1", "kind": "ConversionReview",
		"request": object{"uid": uid, "desiredAPIVersion": desired, "objects": objects}}
}

// call sends body to a new handler. It returns the status code, the response body and the reports.
func call(t *testing.T, method string, body []byte) (int, string, []string) {
	t.Helper()
	var reports []string
	rec := httptest.NewRecorder()
	NewHandler(func(reason string) { reports = append(reports, reason) }).
		ServeHTTP(rec, httptest.NewRequest(method, "/convert", bytes.NewReader(body)))
	return rec.Code, rec.Body.String(), reports
}

// convert sends a review and returns the response and the reports. The request must get the code 200.
func convert(t *testing.T, review object) (*apix.ConversionResponse, []string) {
	t.Helper()
	in, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	code, body, reports := call(t, http.MethodPost, in)
	out := &apix.ConversionReview{}
	if err := json.Unmarshal([]byte(body), out); err != nil || code != http.StatusOK {
		t.Fatalf("code %d, decode error %v", code, err)
	}
	if out.APIVersion != "apiextensions.k8s.io/v1" || out.Kind != "ConversionReview" || out.Request != nil ||
		out.Response == nil || out.Response.UID != uid {
		t.Fatalf("the review is not a response to the request: %s", body)
	}
	return out.Response, reports
}

// converted returns the one converted object of a successful response.
func converted(t *testing.T, review object) object {
	t.Helper()
	resp, reports := convert(t, review)
	if resp.Result.Status != metav1.StatusSuccess || len(resp.ConvertedObjects) != 1 || len(reports) != 0 {
		t.Fatalf("want one converted object and no report, got %+v, %v", resp.Result, reports)
	}
	out := object{}
	if err := json.Unmarshal(resp.ConvertedObjects[0].Raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func at(o object, path ...string) any {
	var v any = o
	for _, key := range path {
		m, ok := v.(object)
		if !ok {
			return nil
		}
		v = m[key]
	}
	return v
}

func TestEachKindConvertsInTheTwoDirections(t *testing.T) {
	for _, kind := range kinds {
		for _, to := range []string{beta, alpha} {
			from := alpha
			if to == alpha {
				from = beta
			}
			got := converted(t, reviewOf(to, newObject(from, kind, object{})))
			if got[keyVersion] != to || got["kind"] != kind || at(got, "metadata", keyName) != "one" ||
				at(got, "metadata", "namespace") != "ns" || at(got, "metadata", "labels", "data") != marker {
				t.Errorf("%s to %s: got %v", kind, to, got)
			}
		}
	}
}

func TestTestKeyID(t *testing.T) {
	got := converted(t, reviewOf(beta, newObject(alpha, "TestKey", object{oldID: run})))
	if at(got, "spec", newID) != run || at(got, "spec", oldID) != nil {
		t.Errorf("to v1beta1: got spec %v", got["spec"])
	}
	got = converted(t, reviewOf(alpha, got))
	if at(got, "spec", oldID) != run || at(got, "spec", newID) != nil {
		t.Errorf("to v1alpha1: got spec %v", got["spec"])
	}
}

func TestPluginsThatV1alpha1CannotHold(t *testing.T) {
	plugins := []any{"nookku", "other"}
	hub := newObject(beta, "Agent", object{"harness": object{keyName: "codex", "plugins": plugins}})
	spoke := converted(t, reviewOf(alpha, hub))
	if got := at(spoke, "spec", "harness", "plugins"); !reflect.DeepEqual(got, []any{"nookku"}) {
		t.Errorf("v1alpha1 plugins: got %v", got)
	}
	if at(spoke, "metadata", "annotations", corev1alpha1.ConversionDataAnnotation) == nil {
		t.Error("the v1alpha1 form has no conversion data")
	}
	back := converted(t, reviewOf(beta, spoke))
	if got := at(back, "spec", "harness", "plugins"); !reflect.DeepEqual(got, plugins) {
		t.Errorf("v1beta1 plugins: got %v", got)
	}
	if at(back, "metadata", "annotations", corev1alpha1.ConversionDataAnnotation) != nil {
		t.Error("the v1beta1 form has the conversion data")
	}
}

func TestOrderAndSameVersion(t *testing.T) {
	same := newObject(beta, "Hold", object{"reason": marker})
	resp, _ := convert(t, reviewOf(beta, newObject(alpha, "TestKey", object{oldID: "a"}), same,
		newObject(alpha, "TestKey", object{oldID: "b"})))
	if resp.Result.Status != metav1.StatusSuccess || len(resp.ConvertedObjects) != 3 {
		t.Fatalf("got %+v", resp.Result)
	}
	for i, want := range []string{`"testRunID":"a"`, `"reason":"` + marker + `"`, `"testRunID":"b"`} {
		if raw := string(resp.ConvertedObjects[i].Raw); !strings.Contains(raw, want) {
			t.Errorf("object %d: want %s in %s", i, want, raw)
		}
	}
}

// TestRefusals makes sure that the handler converts all the objects or none, and that a failure
// has a fixed reason with no object data.
func TestRefusals(t *testing.T) {
	good := newObject(alpha, "TestKey", object{oldID: run})
	cases := map[string]struct {
		review object
		reason string
	}{
		"desired version not known": {reviewOf("core.koodam.dev/v2", good), ReasonKind},
		"desired version not valid": {reviewOf("a/b/c", good), ReasonKind},
		"desired group not known":   {reviewOf("other.koodam.dev/v1beta1", good), ReasonKind},
		"kind not known":            {reviewOf(beta, good, newObject(alpha, "Other", object{})), ReasonKind},
		"version not known":         {reviewOf(beta, good, newObject("core.koodam.dev/v0", "Agent", object{})), ReasonKind},
		"no kind":                   {reviewOf(beta, good, object{keyVersion: alpha, "spec": object{"x": marker}}), ReasonKind},
		"a list kind":               {reviewOf(beta, good, newObject(alpha, "AgentList", object{})), ReasonKind},
		"a machinery kind":          {reviewOf(beta, good, newObject(alpha, "ListOptions", object{})), ReasonKind},
		"spec with a wrong type":    {reviewOf(beta, good, newObject(alpha, "TestKey", object{oldID: []any{marker}})), ReasonObject},
		"not an object":             {reviewOf(beta, good, marker), ReasonObject},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			resp, reports := convert(t, c.review)
			if resp.Result.Status != metav1.StatusFailure || resp.Result.Message != failure+c.reason ||
				len(resp.ConvertedObjects) != 0 || !reflect.DeepEqual(reports, []string{c.reason}) {
				t.Errorf("got %+v with %d objects and the reports %v", resp.Result, len(resp.ConvertedObjects), reports)
			}
		})
	}
}

func TestBadRequests(t *testing.T) {
	good, err := json.Marshal(reviewOf(beta, newObject(alpha, "TestKey", object{oldID: run})))
	if err != nil {
		t.Fatal(err)
	}
	other := bytes.Replace(good, []byte("ConversionReview"), []byte("AdmissionReview"), 1)
	old := bytes.Replace(good, []byte("apiextensions.k8s.io/v1"), []byte("apiextensions.k8s.io/v1beta1"), 1)
	large := append(bytes.Repeat([]byte(" "), MaxRequestBytes), good...)
	cases := map[string]struct {
		method string
		body   []byte
	}{
		"GET":             {http.MethodGet, good},
		"not JSON":        {http.MethodPost, []byte(marker)},
		"no request":      {http.MethodPost, []byte(`{"apiVersion":"apiextensions.k8s.io/v1","kind":"ConversionReview"}`)},
		"other kind":      {http.MethodPost, other},
		"old API version": {http.MethodPost, old},
		"too large":       {http.MethodPost, large},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			code, body, reports := call(t, c.method, c.body)
			if code != http.StatusBadRequest || strings.TrimSpace(body) != ReasonRequest ||
				!reflect.DeepEqual(reports, []string{ReasonRequest}) {
				t.Errorf("got the code %d, the body %q and the reports %v", code, body, reports)
			}
		})
	}
}

// TestPanicIsAFailure uses a request with a nil scheme, which makes the conversion panic.
func TestPanicIsAFailure(t *testing.T) {
	var reports []string
	h := &handler{refused: func(reason string) { reports = append(reports, reason) }}
	resp := h.convert(&apix.ConversionRequest{UID: uid, DesiredAPIVersion: beta,
		Objects: []runtime.RawExtension{{Raw: []byte(`{}`)}}})
	if resp.Result.Status != metav1.StatusFailure || resp.Result.Message != failure+ReasonInternal ||
		resp.UID != uid || !reflect.DeepEqual(reports, []string{ReasonInternal}) {
		t.Errorf("got %+v and the reports %v", resp, reports)
	}
}
