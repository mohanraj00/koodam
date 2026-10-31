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
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// approvedBodyBytes is the 7 MiB body bound of the approved design.
const approvedBodyBytes = 7 * 1024 * 1024

// streamReader returns data and then err. It has no length, thus a request with it has no
// Content-Length.
type streamReader struct {
	data string
	err  error
}

func (s *streamReader) Read(p []byte) (int, error) {
	if s.data == "" {
		return 0, s.err
	}
	n := copy(p, s.data)
	s.data = s.data[n:]
	return n, nil
}

// request returns a request with the JSON media type. Its target and a header contain the marker.
func request(method string, in io.Reader) *http.Request {
	r := httptest.NewRequest(method, "/validate?"+marker+"="+marker, in)
	r.Header.Set(headerContentType, mediaTypeJSON)
	r.Header.Set("X-Note", marker)
	return r
}

// serve runs h for r and checks that no part of the response contains the marker. The failure
// messages are fixed, so that they do not show input data.
func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	Expect(strings.Contains(strings.ToLower(rec.Body.String()), marker)).To(BeFalse(),
		"the response body contains input data")
	for k, v := range rec.Header() {
		Expect(strings.Contains(strings.ToLower(k+strings.Join(v, ",")), marker)).To(BeFalse(),
			"a response header contains input data")
	}
	return rec
}

// expectAdmission checks that the response is exactly the JSON of want(id, status).
func expectAdmission(rec *httptest.ResponseRecorder, id types.UID, status *metav1.Status) {
	exp := want(id, status)
	out, err := json.Marshal(&exp)
	Expect(err).NotTo(HaveOccurred())
	Expect(rec.Code).To(Equal(http.StatusOK))
	Expect(rec.Header().Get(headerContentType)).To(Equal(mediaTypeJSON))
	Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(len(out))))
	same := rec.Body.String() == string(out)
	Expect(same).To(BeTrue(), "the response is not the expected AdmissionReview")
}

// expectFixedError checks a plain text protocol error with the fixed text.
func expectFixedError(rec *httptest.ResponseRecorder, code int, text string) {
	Expect(rec.Code).To(Equal(code))
	Expect(rec.Header().Get(headerContentType)).To(Equal("text/plain; charset=utf-8"))
	same := rec.Body.String() == text+"\n"
	Expect(same).To(BeTrue(), "the error text is not fixed")
}

