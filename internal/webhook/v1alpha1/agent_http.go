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
	"mime"
	"net/http"
	"strconv"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
)

// MaxBodyBytes is the largest request body that the handler accepts: 7 MiB.
const MaxBodyBytes = 7 << 20

const (
	headerContentType = "Content-Type"
	mediaTypeJSON     = "application/json"
)

// The protocol errors have fixed text. They never contain a value from the request.
const (
	textMethod    = "Only the POST method is permitted."
	textMediaType = "Only the media type application/json is permitted."
	textTooLarge  = "The request body is larger than 7 MiB."
	textRead      = "The request body could not be read."
	textInternal  = "The admission handler could not make a response."
)

// NewHandler returns the HTTP handler of the Agent ingress admission. It sends each body that it
// accepts to Review. Nothing registers or starts this handler yet. A later separate Agent
// admission binary serves it. That binary must also contain the process output and load the TLS.
func NewHandler() http.Handler {
	return &handler{review: Review}
}

// handler has review only so that a test can make it fail. NewHandler always sets Review.
type handler struct {
	review func([]byte) *admissionv1.AdmissionReview
}

// reply is a complete response. The handler makes it before it writes a byte.
type reply struct {
	code int
	text string
	body []byte
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	written := false
	// A panic value can contain input. Drop it, do not log it and send fixed text.
	defer func() {
		if recover() != nil && !written {
			http.Error(w, textInternal, http.StatusInternalServerError)
		}
	}()
	rep := h.respond(w, r)
	written = true
	if rep.code != http.StatusOK {
		if rep.code == http.StatusMethodNotAllowed {
			w.Header().Set("Allow", http.MethodPost)
		}
		http.Error(w, rep.text, rep.code)
		return
	}
	hdr := w.Header()
	hdr.Set(headerContentType, mediaTypeJSON)
	hdr.Set("Content-Length", strconv.Itoa(len(rep.body)))
	hdr.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(rep.body)
}

// respond checks the method and the media type, limits the body to MaxBodyBytes and calls review. It
// does not use Content-Length. It writes nothing to w: MaxBytesReader uses w only to close the
// connection after a body that is too large.
func (h *handler) respond(w http.ResponseWriter, r *http.Request) reply {
	if r.Method != http.MethodPost {
		return reply{code: http.StatusMethodNotAllowed, text: textMethod}
	}
	if !jsonMediaType(r.Header.Values(headerContentType)) {
		return reply{code: http.StatusUnsupportedMediaType, text: textMediaType}
	}
	in := r.Body
	if in == nil {
		in = http.NoBody
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, in, MaxBodyBytes))
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return reply{code: http.StatusRequestEntityTooLarge, text: textTooLarge}
	}
	if err != nil {
		return reply{code: http.StatusBadRequest, text: textRead}
	}
	rev := h.review(data)
	out, err := json.Marshal(rev)
	if rev == nil || err != nil {
		return reply{code: http.StatusInternalServerError, text: textInternal}
	}
	return reply{code: http.StatusOK, body: out}
}

// jsonMediaType is true for exactly one Content-Type value with the media type application/json
// and no parameter, or only the parameter charset=utf-8.
func jsonMediaType(values []string) bool {
	if len(values) != 1 {
		return false
	}
	mediaType, params, err := mime.ParseMediaType(values[0])
	if err != nil || mediaType != mediaTypeJSON {
		return false
	}
	charset, ok := params["charset"]
	return len(params) == 0 || (len(params) == 1 && ok && strings.EqualFold(charset, "utf-8"))
}
