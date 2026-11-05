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
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const testAgent = "web"

const (
	litAgentGroup           = "core.koodam.dev"
	litAgentResource        = "agents"
	litCallerPod            = "caller-pod"
	litCaseAPIError         = "api error"
	litCaseEmptyNS          = "empty namespace"
	litCaseEmptyUID         = "empty uid"
	litCaseInvalidNS        = "invalid namespace"
	litCaseLongNS           = "long namespace"
	litCaseNilReply         = "nil reply"
	litCaseNotAuthenticated = "not authenticated"
	litCaseUpperNS          = "upper case namespace"
	litCookieKey            = "Cookie"
	litIngressSubresource   = "ingress"
	litKubeServiceURL       = "https://kubernetes.default.svc"
)

// wantAccess is the exact SubjectAccessReview for authenticated and the Agent testAgent.
func wantAccess() *authorizationv1.SubjectAccessReview {
	return &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
		User:   testUser,
		UID:    testUID,
		Groups: testGroups(),
		Extra:  map[string]authorizationv1.ExtraValue{podNameKey: {litCallerPod}, multiKey: {"one", "two"}},
		ResourceAttributes: &authorizationv1.ResourceAttributes{Namespace: testNamespace, Verb: "create",
			Group: litAgentGroup, Resource: litAgentResource, Subresource: litIngressSubresource, Name: testAgent},
	}}
}

// reviewEvidenceWantStop returns ErrReviewUnavailable for a failed review call, which is not a
// completed refusal, and refusal for each other case.
func reviewEvidenceWantStop(name string, refusal error) error {
	if name == litCaseAPIError || name == litCaseNilReply {
		return ErrReviewUnavailable
	}
	return refusal
}

func TestReview(t *testing.T) {
	if corev1alpha1.GroupVersion.Group != litAgentGroup {
		t.Fatal("the review group is not the Agent API group")
	}
	for _, audiences := range [][]string{{Audience}, {litKubeServiceURL, Audience}} {
		f := newFake(t.Context())
		f.tokenReply.Status.Audiences = audiences
		reply := f.tokenReply.DeepCopy()
		tok := jwtShaped()
		r := forged(tok)
		before := r.Header.Clone()
		got, err := f.reviewer().Review(f.ctx, r, testNamespace, testAgent)
		if err != nil || got != (Caller{testNamespace, testName, testUID}) {
			t.Fatal("Review did not return the TokenReview caller")
		}
		if !slices.Equal(f.calls, []string{tokenCall, accessCall}) || f.otherCtx {
			t.Fatal("Review did not make 1 TokenReview and then 1 SubjectAccessReview with the context")
		}
		tr := f.tokens[0]
		if tr.Spec.Token != tok || !slices.Equal(tr.Spec.Audiences, []string{Audience}) ||
			!equality.Semantic.DeepEqual(tr.ObjectMeta, metav1.ObjectMeta{}) ||
			!equality.Semantic.DeepEqual(tr.Status, authenticationv1.TokenReviewStatus{}) {
			t.Fatal("the TokenReview did not have only the token and the sole audience")
		}
		if !equality.Semantic.DeepEqual(f.accesses[0], wantAccess()) {
			t.Fatal("the SubjectAccessReview is not the exact review")
		}
		if !headersEqual(before, r.Header) || !equality.Semantic.DeepEqual(reply, f.tokenReply) {
			t.Fatal("Review changed the request or the TokenReview reply")
		}
	}
}

func TestReviewCopiesUser(t *testing.T) {
	f := newFake(t.Context())
	reply := f.tokenReply.DeepCopy()
	f.onAccess = func(sar *authorizationv1.SubjectAccessReview) {
		sar.Spec.User = canary
		sar.Spec.Groups[0] = canary
		sar.Spec.Extra[multiKey][0] = canary
		sar.Spec.Extra[canary] = authorizationv1.ExtraValue{canary}
	}
	if _, err := f.reviewer().Review(f.ctx, bearerRequest(opaque()), testNamespace, testAgent); err != nil {
		t.Fatal("Review refused a valid caller")
	}
	if !equality.Semantic.DeepEqual(reply, f.tokenReply) {
		t.Fatal("a change to the SubjectAccessReview changed the TokenReview reply")
	}
}

