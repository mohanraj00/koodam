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

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Audience is the sole audience that a TokenReview requests. The returned audiences must contain
// it, and they may contain others.
const Audience = "koodam-ingress"

// ErrReviewUnavailable is the fixed error for a review that cannot complete.
var ErrReviewUnavailable = errors.New("the ingress caller review is not available")

// TokenReviewCreator creates a TokenReview. The typed client-go TokenReviewInterface satisfies it.
type TokenReviewCreator interface {
	Create(ctx context.Context, tr *authenticationv1.TokenReview,
		opts metav1.CreateOptions) (*authenticationv1.TokenReview, error)
}

// SubjectAccessReviewCreator creates a SubjectAccessReview. The typed client-go
// SubjectAccessReviewInterface satisfies it.
type SubjectAccessReviewCreator interface {
	Create(ctx context.Context, sar *authorizationv1.SubjectAccessReview,
		opts metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error)
}

// Reviewer has the 2 Kubernetes API clients that check the caller of an ingress request. It keeps
// no result. TokenReviews authenticates the caller. SubjectAccessReviews authorizes the caller.
type Reviewer struct {
	TokenReviews         TokenReviewCreator
	SubjectAccessReviews SubjectAccessReviewCreator
}

// authenticate submits the Bearer credential of r to TokenReview with the sole audience Audience.
// The status must be authenticated, with no error, the audience Audience and a ServiceAccount
// user with a UID. Other returned audiences are permitted. A bad Bearer credential or a completed
// review that fails a check gives ErrAuthentication. The credential check runs first, thus a bad
// credential refuses before a client check. A missing client, a call error, a nil reply or an
// ended ctx gives ErrReviewUnavailable.
func (v *Reviewer) authenticate(ctx context.Context, r *http.Request) (authenticationv1.UserInfo, Caller, error) {
	token, err := bearer(r)
	if err != nil {
		return authenticationv1.UserInfo{}, Caller{}, ErrAuthentication
	}
	if v == nil || v.TokenReviews == nil || ctx.Err() != nil {
		return authenticationv1.UserInfo{}, Caller{}, ErrReviewUnavailable
	}
	res, err := v.TokenReviews.Create(ctx, &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{Token: token, Audiences: []string{Audience}},
	}, metav1.CreateOptions{})
	if err != nil || ctx.Err() != nil || res == nil {
		return authenticationv1.UserInfo{}, Caller{}, ErrReviewUnavailable
	}
	s := res.Status
	if !s.Authenticated || s.Error != "" || !slices.Contains(s.Audiences, Audience) {
		return authenticationv1.UserInfo{}, Caller{}, ErrAuthentication
	}
	caller, err := callerFrom(s.User)
	if err != nil {
		return authenticationv1.UserInfo{}, Caller{}, ErrAuthentication
	}
	return s.User, caller, nil
}
