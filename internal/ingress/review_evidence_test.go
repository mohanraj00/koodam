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
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/api/equality"
)

// The expected proved identity. These constants do not come from the fake.
const (
	reviewEvidenceNamespace = "team-a"
	reviewEvidenceName      = "caller"
	reviewEvidenceUID       = "uid-1"
)

func reviewEvidenceProved() reviewEvidenceIdentity {
	return reviewEvidenceIdentity{reviewEvidenceNamespace, reviewEvidenceName, reviewEvidenceUID}
}

// reviewEvidenceCanary returns a new random private detail. It is not a credential. It is only in
// memory in this test process, and no test prints it.
func reviewEvidenceCanary() string { return "evidence-" + strings.ToLower(rand.Text()) }

// reviewEvidenceClean is true if err and ev do not contain canary.
func reviewEvidenceClean(err error, ev reviewEvidence, canary string) bool {
	if err != nil && strings.Contains(err.Error(), canary) {
		return false
	}
	return !strings.Contains(fmt.Sprintf("%#v", ev), canary)
}

// reviewEvidenceSetup is the input of 1 case. canary is in the groups, the extra values and a
// forged header of each case.
type reviewEvidenceSetup struct {
	f      *fake
	v      *Reviewer
	r      *http.Request
	cancel context.CancelFunc
	canary string
}

// reviewEvidenceResult is the output of 1 case.
type reviewEvidenceResult struct {
	caller   Caller
	ev       reviewEvidence
	calls    []string
	otherCtx bool
	canary   string
	err      error
}

// reviewEvidenceRun runs 1 case with reviewWithEvidence, or with Review if viaReview is true.
func reviewEvidenceRun(t *testing.T, change func(*reviewEvidenceSetup), viaReview bool) reviewEvidenceResult {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := &reviewEvidenceSetup{f: newFake(ctx), r: forged(opaque()), cancel: cancel, canary: reviewEvidenceCanary()}
	s.v = s.f.reviewer()
	user := &s.f.tokenReply.Status.User
	user.Groups = append(user.Groups, s.canary)
	user.Extra[s.canary] = authenticationv1.ExtraValue{s.canary}
	s.r.Header.Set("X-Remote-User", s.canary)
	change(s)
	res := reviewEvidenceResult{canary: s.canary}
	if viaReview {
		res.caller, res.err = s.v.Review(ctx, s.r, testNamespace, testAgent)
	} else {
		res.caller, res.ev, res.err = s.v.reviewWithEvidence(ctx, s.r, testNamespace, testAgent)
	}
	res.calls, res.otherCtx = s.f.calls, s.f.otherCtx
	return res
}