var _ = Describe("Agent ingress admission HTTP handler", func() {
	var (
		invalid  = invalidRequest()
		reserved = forbidden(MessageReservedPort)
		valid    = body(create, agent(port+webTpl), "", "")
		exact    = valid + strings.Repeat(" ", approvedBodyBytes-len(valid))
		over     = exact + " "
	)

	DescribeTable("returns the strict Review decision",
		func(in string, id types.UID, status *metav1.Status) {
			expectAdmission(serve(NewHandler(), request(http.MethodPost, strings.NewReader(in))), id, status)
		},
		Entry("valid CREATE", valid, types.UID(uid), nil),
		Entry("valid UPDATE", body(update, agent(port+webTpl), agent(port), ""), types.UID(uid), nil),
		Entry("http on the main container", body(create, agent(port+httpTpl), "", ""), types.UID(uid), reserved),
		Entry("http on an init container", body(create, agent(port+
			`,"podTemplate":{"spec":{"initContainers":[{"name":"i","ports":[{"name":"http","containerPort":1}]}]}}`),
			"", ""), types.UID(uid), reserved),
		Entry("unknown spec field", body(create, agent(port+`,"`+marker+`":1`), "", ""), types.UID(uid), invalid),
		Entry("duplicate spec field", body(create, agent(port+`,"ingressPort":9090`), "", ""), types.UID(uid), invalid),
		Entry("DELETE", body("DELETE", "", agent(port), ""), types.UID(uid), invalid),
		Entry("empty body", "", types.UID(""), invalid),
		Entry("not JSON", marker, types.UID(""), invalid),
		Entry("unknown envelope field", strings.Replace(valid, `"request":`, `"`+marker+`":1,"request":`, 1),
			types.UID(""), invalid),
		Entry("v1beta1 envelope", strings.Replace(valid, "admission.k8s.io/v1", "admission.k8s.io/v1beta1", 1),
			types.UID(""), invalid),
	)

	DescribeTable("accepts the JSON media type with a permitted form",
		func(value string) {
			r := request(http.MethodPost, strings.NewReader(valid))
			r.Header.Set(headerContentType, value)
			expectAdmission(serve(NewHandler(), r), uid, nil)
		},
		Entry("UTF-8 charset", "application/json; charset=UTF-8"),
		Entry("upper case", "Application/JSON"),
	)

	DescribeTable("refuses other media types",
		func(values []string) {
			r := request(http.MethodPost, strings.NewReader(valid))
			r.Header.Del(headerContentType)
			for _, v := range values {
				r.Header.Add(headerContentType, v)
			}
			expectFixedError(serve(NewHandler(), r), http.StatusUnsupportedMediaType, textMediaType)
		},
		Entry("no Content-Type", []string(nil)),
		Entry("empty", []string{""}),
		Entry("text", []string{"text/" + marker}),
		Entry("other suffix", []string{mediaTypeJSON + "x"}),
		Entry("other charset", []string{mediaTypeJSON + "; charset=latin1"}),
		Entry("other parameter", []string{mediaTypeJSON + "; " + marker + "=1"}),
		Entry("malformed", []string{mediaTypeJSON + "; =" + marker}),
		Entry("two values", []string{mediaTypeJSON, mediaTypeJSON}),
	)

	DescribeTable("refuses other methods",
		func(method string) {
			rec := serve(NewHandler(), request(method, strings.NewReader(valid)))
			expectFixedError(rec, http.StatusMethodNotAllowed, textMethod)
			Expect(rec.Header().Get("Allow")).To(Equal(http.MethodPost))
		},
		Entry("GET", http.MethodGet),
		Entry("PUT", http.MethodPut),
		Entry("PATCH", http.MethodPatch),
		Entry("unknown method", strings.ToUpper(marker)),
	)

	DescribeTable("refuses a body that it cannot read",
		func(in *streamReader) {
			expectFixedError(serve(NewHandler(), request(http.MethodPost, in)), http.StatusBadRequest, textRead)
		},
		Entry("reader error", &streamReader{err: errors.New(marker)}),
		Entry("truncated body", &streamReader{data: valid[:len(valid)/2], err: io.ErrUnexpectedEOF}),
	)

	It("permits a body of exactly 7 MiB, with or without Content-Length", func() {
		size := len(exact)
		Expect(size).To(Equal(approvedBodyBytes))
		expectAdmission(serve(NewHandler(), request(http.MethodPost, strings.NewReader(exact))), uid, nil)
		r := request(http.MethodPost, &streamReader{data: exact, err: io.EOF})
		Expect(r.ContentLength).To(Equal(int64(-1)))
		expectAdmission(serve(NewHandler(), r), uid, nil)
	})

	It("refuses a body one byte over 7 MiB, also with a false Content-Length", func() {
		size := len(over)
		Expect(size).To(Equal(approvedBodyBytes + 1))
		for _, length := range []int64{int64(len(over)), -1, 0, int64(len(valid))} {
			r := request(http.MethodPost, strings.NewReader(over))
			r.ContentLength = length
			expectFixedError(serve(NewHandler(), r), http.StatusRequestEntityTooLarge, textTooLarge)
		}
	})

	DescribeTable("drops a panic value",
		func(value any) {
			h := &handler{review: func([]byte) *admissionv1.AdmissionReview { panic(value) }}
			expectFixedError(serve(h, request(http.MethodPost, strings.NewReader(valid))),
				http.StatusInternalServerError, textInternal)
		},
		Entry("string", marker),
		Entry("error", errors.New(marker)),
		Entry("nil", nil),
		Entry("abort", http.ErrAbortHandler),
	)

	It("refuses to send a missing review", func() {
		h := &handler{review: func([]byte) *admissionv1.AdmissionReview { return nil }}
		expectFixedError(serve(h, request(http.MethodPost, strings.NewReader(valid))),
			http.StatusInternalServerError, textInternal)
	})
})
