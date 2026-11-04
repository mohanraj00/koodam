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

import "testing"

// TestSpanClassifyDirect calls classifySpan without the encoder. A valid class keeps the status
// that the handler selected, also status 0.
func TestSpanClassifyDirect(t *testing.T) {
	for _, tc := range []struct {
		o              spanOutcome
		status         int
		ctxErr         bool
		result, reason string
	}{
		{outcomeRoute, 400, false, wantDenied, wantRoute},
		{outcomeRoute, 0, false, wantDenied, wantRoute},
		{outcomeTrace, 400, false, wantDenied, wantSpanTrace},
		{outcomeMethod, 405, false, wantDenied, wantMethod},
		{outcomeAuthentication, 401, false, wantDenied, wantAuthentication},
		{outcomeAuthorization, 403, false, wantDenied, wantAuthorization},
		{outcomeAuthorization, 403, true, wantDenied, wantAuthorization},
		{outcomeReview, 503, false, wantError, wantReview},
		{outcomeReview, 503, true, wantError, wantReview},
		{outcomeEntropy, 503, false, wantError, wantEntropy},
		{outcomeTargetAbsent, 404, false, wantError, wantTarget},
		{outcomeTargetAbsent, 404, true, wantError, wantTarget},
		{outcomeTargetUnavailable, 503, false, wantError, wantTarget},
		{outcomeTargetUnavailable, 0, false, wantError, wantTarget},
		{outcomeOK, 200, false, wantOK, ""},
		{outcomeOK, 400, false, wantOK, ""},
		{outcomeOK, 401, false, wantOK, ""},
		{outcomeOK, 403, false, wantOK, ""},
		{outcomeOK, 404, false, wantOK, ""},
		{outcomeOK, 405, false, wantOK, ""},
		{outcomeOK, 500, false, wantOK, ""},
		{outcomeOK, 503, false, wantOK, ""},
		{outcomeOK, 200, true, wantOK, ""},
		{outcomeStream, 200, false, wantError, wantStream},
		{outcomeStream, 401, false, wantError, wantStream},
		{outcomeStream, 200, true, wantError, wantCancelled},
		{outcomeStream, 503, true, wantError, wantCancelled},
		{outcomeUpstream, 502, false, wantError, wantUpstream},
		{outcomeUpstream, 0, false, wantError, wantUpstream},
		{outcomeUpstream, 0, true, wantError, wantCancelled},
		{outcomeUpstream, 502, true, wantError, wantCancelled},
		{outcomeCancelled, 0, true, wantError, wantCancelled},
		{outcomeCancelled, 201, true, wantError, wantCancelled},
	} {
		c, ok := classifySpan(tc.o, tc.status, tc.ctxErr)
		if !ok || c.result != tc.result || c.reason != tc.reason || c.status != tc.status {
			t.Fatal("a class is not the contract value")
		}
	}
	for _, tc := range []struct {
		o      spanOutcome
		status int
		ctxErr bool
	}{
		{outcomeNone, 200, false},
		{spanOutcome(99), 200, false},
		{outcomeOK, 0, false},
		{outcomeOK, 101, false},
		{outcomeOK, 1000, false},
		{outcomeStream, 0, false},
		{outcomeStream, 101, true},
		{outcomeUpstream, 200, false},
		{outcomeRoute, 401, false},
		{outcomeAuthorization, 503, false},
		{outcomeReview, 401, false},
		{outcomeTargetAbsent, 503, false},
		{outcomeTargetUnavailable, 404, false},
		{outcomeCancelled, 0, false},
		{outcomeCancelled, 200, false},
		{outcomeCancelled, 101, true},
		{outcomeCancelled, 1000, true},
	} {
		if _, ok := classifySpan(tc.o, tc.status, tc.ctxErr); ok {
			t.Fatal("an invalid status or outcome gave a class")
		}
	}
}