func TestReviewAuthentication(t *testing.T) {
	saUser := func(namespace, name string) string { return serviceAccountPrefix + namespace + ":" + name }
	status := func(f *fake) *authenticationv1.TokenReviewStatus { return &f.tokenReply.Status }
	for _, tc := range []struct {
		name   string
		change func(*fake, *http.Request)
		calls  int
	}{
		{"no tls", func(_ *fake, r *http.Request) {
			r.TLS = nil
			r.Header.Set(forwardedProto, https)
		}, 0},
		{"no credential", func(_ *fake, r *http.Request) { r.Header.Del(authorizationHeader) }, 0},
		{"cookie only", func(_ *fake, r *http.Request) {
			r.Header.Del(authorizationHeader)
			r.Header.Set(litCookieKey, "session="+opaque())
		}, 0},
		{"two credentials", func(_ *fake, r *http.Request) { r.Header.Add(authorizationHeader, scheme+opaque()) }, 0},
		{litCaseAPIError, func(f *fake, _ *http.Request) { f.tokenErr = errors.New(canary) }, 1},
		{litCaseNilReply, func(f *fake, _ *http.Request) { f.tokenReply = nil }, 1},
		{litCaseNotAuthenticated, func(f *fake, _ *http.Request) { status(f).Authenticated = false }, 1},
		{"status error", func(f *fake, _ *http.Request) { status(f).Error = canary }, 1},
		{"no audience", func(f *fake, _ *http.Request) { status(f).Audiences = nil }, 1},
		{"wrong audience", func(f *fake, _ *http.Request) {
			status(f).Audiences = []string{litKubeServiceURL}
		}, 1},
		{"audience with suffix", func(f *fake, _ *http.Request) { status(f).Audiences = []string{Audience + "-x"} }, 1},
		{"plain user", func(f *fake, _ *http.Request) { status(f).User.Username = "alice" }, 1},
		{"node user", func(f *fake, _ *http.Request) { status(f).User.Username = "system:node:node-1" }, 1},
		{"five parts", func(f *fake, _ *http.Request) { status(f).User.Username = testUser + ":x" }, 1},
		{litCaseInvalidNS, func(f *fake, _ *http.Request) {
			status(f).User.Username = saUser("Team-A", testName)
		}, 1},
		{"invalid name", func(f *fake, _ *http.Request) {
			status(f).User.Username = saUser(testNamespace, "a_b")
		}, 1},
		{litCaseEmptyUID, func(f *fake, _ *http.Request) { status(f).User.UID = "" }, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t.Context())
			r := forged(opaque())
			tc.change(f, r)
			got, err := f.reviewer().Review(f.ctx, r, testNamespace, testAgent)
			if !refused(got, err, reviewEvidenceWantStop(tc.name, ErrAuthentication)) {
				t.Fatal("Review did not refuse with only the fixed error")
			}
			if len(f.tokens) != tc.calls || len(f.accesses) != 0 {
				t.Fatal("Review made the wrong reviews after an authentication failure")
			}
		})
	}
}

func TestReviewAuthorization(t *testing.T) {
	status := func(f *fake) *authorizationv1.SubjectAccessReviewStatus { return &f.accessReply.Status }
	for _, tc := range []struct {
		name   string
		change func(*fake)
	}{
		{"not allowed", func(f *fake) { status(f).Allowed = false }},
		{"not allowed with reason", func(f *fake) { status(f).Allowed, status(f).Reason = false, canary }},
		{wantDenied, func(f *fake) { status(f).Allowed, status(f).Denied = false, true }},
		{"allowed and denied", func(f *fake) { status(f).Denied = true }},
		{"evaluation error", func(f *fake) { status(f).EvaluationError = canary }},
		{litCaseAPIError, func(f *fake) { f.accessErr = errors.New(canary) }},
		{litCaseNilReply, func(f *fake) { f.accessReply = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t.Context())
			tc.change(f)
			got, err := f.reviewer().Review(f.ctx, bearerRequest(opaque()), testNamespace, testAgent)
			if !refused(got, err, reviewEvidenceWantStop(tc.name, ErrAuthorization)) {
				t.Fatal("Review did not refuse with only the fixed error")
			}
			if !slices.Equal(f.calls, []string{tokenCall, accessCall}) {
				t.Fatal("Review did not make 1 TokenReview and then 1 SubjectAccessReview")
			}
		})
	}
}

