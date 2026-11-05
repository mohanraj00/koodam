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

package ingress

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"maps"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testProxyNS = "koodam-system"
	uidCaller   = "00000000-0000-4000-8000-000000000001"
	uidAgent    = "00000000-0000-4000-8000-000000000002"
	uidRun      = "00000000-0000-4000-8000-000000000003"
	uidPod      = "00000000-0000-4000-8000-000000000004"
	uidOpaque   = "Opaque-UID_1"
)

// The contract values of result and reason. The tests do not use the production constants, so
// a change to a production wire value makes a test fail.
const (
	wantOK             = "ok"
	wantDenied         = "denied"
	wantError          = "error"
	wantRoute          = "route"
	wantSpanTrace      = "trace"
	wantMethod         = "method"
	wantAuthentication = "authentication"
	wantAuthorization  = "authorization"
	wantReview         = "review"
	wantEntropy        = "entropy"
	wantTarget         = "target"
	wantUpstream       = "upstream"
	wantStream         = "stream"
	wantCancelled      = "cancelled"
)

var (
	baseKeys   = []string{"v", "kind", "time", "proxyNamespace", litSpanMethodKey, "bytesOut", "bytesIn", "durationMs", "result"}
	callerKeys = []string{"callerNamespace", "callerServiceAccount", "callerUID"}
	targetKeys = []string{
		"targetNamespace", "targetAgent", "targetAgentUID", "targetRun", "targetRunUID", "targetPod", "targetPodUID",
	}
	traceKeys = []string{"traceID"}
)

func testCaller() *spanCaller { return new(spanCaller{"caller-ns", "caller-sa", uidCaller}) }

func testTarget() *spanTarget {
	return new(spanTarget{"run-ns", "agent-a", uidAgent, "agent-a-run", uidRun, "agent-a-run-pod", uidPod})
}

func testTrace() *[16]byte { return new([16]byte{0x0a, 1, 2, 3}) }

func okObservation() spanObservation {
	return spanObservation{
		start: time.Now(), method: http.MethodGet, outcome: outcomeOK, status: http.StatusOK,
		trace: testTrace(), caller: testCaller(), target: testTarget(), bytesOut: 3, bytesIn: 5,
	}
}

// spanCanary is a random value that no test prints.
func spanCanary() string { return rand.Text() }

// decodeLine checks the line form and returns the object.
func decodeLine(t *testing.T, b []byte) map[string]any {
	t.Helper()
	if bytes.IndexByte(b, '\n') != len(b)-1 {
		t.Fatal("the span is not 1 line with a final newline")
	}
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&m); err != nil || dec.More() {
		t.Fatal("the span is not 1 JSON object")
	}
	for _, v := range m {
		if v == nil || v == "" {
			t.Fatal("the span has a null or empty value")
		}
	}
	return m
}

func wantKeys(t *testing.T, m map[string]any, groups ...[]string) {
	t.Helper()
	want := slices.Concat(groups...)
	slices.Sort(want)
	if got := slices.Sorted(maps.Keys(m)); !slices.Equal(got, want) {
		t.Fatal("the span field set is not the expected set")
	}
}

func TestSpanFieldSetOK(t *testing.T) {
	b, err := encodeSpan(testProxyNS, okObservation(), time.Now())
	if err != nil {
		t.Fatal("a valid span gave an error")
	}
	m := decodeLine(t, b)
	wantKeys(t, m, baseKeys, callerKeys, targetKeys, traceKeys, []string{litSpanStatusKey})
	if m["v"] != float64(1) || m["kind"] != wantSpanKind || m["result"] != "ok" || m[litSpanStatusKey] != float64(200) ||
		m[litSpanMethodKey] != wantSpanGET || m["bytesOut"] != float64(3) || m["bytesIn"] != float64(5) ||
		m["traceID"] != "0a010203000000000000000000000000" || m["proxyNamespace"] != testProxyNS ||
		m["callerUID"] != uidCaller || m["targetRun"] != "agent-a-run" || m["targetPodUID"] != uidPod {
		t.Fatal("a span value is not the expected value")
	}
	ts, _ := m["time"].(string)
	if _, err := time.Parse(time.RFC3339Nano, ts); err != nil || !strings.HasSuffix(ts, "Z") {
		t.Fatal("time is not RFC 3339 in UTC")
	}
	if d, _ := m["durationMs"].(float64); d < 0 {
		t.Fatal("durationMs is negative")
	}
}

