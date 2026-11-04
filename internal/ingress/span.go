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

// Package ingress holds the parts of the ingress proxy (#147). This file is the span record of
// docs/ingress-spans.md. No handler uses it yet.
package ingress

import (
	"io"
	"math"
	"net/http"
	"sync/atomic"
)

// The wire constants of docs/ingress-spans.md.
const (
	spanOther = "OTHER"
)

const (
	resultOK     = "ok"
	resultDenied = "denied"
	resultError  = "error"

	reasonRoute          = "route"
	reasonTrace          = "trace"
	reasonMethod         = "method"
	reasonAuthentication = "authentication"
	reasonAuthorization  = "authorization"
	reasonReview         = "review"
	reasonEntropy        = "entropy"
	reasonTarget         = "target"
	reasonUpstream       = "upstream"
	reasonStream         = "stream"
	reasonCancelled      = "cancelled"
)

// spanOutcome is the check or stage at which the handler call stopped. The handler sets it from
// what it observed. It never comes from an error string.
type spanOutcome int

const (
	outcomeNone spanOutcome = iota // not valid
	outcomeOK                      // the exchange and the body copy completed normally
	outcomeRoute
	outcomeTrace
	outcomeMethod
	outcomeAuthentication
	outcomeAuthorization
	outcomeReview
	outcomeEntropy
	outcomeTargetAbsent
	outcomeTargetUnavailable
	outcomeUpstream  // the exchange failed before the response headers
	outcomeStream    // the exchange failed after the handler selected a status
	outcomeCancelled // the call failed or stopped before normal completion, with a context error
)

// spanClass is the result, the reason and the status of a span. status 0 means no status.
type spanClass struct {
	result, reason string
	status         int
}

// spanRefusals holds the class of each refusal with the fixed code of proxyStatus.
var spanRefusals = map[spanOutcome]spanClass{
	outcomeRoute:             {resultDenied, reasonRoute, http.StatusBadRequest},
	outcomeTrace:             {resultDenied, reasonTrace, http.StatusBadRequest},
	outcomeMethod:            {resultDenied, reasonMethod, http.StatusMethodNotAllowed},
	outcomeAuthentication:    {resultDenied, reasonAuthentication, http.StatusUnauthorized},
	outcomeAuthorization:     {resultDenied, reasonAuthorization, http.StatusForbidden},
	outcomeReview:            {resultError, reasonReview, http.StatusServiceUnavailable},
	outcomeEntropy:           {resultError, reasonEntropy, http.StatusServiceUnavailable},
	outcomeTargetAbsent:      {resultError, reasonTarget, http.StatusNotFound},
	outcomeTargetUnavailable: {resultError, reasonTarget, http.StatusServiceUnavailable},
	outcomeUpstream:          {resultError, reasonUpstream, http.StatusBadGateway},
}

// proxyStatus is the fixed code that the handler sends for a refusal or an upstream failure, or
// 0. The span does not use it to make a status: it records only the status that the handler
// actually selected.
func proxyStatus(o spanOutcome) int { return spanRefusals[o].status }

// classifySpan maps an outcome to its class. status is the final status that the handler
// selected, or 0 if it selected none. ctxErr is true if the request context had an error when the
// handler classified the call. ctxErr changes only a failed upstream or stream exchange. It never
// changes a completed exchange or a refusal. A refusal or upstream status must be absent or equal
// to proxyStatus.
func classifySpan(o spanOutcome, status int, ctxErr bool) (spanClass, bool) {
	switch o {
	case outcomeOK:
		return spanClass{resultOK, "", status}, finalStatus(status)
	case outcomeStream:
		if ctxErr {
			return spanClass{resultError, reasonCancelled, status}, finalStatus(status)
		}
		return spanClass{resultError, reasonStream, status}, finalStatus(status)
	case outcomeCancelled:
		return spanClass{resultError, reasonCancelled, status}, ctxErr && (status == 0 || finalStatus(status))
	}
	c, ok := spanRefusals[o]
	if !ok || (status != 0 && status != c.status) {
		return spanClass{}, false
	}
	c.status = status
	if o == outcomeUpstream && ctxErr {
		c.reason = reasonCancelled
	}
	return c, true
}

// finalStatus is true for a final status that net/http can write.
func finalStatus(s int) bool { return s >= 200 && s <= 999 }

// spanMethods is the closed method set. Methods are case-sensitive.
var spanMethods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodPost: true, http.MethodPut: true,
	http.MethodPatch: true, http.MethodDelete: true, http.MethodOptions: true,
}

// spanMethod returns m if it is in the closed set, or OTHER. The raw text is never written.
func spanMethod(m string) string {
	if spanMethods[m] {
		return m
	}
	return spanOther
}

// spanCounter counts the bytes of local body reads. It is safe for concurrent use. It stops at
// math.MaxInt64.
type spanCounter struct{ n atomic.Int64 }

func (c *spanCounter) add(n int) {
	for n > 0 {
		old := c.n.Load()
		next := old + int64(n)
		if next < old {
			next = math.MaxInt64
		}
		if c.n.CompareAndSwap(old, next) {
			return
		}
	}
}

func (c *spanCounter) load() int64 { return c.n.Load() }

// countingReader adds the bytes that each Read returns to c.
type countingReader struct {
	r io.Reader
	c *spanCounter
}

func (r countingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.c.add(min(max(n, 0), len(p)))
	return n, err
}
