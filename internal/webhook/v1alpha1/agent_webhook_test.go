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
	"encoding/json"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	uid = "0b8f3c2e-7a41-4c55-9d1e-5f2a6b7c8d90"
	// marker is in each request. No response can contain it.
	marker = "koodam-input-marker"
	create = "CREATE"
	update = "UPDATE"
	// httpTpl has the reserved name on the main container. webTpl has no reserved name.
	httpTpl = `,"podTemplate":{"spec":{"containers":[{"name":"agent","ports":[{"name":"http","containerPort":8080}]}]}}`
	webTpl  = `,"podTemplate":{"spec":{"containers":[{"name":"agent","ports":[{"name":"web","containerPort":8080}]}]}}`
	port    = `,"ingressPort":8080`
)

// agent returns an Agent v1alpha1 with the marker in its name and an annotation. extra is added to
// the spec.
func agent(extra string) string {
	return `{"apiVersion":"core.koodam.dev/v1alpha1","kind":"Agent","metadata":{"name":"` + marker +
		`","namespace":"n","annotations":{"note":"` + marker + `"}},"spec":{"image":"img","command":["run"],` +
		`"model":"m","budget":"1"` + extra + `}}`
}

// body returns an AdmissionReview v1. fields is added to the request. An empty object or old is
// not in the request.
func body(op, obj, old, fields string) string {
	r := `{"uid":"` + uid + `","kind":{"group":"core.koodam.dev","version":"v1alpha1","kind":"Agent"},` +
		`"resource":{"group":"core.koodam.dev","version":"v1alpha1","resource":"agents"},` +
		`"name":"` + marker + `","namespace":"n","operation":"` + op + `","userInfo":{"username":"` + marker + `"}` + fields
	if obj != "" {
		r += `,"object":` + obj
	}
	if old != "" {
		r += `,"oldObject":` + old
	}
	return `{"apiVersion":"admission.k8s.io/v1","kind":"AdmissionReview","request":` + r + `}}`
}

// want returns the only permitted response: a new AdmissionReview v1 with the UID, the decision
// and a fixed status. It has no patch, no warnings and no audit data.
func want(id types.UID, status *metav1.Status) admissionv1.AdmissionReview {
	return admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"},
		Response: &admissionv1.AdmissionResponse{UID: id, Allowed: status == nil, Result: status},
	}
}

// expectReview checks the exact response and that its wire form has no marker from the input.
func expectReview(in string, id types.UID, status *metav1.Status) {
	got := Review([]byte(in))
	Expect(*got).To(Equal(want(id, status)))
	out, err := json.Marshal(got)
	Expect(err).NotTo(HaveOccurred())
	Expect(strings.Contains(string(out), marker)).To(BeFalse(), "the response contains input data")
	Expect(strings.Contains(string(out), "json:")).To(BeFalse(), "the response contains a decoder error")
}