func TestReviewEvidence(t *testing.T) {
	tr := func(s *reviewEvidenceSetup) *authenticationv1.TokenReviewStatus { return &s.f.tokenReply.Status }
	sar := func(s *reviewEvidenceSetup) *authorizationv1.SubjectAccessReviewStatus {
		return &s.f.accessReply.Status
	}
	one, both := []string{tokenCall}, []string{tokenCall, accessCall}
	authn, authz, down := reviewEvidenceAuthentication, reviewEvidenceAuthorization, reviewEvidenceUnavailable
	for _, tc := range []struct {
		name   string
		change func(*reviewEvidenceSetup)
		want   error
		class  reviewEvidenceClass
		proved bool
		calls  []string
	}{
		{"allowed", func(*reviewEvidenceSetup) {}, nil, reviewEvidenceAllowed, true, both},
		{"bad credential", func(s *reviewEvidenceSetup) { s.r.TLS = nil }, ErrAuthentication, authn, false, nil},
		{"bad credential and no clients", func(s *reviewEvidenceSetup) {
			s.r.Header.Del(authorizationHeader)
			s.v.TokenReviews, s.v.SubjectAccessReviews = nil, nil
		}, ErrAuthentication, authn, false, nil},
		{litCaseNoTokenClient, func(s *reviewEvidenceSetup) { s.v.TokenReviews = nil },
			ErrReviewUnavailable, down, false, nil},
		{"cancel before", func(s *reviewEvidenceSetup) { s.cancel() }, ErrReviewUnavailable, down, false, nil},
		{"token not authenticated", func(s *reviewEvidenceSetup) { tr(s).Authenticated = false },
			ErrAuthentication, authn, false, one},
		{"token status error", func(s *reviewEvidenceSetup) { tr(s).Error = s.canary },
			ErrAuthentication, authn, false, one},
		{"token other audience", func(s *reviewEvidenceSetup) { tr(s).Audiences = []string{s.canary} },
			ErrAuthentication, authn, false, one},
		{"token other user", func(s *reviewEvidenceSetup) { tr(s).User.Username = s.canary },
			ErrAuthentication, authn, false, one},
		{"token api error", func(s *reviewEvidenceSetup) { s.f.tokenErr = errors.New(s.canary) },
			ErrReviewUnavailable, down, false, one},
		{"token nil reply", func(s *reviewEvidenceSetup) { s.f.tokenReply = nil },
			ErrReviewUnavailable, down, false, one},
		{"token cancel", func(s *reviewEvidenceSetup) { s.f.onToken = s.cancel },
			ErrReviewUnavailable, down, false, one},
		{"access not allowed", func(s *reviewEvidenceSetup) { sar(s).Allowed, sar(s).Reason = false, s.canary },
			ErrAuthorization, authz, true, both},
		{"access denied", func(s *reviewEvidenceSetup) { sar(s).Allowed, sar(s).Denied = false, true },
			ErrAuthorization, authz, true, both},
		{"access denied and allowed", func(s *reviewEvidenceSetup) { sar(s).Denied = true },
			ErrAuthorization, authz, true, both},
		{"access evaluation error", func(s *reviewEvidenceSetup) { sar(s).EvaluationError = s.canary },
			ErrAuthorization, authz, true, both},
		{"access api error", func(s *reviewEvidenceSetup) { s.f.accessErr = errors.New(s.canary) },
			ErrReviewUnavailable, down, true, both},
		{"access nil reply", func(s *reviewEvidenceSetup) { s.f.accessReply = nil },
			ErrReviewUnavailable, down, true, both},
		{"access cancel", func(s *reviewEvidenceSetup) {
			s.f.onAccess = func(*authorizationv1.SubjectAccessReview) { s.cancel() }
		}, ErrReviewUnavailable, down, true, both},
		{litCaseNoAccessClient, func(s *reviewEvidenceSetup) { s.v.SubjectAccessReviews = nil },
			ErrReviewUnavailable, down, true, one},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantCaller := Caller{}
			if tc.want == nil {
				wantCaller = Caller{reviewEvidenceNamespace, reviewEvidenceName, reviewEvidenceUID}
			}
			wantID := reviewEvidenceIdentity{}
			if tc.proved {
				wantID = reviewEvidenceProved()
			}
			for _, viaReview := range []bool{false, true} {
				res := reviewEvidenceRun(t, tc.change, viaReview)
				if res.err != tc.want || !slices.Equal(res.calls, tc.calls) || res.otherCtx {
					t.Fatal("the review did not stop with the fixed error after the expected reviews")
				}
				if res.caller != wantCaller {
					t.Fatal("the review returned an authorized Caller on a failure")
				}
				if !reviewEvidenceClean(res.err, res.ev, res.canary) {
					t.Fatal("an error or the evidence has a private detail")
				}
				if !viaReview && res.ev != (reviewEvidence{class: tc.class, identity: wantID}) {
					t.Fatal("the evidence does not have the fixed class and only the proved identity")
				}
			}
		})
	}
}

// TestReviewEvidenceRoute makes sure that a route refusal makes no review and keeps no route value.
func TestReviewEvidenceRoute(t *testing.T) {
	c := reviewEvidenceCanary()
	for _, route := range [][2]string{{"Team-" + c, testAgent}, {testNamespace, "Web-" + c}, {testNamespace, c + "/x"}} {
		f := newFake(t.Context())
		got, ev, err := f.reviewer().reviewWithEvidence(f.ctx, bearerRequest(opaque()), route[0], route[1])
		if err != ErrRoute || got != (Caller{}) || ev != (reviewEvidence{class: reviewEvidenceRoute}) {
			t.Fatal("reviewWithEvidence did not refuse the route with only the route class")
		}
		if len(f.calls) != 0 || !reviewEvidenceClean(err, ev, c) {
			t.Fatal("a route refusal made a review or kept a private detail")
		}
	}
	var none *Reviewer
	got, ev, err := none.reviewWithEvidence(t.Context(), nil, "", "")
	if err != ErrRoute || got != (Caller{}) || ev != (reviewEvidence{class: reviewEvidenceRoute}) {
		t.Fatal("a nil Reviewer did not refuse the route first")
	}
}

