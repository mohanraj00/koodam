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
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/util/validation"
)

// The wire constants of docs/ingress-spans.md.
const (
	spanVersion = 1
	spanKind    = "ingress"
	spanOther   = "OTHER"
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

// errSpanInvalid is the only error of encodeSpan. It holds no input text.
var errSpanInvalid = errors.New("ingress span: the record is not valid")

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

// spanCaller is group C. The handler fills it only from a successful TokenReview.
type spanCaller struct{ namespace, serviceAccount, uid string }

// spanTarget is group T. The handler fills it only from a successful final uncached target check.
type spanTarget struct{ namespace, agent, agentUID, run, runUID, pod, podUID string }

// spanObservation is what the handler observed in 1 call. It holds no request, review, body or
// error object. A nil group or trace is absent from the line.
type spanObservation struct {
	start    time.Time // time.Now() at handler entry, with its monotonic reading
	method   string    // the request method, written only as spanMethod(method)
	outcome  spanOutcome
	status   int  // the final status that the handler selected, or 0 if none
	ctxErr   bool // the request context had an error at classification
	trace    *[16]byte
	caller   *spanCaller
	target   *spanTarget
	bytesOut int64
	bytesIn  int64
}

// spanLine is the wire form. It has the 22 fields of the contract and no other field.
type spanLine struct {
	V                    int     `json:"v"`
	Kind                 string  `json:"kind"`
	Time                 string  `json:"time"`
	TraceID              string  `json:"traceID,omitempty"`
	ProxyNamespace       string  `json:"proxyNamespace"`
	TargetNamespace      string  `json:"targetNamespace,omitempty"`
	TargetAgent          string  `json:"targetAgent,omitempty"`
	TargetAgentUID       string  `json:"targetAgentUID,omitempty"`
	TargetRun            string  `json:"targetRun,omitempty"`
	TargetRunUID         string  `json:"targetRunUID,omitempty"`
	TargetPod            string  `json:"targetPod,omitempty"`
	TargetPodUID         string  `json:"targetPodUID,omitempty"`
	CallerNamespace      string  `json:"callerNamespace,omitempty"`
	CallerServiceAccount string  `json:"callerServiceAccount,omitempty"`
	CallerUID            string  `json:"callerUID,omitempty"`
	Method               string  `json:"method"`
	Status               int     `json:"status,omitempty"`
	BytesOut             int64   `json:"bytesOut"`
	BytesIn              int64   `json:"bytesIn"`
	DurationMs           float64 `json:"durationMs"`
	Result               string  `json:"result"`
	Reason               string  `json:"reason,omitempty"`
}

// encodeSpan returns 1 JSON line with a final newline. end is time.Now() at span assembly. Any
// invalid input gives errSpanInvalid and no line.
func encodeSpan(proxyNamespace string, o spanObservation, end time.Time) ([]byte, error) {
	c, ok := classifySpan(o.outcome, o.status, o.ctxErr)
	ms, msOK := elapsedMs(o.start, end)
	year := o.start.UTC().Year()
	if !ok || !msOK || !groupsAllowed(o) || !isNamespace(proxyNamespace) ||
		o.bytesOut < 0 || o.bytesIn < 0 || year < 1 || year > 9999 {
		return nil, errSpanInvalid
	}
	line := spanLine{
		V: spanVersion, Kind: spanKind, Time: o.start.UTC().Format(time.RFC3339Nano),
		ProxyNamespace: proxyNamespace, Method: spanMethod(o.method), Status: c.status,
		BytesOut: o.bytesOut, BytesIn: o.bytesIn, DurationMs: ms, Result: c.result, Reason: c.reason,
	}
	if o.trace != nil {
		if *o.trace == ([16]byte{}) {
			return nil, errSpanInvalid
		}
		line.TraceID = hex.EncodeToString(o.trace[:])
	}
	if p := o.caller; p != nil {
		if !isNamespace(p.namespace) || !isName(p.serviceAccount) || !isUID(p.uid) {
			return nil, errSpanInvalid
		}
		line.CallerNamespace, line.CallerServiceAccount, line.CallerUID = p.namespace, p.serviceAccount, p.uid
	}
	if p := o.target; p != nil {
		if !isNamespace(p.namespace) || !isName(p.agent) || !isUID(p.agentUID) || !isName(p.run) ||
			!isUID(p.runUID) || !isName(p.pod) || !isUID(p.podUID) {
			return nil, errSpanInvalid
		}
		line.TargetNamespace, line.TargetAgent, line.TargetAgentUID = p.namespace, p.agent, p.agentUID
		line.TargetRun, line.TargetRunUID, line.TargetPod, line.TargetPodUID = p.run, p.runUID, p.pod, p.podUID
	}
	b, err := json.Marshal(line)
	if err != nil {
		return nil, errSpanInvalid
	}
	return append(b, '\n'), nil
}

// groupsAllowed applies the presence rules of the contract to each outcome. A target needs a
// caller, because authorization runs before the target lookup. Body reads need a target.
func groupsAllowed(o spanObservation) bool {
	if (o.target != nil && o.caller == nil) || (o.target == nil && (o.bytesOut != 0 || o.bytesIn != 0)) ||
		(o.method == http.MethodHead && o.bytesIn != 0) {
		return false
	}
	switch o.outcome {
	case outcomeOK, outcomeStream:
		return o.target != nil && o.trace != nil
	case outcomeUpstream:
		return o.target != nil && o.trace != nil && o.bytesIn == 0
	case outcomeCancelled:
		return true // each group is present only if its helper succeeded before the stop
	case outcomeAuthentication:
		return o.caller == nil && o.target == nil
	case outcomeAuthorization, outcomeTargetAbsent, outcomeTargetUnavailable:
		return o.caller != nil && o.target == nil
	case outcomeTrace, outcomeEntropy:
		return o.trace == nil && o.target == nil
	}
	return o.target == nil
}

// elapsedMs uses the monotonic readings of start and end. It refuses a time without a monotonic
// reading, because a wall clock step can make the difference wrong.
func elapsedMs(start, end time.Time) (float64, bool) {
	// The == on [1]time.Time compares all fields of time.Time, also the monotonic reading. Round(0)
	// removes only the monotonic reading. Thus a time is equal to its Round(0) copy only if it has no
	// monotonic reading. Do not use time.Time.Equal here: it compares only the instant, which is the
	// same in the two values, and then the check cannot find the monotonic reading.
	if [1]time.Time{start} == [1]time.Time{start.Round(0)} || [1]time.Time{end} == [1]time.Time{end.Round(0)} {
		return 0, false
	}
	d := end.Sub(start)
	ms := float64(d) / float64(time.Millisecond)
	if d < 0 || math.IsNaN(ms) || math.IsInf(ms, 0) {
		return 0, false
	}
	return ms, true
}

func isNamespace(s string) bool { return len(validation.IsDNS1123Label(s)) == 0 }
func isName(s string) bool      { return len(validation.IsDNS1123Subdomain(s)) == 0 }

// isUID accepts the opaque UID that the API returned. Its form does not prove where it came from.
func isUID(s string) bool { return s != "" && utf8.ValidString(s) }

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