func TestSpanProxyStatus(t *testing.T) {
	for o, want := range map[spanOutcome]int{
		outcomeRoute: 400, outcomeTrace: 400, outcomeMethod: 405, outcomeAuthentication: 401,
		outcomeAuthorization: 403, outcomeReview: 503, outcomeEntropy: 503, outcomeTargetAbsent: 404,
		outcomeTargetUnavailable: 503, outcomeUpstream: 502, outcomeOK: 0, outcomeStream: 0, outcomeCancelled: 0,
	} {
		if proxyStatus(o) != want {
			t.Fatal("a proxy status is not the contract value")
		}
	}
}

func TestSpanRefusesInvalidRecords(t *testing.T) {
	refusal := func(o *spanObservation, out spanOutcome, status int) {
		*o = spanObservation{start: o.start, outcome: out, status: status, caller: o.caller}
	}
	for _, tc := range []struct {
		name   string
		change func(*spanObservation)
	}{
		{"no outcome", func(o *spanObservation) { o.outcome = outcomeNone }},
		{"unknown outcome", func(o *spanObservation) { o.outcome = spanOutcome(99) }},
		{"ok without status", func(o *spanObservation) { o.status = 0 }},
		{"informational status", func(o *spanObservation) { o.status = 101 }},
		{"status too large", func(o *spanObservation) { o.status = 1000 }},
		{"upstream with backend status", func(o *spanObservation) { o.outcome, o.bytesIn = outcomeUpstream, 0 }},
		{"upstream with response bytes", func(o *spanObservation) { o.outcome, o.status = outcomeUpstream, 0 }},
		{"route with other status", func(o *spanObservation) { refusal(o, outcomeRoute, 401) }},
		{"authorization with other status", func(o *spanObservation) { refusal(o, outcomeAuthorization, 503) }},
		{"review with refusal status", func(o *spanObservation) { refusal(o, outcomeReview, 401) }},
		{"target absent with 503", func(o *spanObservation) { refusal(o, outcomeTargetAbsent, 503) }},
		{"target unavailable with 404", func(o *spanObservation) { refusal(o, outcomeTargetUnavailable, 404) }},
		{"refusal with bytes", func(o *spanObservation) {
			*o = spanObservation{start: o.start, outcome: outcomeRoute, bytesOut: 1}
		}},
		{"cancelled without context error", func(o *spanObservation) { o.outcome = outcomeCancelled }},
		{"cancelled with informational status", func(o *spanObservation) {
			o.outcome, o.ctxErr, o.status = outcomeCancelled, true, 101
		}},
		{"cancelled bytes without target", func(o *spanObservation) {
			o.outcome, o.ctxErr, o.target = outcomeCancelled, true, nil
		}},
		{"cancelled target without caller", func(o *spanObservation) {
			o.outcome, o.ctxErr, o.caller = outcomeCancelled, true, nil
		}},
		{"target without caller", func(o *spanObservation) { o.caller = nil }},
		{"exchange without target", func(o *spanObservation) { o.target = nil }},
		{"exchange without trace", func(o *spanObservation) { o.trace = nil }},
		{"caller on authentication", func(o *spanObservation) {
			o.outcome, o.status, o.target, o.bytesOut, o.bytesIn = outcomeAuthentication, 0, nil, 0, 0
		}},
		{"no caller on authorization", func(o *spanObservation) {
			*o = spanObservation{start: o.start, outcome: outcomeAuthorization}
		}},
		{"target on target absent", func(o *spanObservation) {
			o.outcome, o.status, o.bytesOut, o.bytesIn = outcomeTargetAbsent, 0, 0, 0
		}},
		{"trace on trace refusal", func(o *spanObservation) {
			*o = spanObservation{start: o.start, outcome: outcomeTrace, trace: o.trace}
		}},
		{"zero trace", func(o *spanObservation) { o.trace = new([16]byte{}) }},
		{"partial caller", func(o *spanObservation) { o.caller = new(spanCaller{"caller-ns", "caller-sa", ""}) }},
		{"empty target uid", func(o *spanObservation) { o.target.runUID = "" }},
		{"invalid utf8 uid", func(o *spanObservation) { o.caller.uid = "\xff" }},
		{"partial target", func(o *spanObservation) { o.target.pod = "" }},
		{"invalid name", func(o *spanObservation) { o.target.agent = "Agent_A" }},
		{"negative bytesOut", func(o *spanObservation) { o.bytesOut = -1 }},
		{"negative bytesIn", func(o *spanObservation) { o.bytesIn = -1 }},
		{"head with response bytes", func(o *spanObservation) { o.method = http.MethodHead }},
		{"wall clock start", func(o *spanObservation) { o.start = o.start.Round(0) }},
		{"negative duration", func(o *spanObservation) { o.start = o.start.Add(time.Hour) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := okObservation()
			tc.change(&o)
			if b, err := encodeSpan(testProxyNS, o, time.Now()); !errors.Is(err, errSpanInvalid) || b != nil {
				t.Fatal("an invalid record did not give the fixed error")
			}
		})
	}
	if _, err := encodeSpan("", okObservation(), time.Now()); !errors.Is(err, errSpanInvalid) {
		t.Fatal("an empty proxy namespace did not give the fixed error")
	}
	if _, err := encodeSpan(testProxyNS, okObservation(), time.Now().Round(0)); !errors.Is(err, errSpanInvalid) {
		t.Fatal("a wall clock end did not give the fixed error")
	}
}