// TestReviewEvidenceCopies makes sure that a later change to a review object does not change the
// evidence, and that the reviews keep the sole audience, the exact access review and the copies.
func TestReviewEvidenceCopies(t *testing.T) {
	for _, allow := range []bool{true, false} {
		f := newFake(t.Context())
		f.accessReply.Status.Allowed = allow
		c := reviewEvidenceCanary()
		var sent *authorizationv1.SubjectAccessReview
		f.onAccess = func(sar *authorizationv1.SubjectAccessReview) {
			sent = sar.DeepCopy()
			sar.Spec.User, sar.Spec.UID, sar.Spec.Groups[0], sar.Spec.Extra[multiKey][0] = c, c, c, c
		}
		reply, tok := f.tokenReply.DeepCopy(), opaque()
		got, ev, err := f.reviewer().reviewWithEvidence(f.ctx, bearerRequest(tok), testNamespace, testAgent)
		if (err == nil) != allow || (got != Caller{}) != allow {
			t.Fatal("reviewWithEvidence did not follow the SubjectAccessReview decision")
		}
		if !equality.Semantic.DeepEqual(reply, f.tokenReply) || !equality.Semantic.DeepEqual(sent, wantAccess()) {
			t.Fatal("the SubjectAccessReview is not the exact copy of the TokenReview user")
		}
		if f.tokens[0].Spec.Token != tok || !slices.Equal(f.tokens[0].Spec.Audiences, []string{"koodam-ingress"}) {
			t.Fatal("the TokenReview did not have the token and the sole audience")
		}
		u := &f.tokenReply.Status.User
		u.Username, u.UID, u.Groups[0] = c, c, c
		if ev.identity != reviewEvidenceProved() || !reviewEvidenceClean(nil, ev, c) {
			t.Fatal("a change to a review object changed the evidence")
		}
	}
}

// TestReviewEvidenceFields makes sure that the evidence has only the fixed class and the 3 proved
// identity values, in a type that is not the authorized Caller.
func TestReviewEvidenceFields(t *testing.T) {
	// fields returns the field names of typ, or nil if a field does not have the kind want.
	fields := func(typ reflect.Type, want reflect.Kind) []string {
		got := make([]string, 0, typ.NumField())
		for f := range typ.Fields() {
			if want != reflect.Invalid && f.Type.Kind() != want {
				return nil
			}
			got = append(got, f.Name)
		}
		return got
	}
	id := reflect.TypeFor[reviewEvidenceIdentity]()
	if !slices.Equal(fields(reflect.TypeFor[reviewEvidence](), reflect.Invalid), []string{"class", "identity"}) ||
		!slices.Equal(fields(id, reflect.String), []string{"namespace", "serviceAccount", "uid"}) ||
		id.ConvertibleTo(reflect.TypeFor[Caller]()) {
		t.Fatal("the evidence has a field other than the class and the proved identity")
	}
	classes := []reviewEvidenceClass{reviewEvidenceAllowed, reviewEvidenceRoute, reviewEvidenceAuthentication,
		reviewEvidenceAuthorization, reviewEvidenceUnavailable}
	if slices.Contains(classes, 0) || len(slices.Compact(slices.Sorted(slices.Values(classes)))) != len(classes) {
		t.Fatal("the evidence classes are not distinct and set")
	}
	texts := []string{ErrRoute.Error(), ErrAuthentication.Error(), ErrAuthorization.Error(),
		ErrReviewUnavailable.Error()}
	if len(slices.Compact(slices.Sorted(slices.Values(texts)))) != len(texts) {
		t.Fatal("the review errors do not have distinct fixed text")
	}
	class, err := reviewEvidenceStop(errors.New(reviewEvidenceCanary()))
	if class != reviewEvidenceUnavailable || err != ErrReviewUnavailable {
		t.Fatal("an unknown error did not give the fixed unavailable class")
	}
}

const (
	litCaseNoAccessClient = "no access client"
	litCaseNoTokenClient  = "no token client"
)
