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
	typedauthenticationv1 "k8s.io/client-go/kubernetes/typed/authentication/v1"
	typedauthorizationv1 "k8s.io/client-go/kubernetes/typed/authorization/v1"
)

// The typed client-go interfaces satisfy the narrow interfaces.
var (
	_ TokenReviewCreator         = typedauthenticationv1.TokenReviewInterface(nil)
	_ SubjectAccessReviewCreator = typedauthorizationv1.SubjectAccessReviewInterface(nil)
)

// canary is a fixed private detail. It is not a credential. A fake API puts it in an error or a
// status. An error from Review must not contain it.
const canary = "private-detail-canary"

const (
	tokenCall  = "TokenReview"
	accessCall = "SubjectAccessReview"
	podNameKey = "authentication.kubernetes.io/pod-name"
	multiKey   = "example.test/multi"
)

type createTokenReview func(context.Context, *authenticationv1.TokenReview,
	metav1.CreateOptions) (*authenticationv1.TokenReview, error)

func (f createTokenReview) Create(ctx context.Context, tr *authenticationv1.TokenReview,
	opts metav1.CreateOptions) (*authenticationv1.TokenReview, error) {
	return f(ctx, tr, opts)
}

type createAccessReview func(context.Context, *authorizationv1.SubjectAccessReview,
	metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error)

func (f createAccessReview) Create(ctx context.Context, sar *authorizationv1.SubjectAccessReview,
	opts metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
	return f(ctx, sar, opts)
}

// fake is a local fake of the 2 review APIs. It keeps the calls only in memory in this process.
type fake struct {
	ctx         context.Context // Each call must get this context.
	calls       []string
	otherCtx    bool
	tokens      []*authenticationv1.TokenReview
	accesses    []*authorizationv1.SubjectAccessReview
	tokenReply  *authenticationv1.TokenReview
	tokenErr    error
	accessReply *authorizationv1.SubjectAccessReview
	accessErr   error
	onToken     func()
	onAccess    func(*authorizationv1.SubjectAccessReview)
}

func newFake(ctx context.Context) *fake {
	return &fake{ctx: ctx, tokenReply: authenticated(), accessReply: allowed()}
}

func (f *fake) record(ctx context.Context, call string) {
	f.calls = append(f.calls, call)
	if ctx != f.ctx {
		f.otherCtx = true
	}
}

func (f *fake) reviewer() *Reviewer {
	return &Reviewer{
		TokenReviews: createTokenReview(func(ctx context.Context, tr *authenticationv1.TokenReview,
			_ metav1.CreateOptions) (*authenticationv1.TokenReview, error) {
			f.record(ctx, tokenCall)
			f.tokens = append(f.tokens, tr)
			if f.onToken != nil {
				f.onToken()
			}
			return f.tokenReply, f.tokenErr
		}),
		SubjectAccessReviews: createAccessReview(func(ctx context.Context, sar *authorizationv1.SubjectAccessReview,
			_ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
			f.record(ctx, accessCall)
			f.accesses = append(f.accesses, sar)
			if f.onAccess != nil {
				f.onAccess(sar)
			}
			return f.accessReply, f.accessErr
		}),
	}
}

// testGroups returns a new copy of the groups of the test user.
func testGroups() []string {
	return []string{"system:serviceaccounts", "system:serviceaccounts:team-a", "system:authenticated"}
}

func authenticated() *authenticationv1.TokenReview {
	return &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
		Authenticated: true, Audiences: []string{Audience},
		User: authenticationv1.UserInfo{Username: testUser, UID: testUID, Groups: testGroups(),
			Extra: map[string]authenticationv1.ExtraValue{podNameKey: {"caller-pod"}, multiKey: {"one", "two"}}},
	}}
}

func allowed() *authorizationv1.SubjectAccessReview {
	return &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true}}
}

// bearerRequest returns a request on TLS with one Bearer credential.
func bearerRequest(tok string) *http.Request {
	return request(http.Header{authorizationHeader: {scheme + tok}})
}

// forged returns a request with one Bearer credential and forged identity headers. Review must
// ignore the forged headers.
func forged(tok string) *http.Request {
	other := "system:serviceaccount:kube-system:forged"
	return request(http.Header{
		authorizationHeader: {scheme + tok},
		"Cookie":            {"session=" + opaque()},
		"Forwarded":         {"for=192.0.2.1;proto=https"},
		"X-Forwarded-For":   {"192.0.2.1"},
		forwardedProto:      {https},
		"X-Forwarded-User":  {other},
		"X-Remote-User":     {other},
		"Impersonate-User":  {other},
		"X-Koodam-Run":      {other},
		"X-Koodam-User":     {other},
	})
}