// The API returns an opaque UID. The encoder keeps it as it is.
func TestSpanKeepsOpaqueUID(t *testing.T) {
	o := okObservation()
	o.caller.uid, o.target.agentUID, o.target.runUID, o.target.podUID = uidOpaque, uidOpaque, uidOpaque, uidOpaque
	b, err := encodeSpan(testProxyNS, o, time.Now())
	if err != nil {
		t.Fatal("an opaque uid gave an error")
	}
	m := decodeLine(t, b)
	for _, k := range []string{"callerUID", "targetAgentUID", "targetRunUID", "targetPodUID"} {
		if m[k] != uidOpaque {
			t.Fatal("an opaque uid was not kept")
		}
	}
}

func TestSpanMethodSet(t *testing.T) {
	for _, m := range []string{wantSpanGET, "HEAD", wantSpanPOST, "PUT", "PATCH", "DELETE", "OPTIONS"} {
		if spanMethod(m) != m {
			t.Fatal("an approved method was not kept")
		}
	}
	secret := spanCanary()
	for _, m := range []string{"CONNECT", litLowerGet, "TRACE", "", secret} {
		if spanMethod(m) != wantSpanOther {
			t.Fatal("a method outside the set was not OTHER")
		}
	}
	o := okObservation()
	o.method = secret
	b, err := encodeSpan(testProxyNS, o, time.Now())
	if err != nil || decodeLine(t, b)[litSpanMethodKey] != wantSpanOther || bytes.Contains(b, []byte(secret)) {
		t.Fatal("the raw method text reached the span")
	}
}

func TestSpanErrorHoldsNoInput(t *testing.T) {
	secret := spanCanary()
	o := okObservation()
	o.caller = new(spanCaller{secret, secret, secret})
	_, err := encodeSpan(secret, o, time.Now())
	if !errors.Is(err, errSpanInvalid) || strings.Contains(err.Error(), secret) {
		t.Fatal("the error is not the fixed error")
	}
}

func TestSpanCounter(t *testing.T) {
	var c spanCounter
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			r := countingReader{r: bytes.NewReader(make([]byte, 1000)), c: &c}
			buf := make([]byte, 7)
			for {
				if _, err := r.Read(buf); err != nil {
					return
				}
			}
		})
	}
	wg.Wait()
	if c.load() != 8000 {
		t.Fatal("the counter lost bytes")
	}
	c.add(-5)
	c.add(0)
	if c.load() != 8000 {
		t.Fatal("the counter took a nonpositive count")
	}
	c.n.Store(math.MaxInt64 - 1)
	c.add(10)
	if c.load() != math.MaxInt64 {
		t.Fatal("the counter did not stop at the maximum")
	}
}