func TestReviewNilClients(t *testing.T) {
	ctx, r := t.Context(), bearerRequest(opaque())
	var none *Reviewer
	if got, err := none.Review(ctx, r, testNamespace, testAgent); !refused(got, err, ErrReviewUnavailable) {
		t.Fatal("a nil Reviewer did not refuse with ErrReviewUnavailable")
	}
	if got, err := (&Reviewer{}).Review(ctx, r, testNamespace, testAgent); !refused(got, err, ErrReviewUnavailable) {
		t.Fatal("a Reviewer with no TokenReview client did not refuse with ErrReviewUnavailable")
	}
	f := newFake(ctx)
	v := f.reviewer()
	v.SubjectAccessReviews = nil
	if got, err := v.Review(ctx, r, testNamespace, testAgent); !refused(got, err, ErrReviewUnavailable) {
		t.Fatal("a Reviewer with no SubjectAccessReview client did not refuse with ErrReviewUnavailable")
	}
	if !slices.Equal(f.calls, []string{tokenCall}) {
		t.Fatal("Review did not authenticate before the authorization failure")
	}
}

func TestReviewRoute(t *testing.T) {
	for _, tc := range []struct{ name, namespace, agent string }{
		{litCaseEmptyNS, "", testAgent},
		{"empty agent", testNamespace, ""},
		{litCaseUpperNS, "Team", testAgent},
		{"dotted namespace", "team.a", testAgent},
		{litCaseLongNS, strings.Repeat("a", 64), testAgent},
		{"upper case agent", testNamespace, "Web"},
		{"agent with slash", testNamespace, "a/b"},
		{"dot dot agent", testNamespace, ".."},
		{"long agent", testNamespace, strings.Repeat("a", 254)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t.Context())
			got, err := f.reviewer().Review(f.ctx, bearerRequest(opaque()), tc.namespace, tc.agent)
			if !refused(got, err, ErrRoute) || len(f.calls) != 0 {
				t.Fatal("Review did not refuse the route names with ErrRoute and no review")
			}
		})
	}
	f := newFake(t.Context())
	if _, err := f.reviewer().Review(f.ctx, bearerRequest(opaque()), testNamespace, "web.v1"); err != nil {
		t.Fatal("Review refused a dotted Agent name")
	}
}

// TestReviewNoCache makes sure that each call makes new reviews and uses only their replies.
func TestReviewNoCache(t *testing.T) {
	f := newFake(t.Context())
	v, r := f.reviewer(), bearerRequest(opaque())
	for range 2 {
		if _, err := v.Review(f.ctx, r, testNamespace, testAgent); err != nil {
			t.Fatal("Review refused a valid caller")
		}
	}
	f.tokenReply.Status.Authenticated = false
	if got, err := v.Review(f.ctx, r, testNamespace, testAgent); !refused(got, err, ErrAuthentication) {
		t.Fatal("Review used an earlier authentication")
	}
	f.tokenReply = authenticated()
	f.accessReply.Status.Allowed = false
	if got, err := v.Review(f.ctx, r, testNamespace, testAgent); !refused(got, err, ErrAuthorization) {
		t.Fatal("Review used an earlier authorization")
	}
	want := []string{tokenCall, accessCall, tokenCall, accessCall, tokenCall, tokenCall, accessCall}
	if !slices.Equal(f.calls, want) || f.tokens[0] == f.tokens[1] || f.accesses[0] == f.accesses[1] {
		t.Fatal("Review did not make new reviews in the order TokenReview then SubjectAccessReview")
	}
}

func TestReviewCancel(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    string // The call that cancels the context. The empty string cancels before Review.
		want  error
		calls []string
	}{
		{"before review", "", ErrReviewUnavailable, nil},
		{"during token review", tokenCall, ErrReviewUnavailable, []string{tokenCall}},
		{"during access review", accessCall, ErrReviewUnavailable, []string{tokenCall, accessCall}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			f := newFake(ctx)
			switch tc.in {
			case "":
				cancel()
			case tokenCall:
				f.onToken = cancel
			case accessCall:
				f.onAccess = func(*authorizationv1.SubjectAccessReview) { cancel() }
			}
			v, r := f.reviewer(), bearerRequest(opaque())
			if got, err := v.Review(ctx, r, testNamespace, testAgent); !refused(got, err, tc.want) {
				t.Fatal("Review did not refuse a canceled review with the fixed error")
			}
			if got, err := v.Review(ctx, r, testNamespace, testAgent); !refused(got, err, ErrReviewUnavailable) {
				t.Fatal("Review did not refuse a call after the cancel")
			}
			if !slices.Equal(f.calls, tc.calls) || f.otherCtx {
				t.Fatal("Review made the wrong reviews for a canceled context")
			}
		})
	}
}