var _ = Describe("Agent ingress admission", func() {
	var (
		invalid  = invalidRequest()
		reserved = forbidden(MessageReservedPort)
		bounds   = forbidden(MessagePortRange)
		valid    = body(create, agent(port+webTpl), "", "")
	)

	It("has fixed statuses", func() {
		Expect(*invalid).To(Equal(metav1.Status{Status: metav1.StatusFailure, Code: 400,
			Reason: metav1.StatusReasonBadRequest, Message: MessageInvalidRequest}))
		Expect(*reserved).To(Equal(metav1.Status{Status: metav1.StatusFailure, Code: 403,
			Reason: metav1.StatusReasonForbidden, Message: MessageReservedPort}))
	})

	DescribeTable("decides valid CREATE and UPDATE requests",
		func(in string, status *metav1.Status) { expectReview(in, uid, status) },
		Entry("CREATE with an ingress port", valid, nil),
		Entry("CREATE without an ingress port keeps the legacy http name", body(create, agent(httpTpl), "", ""), nil),
		Entry("CREATE with exact original kind and resource", body(create, agent(port+webTpl), "",
			`,"requestKind":{"group":"core.koodam.dev","version":"v1alpha1","kind":"Agent"},`+
				`"requestResource":{"group":"core.koodam.dev","version":"v1alpha1","resource":"agents"}`), nil),
		Entry("CREATE with http on the main container", body(create, agent(port+httpTpl), "", ""), reserved),
		Entry("CREATE with http on an init container", body(create, agent(port+
			`,"podTemplate":{"spec":{"initContainers":[{"name":"i","ports":[{"name":"http","containerPort":1}]}]}}`),
			"", ""), reserved),
		Entry("CREATE with port 0", body(create, agent(`,"ingressPort":0`), "", ""), bounds),
		Entry("CREATE with port 65536", body(create, agent(`,"ingressPort":65536`), "", ""), bounds),
		Entry("UPDATE with a changed template is not frozen", body(update, agent(port+webTpl), agent(port), ""), nil),
		Entry("UPDATE checks only the new template", body(update, agent(port+webTpl), agent(port+httpTpl), ""), nil),
		Entry("UPDATE with http in the new template", body(update, agent(port+httpTpl), agent(port+webTpl), ""), reserved),
		Entry("UPDATE that removes the port is left to CEL", body(update, agent(httpTpl), agent(port+webTpl), ""), nil),
	)

	DescribeTable("refuses a bad envelope with no UID",
		func(in string) { expectReview(in, "", invalid) },
		Entry("empty body", ""),
		Entry("null", "null"),
		Entry("not JSON", marker),
		Entry("trailing data", valid+marker),
		Entry("no apiVersion or kind", strings.Replace(valid,
			`"apiVersion":"admission.k8s.io/v1","kind":"AdmissionReview",`, "", 1)),
		Entry("v1beta1", strings.Replace(valid, "admission.k8s.io/v1", "admission.k8s.io/v1beta1", 1)),
		Entry("other kind", strings.Replace(valid, `"AdmissionReview"`, `"Agent"`, 1)),
		Entry("unknown envelope field", strings.Replace(valid, `"request":`, `"`+marker+`":1,"request":`, 1)),
		Entry("duplicate envelope field", strings.Replace(valid, `"request":`, `"kind":"AdmissionReview","request":`, 1)),
		Entry("no request", `{"apiVersion":"admission.k8s.io/v1","kind":"AdmissionReview"}`),
		Entry("request with a response", strings.Replace(valid, `"request":`, `"response":{"uid":"`+uid+`"},"request":`, 1)),
		Entry("no UID", strings.Replace(valid, `"uid":"`+uid+`",`, "", 1)),
		Entry("UID type mismatch", strings.Replace(valid, `"uid":"`+uid+`"`, `"uid":7`, 1)),
		Entry("unknown request field", body(create, agent(port), "", `,"`+marker+`":1`)),
		Entry("duplicate request field", body(create, agent(port), "", `,"operation":"CREATE"`)),
		Entry("request type mismatch", body(create, agent(port), "", `,"dryRun":"`+marker+`"`)),
	)

	DescribeTable("refuses a bad request with the UID",
		func(in string) { expectReview(in, uid, invalid) },
		Entry("DELETE", body("DELETE", "", agent(port), "")),
		Entry("CONNECT", body("CONNECT", agent(port), "", "")),
		Entry("other kind", strings.Replace(valid, `"kind":"Agent"}`, `"kind":"Workspace"}`, 1)),
		Entry("other resource", strings.Replace(valid, `"resource":"agents"`, `"resource":"workspaces"`, 1)),
		Entry("other version", strings.Replace(valid, `"version":"v1alpha1"`, `"version":"v1"`, 1)),
		Entry("status subresource", body(update, agent(port), agent(port), `,"subResource":"status"`)),
		Entry("converted kind", body(create, agent(port), "",
			`,"requestKind":{"group":"core.koodam.dev","version":"v1beta1","kind":"Agent"}`)),
		Entry("converted resource", body(create, agent(port), "",
			`,"requestResource":{"group":"core.koodam.dev","version":"v1beta1","resource":"agents"}`)),
		Entry("request subresource", body(create, agent(port), "", `,"requestSubResource":"status"`)),
		Entry("CREATE with no object", body(create, "", "", "")),
		Entry("CREATE with a null object", body(create, "null", "", "")),
		Entry("CREATE with an old object", body(create, agent(port), agent(port), "")),
		Entry("UPDATE with no old object", body(update, agent(port), "", "")),
		Entry("object with no kind", body(create, strings.Replace(agent(port), `"kind":"Agent",`, "", 1), "", "")),
		Entry("object of other kind", body(create, strings.Replace(agent(port), `"Agent"`, `"Workspace"`, 1), "", "")),
		Entry("object of other version", body(create, strings.Replace(agent(port), "v1alpha1", "v1beta1", 1), "", "")),
		Entry("object is not an object", body(create, `"`+marker+`"`, "", "")),
		Entry("unknown object field", body(create, strings.Replace(agent(port), `"spec":`, `"`+marker+`":1,"spec":`, 1), "", "")),
		Entry("duplicate object field", body(create, strings.Replace(agent(port), `"spec":`, `"kind":"Agent","spec":`, 1), "", "")),
		Entry("unknown spec field", body(create, agent(port+`,"`+marker+`":1`), "", "")),
		Entry("duplicate spec field", body(create, agent(port+`,"ingressPort":9090`), "", "")),
		Entry("unknown template field", body(create, agent(port+
			`,"podTemplate":{"spec":{"containers":[{"name":"agent","`+marker+`":1}]}}`), "", "")),
		Entry("duplicate template field", body(create, agent(port+
			`,"podTemplate":{"spec":{"containers":[{"name":"agent","name":"`+marker+`"}]}}`), "", "")),
		Entry("unknown field with other case", body(create, agent(port+`,"IngressPort":8080`), "", "")),
		Entry("ingress port type mismatch", body(create, agent(`,"ingressPort":"8080"`), "", "")),
		Entry("unknown old object field", body(update, agent(port), agent(port+`,"`+marker+`":1`), "")),
		Entry("duplicate old object field", body(update, agent(port), agent(port+`,"ingressPort":9090`), "")),
	)

	It("does not apply defaults to the decoded Agent", func() {
		a, ok := decodeAgent([]byte(agent(port + `,"harness":{"name":"codex"}`)))
		Expect(ok).To(BeTrue())
		Expect(a.Spec.DeadlineSeconds).To(BeZero())
		Expect(a.Spec.Harness.Libc).To(BeEmpty())
	})
})