// refused is true if Review refused with want, with an empty Caller and no private detail.
func refused(got Caller, err, want error) bool {
	return err == want && got == (Caller{}) && !strings.Contains(err.Error(), canary)
}

// TestAuthenticate tests the TokenReview step alone. It makes no SubjectAccessReview.
func TestAuthenticate(t *testing.T) {
	// rejected is true if authenticate refused with only ErrAuthentication and an empty identity.
	rejected := func(user authenticationv1.UserInfo, got Caller, err error) bool {
		return refused(got, err, ErrAuthentication) && equality.Semantic.DeepEqual(user, authenticationv1.UserInfo{})
	}
	for _, audiences := range [][]string{{Audience}, {"https://kubernetes.default.svc", Audience}} {
		f := newFake(t.Context())
		f.tokenReply.Status.Audiences = audiences
		reply := f.tokenReply.DeepCopy()
		tok := jwtShaped()
		r := forged(tok)
		before := r.Header.Clone()
		user, got, err := f.reviewer().authenticate(f.ctx, r)
		if err != nil || got != (Caller{testNamespace, testName, testUID}) ||
			!equality.Semantic.DeepEqual(user, reply.Status.User) {
			t.Fatal("authenticate did not return the TokenReview identity")
		}
		if !slices.Equal(f.calls, []string{tokenCall}) || f.otherCtx {
			t.Fatal("authenticate did not make only 1 TokenReview with the context")
		}
		tr := f.tokens[0]
		if tr.Spec.Token != tok || !slices.Equal(tr.Spec.Audiences, []string{Audience}) ||
			!equality.Semantic.DeepEqual(tr.ObjectMeta, metav1.ObjectMeta{}) ||
			!equality.Semantic.DeepEqual(tr.Status, authenticationv1.TokenReviewStatus{}) {
			t.Fatal("the TokenReview did not have only the token and the sole audience")
		}
		if !headersEqual(before, r.Header) || !equality.Semantic.DeepEqual(reply, f.tokenReply) {
			t.Fatal("authenticate changed the request or the TokenReview reply")
		}
	}

	status := func(f *fake) *authenticationv1.TokenReviewStatus { return &f.tokenReply.Status }
	for _, tc := range []struct {
		name   string
		change func(*fake)
	}{
		{"api error", func(f *fake) { f.tokenErr = errors.New(canary) }},
		{"nil reply", func(f *fake) { f.tokenReply = nil }},
		{"not authenticated", func(f *fake) { status(f).Authenticated = false }},
		{"status error", func(f *fake) { status(f).Error = canary }},
		{"wrong audience", func(f *fake) { status(f).Audiences = []string{"https://kubernetes.default.svc"} }},
		{"node user", func(f *fake) { status(f).User.Username = "system:node:node-1" }},
		{"invalid namespace", func(f *fake) {
			status(f).User.Username = serviceAccountPrefix + "Team-A:" + testName
		}},
		{"empty uid", func(f *fake) { status(f).User.UID = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t.Context())
			tc.change(f)
			if user, got, err := f.reviewer().authenticate(f.ctx, bearerRequest(opaque())); !rejected(user, got, err) {
				t.Fatal("authenticate did not refuse with only ErrAuthentication and an empty identity")
			}
			if !slices.Equal(f.calls, []string{tokenCall}) || f.otherCtx {
				t.Fatal("authenticate did not make only 1 TokenReview with the context")
			}
		})
	}

	ctx, r := t.Context(), bearerRequest(opaque())
	var none *Reviewer
	if user, got, err := none.authenticate(ctx, r); !rejected(user, got, err) {
		t.Fatal("a nil Reviewer did not refuse with ErrAuthentication")
	}
	if user, got, err := (&Reviewer{}).authenticate(ctx, r); !rejected(user, got, err) {
		t.Fatal("a Reviewer with no TokenReview client did not refuse with ErrAuthentication")
	}

	for _, tc := range []struct {
		name   string
		during bool // If false, the context ends before the first call.
		calls  []string
	}{
		{"cancel before", false, nil},
		{"cancel during", true, []string{tokenCall}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			f := newFake(ctx)
			if tc.during {
				f.onToken = cancel
			} else {
				cancel()
			}
			v, r := f.reviewer(), bearerRequest(opaque())
			for range 2 {
				if user, got, err := v.authenticate(ctx, r); !rejected(user, got, err) {
					t.Fatal("authenticate did not refuse a canceled context with ErrAuthentication")
				}
			}
			if !slices.Equal(f.calls, tc.calls) || f.otherCtx {
				t.Fatal("authenticate made a TokenReview after the cancel")
			}
		})
	}
}
