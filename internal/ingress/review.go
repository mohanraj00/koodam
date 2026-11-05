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

// The virtual resource of the SubjectAccessReview. It grants ingress to an Agent. It does not
// grant create on the Agent resource.
const (
	reviewGroup       = "core.koodam.dev"
	reviewResource    = "agents"
	reviewSubresource = "ingress"
	reviewVerb        = "create"
)

// The errors have fixed text. They never contain a credential, an identity, a route name or a
// review status. The server will map ErrRoute to 400, ErrAuthentication to 401,
// ErrAuthorization to 403 and ErrReviewUnavailable to 503. ErrReviewUnavailable is not a
// completed refusal: a review client is missing, a review call failed or gave no reply, or ctx
// ended.
var (
	ErrAuthorization = errors.New("the ingress caller is not authorized")
	ErrRoute         = errors.New("the ingress route is not valid")
)

// Review returns the Caller of r if the caller can use the ingress of the Agent. namespace and
// agent must come from the route parser. Review does not parse a route and it does not set a route
// policy. If a name is not valid, it returns ErrRoute and makes no review. Then it authenticates
// the Bearer credential with TokenReview, and authorizes the caller with SubjectAccessReview. It
// gives ctx to the two reviews. If ctx ends before or during a review, the review fails with
// ErrReviewUnavailable. On a failure, it returns an empty Caller and one fixed error, and it makes
// no further review. It does not change r.
func (v *Reviewer) Review(ctx context.Context, r *http.Request, namespace, agent string) (Caller, error) {
	caller, _, err := v.reviewWithEvidence(ctx, r, namespace, agent)
	if err != nil {
		return Caller{}, err
	}
	return caller, nil
}

// authorize submits user to SubjectAccessReview for the ingress of the Agent. The status must be
// allowed, not denied, with no evaluation error, else it returns ErrAuthorization. A missing
// client, a call error, a nil reply or an ended ctx gives ErrReviewUnavailable.
func (v *Reviewer) authorize(ctx context.Context, user authenticationv1.UserInfo, namespace, agent string) error {
	if v == nil || v.SubjectAccessReviews == nil || ctx.Err() != nil {
		return ErrReviewUnavailable
	}
	res, err := v.SubjectAccessReviews.Create(ctx, accessReview(user, namespace, agent), metav1.CreateOptions{})
	if err != nil || ctx.Err() != nil || res == nil {
		return ErrReviewUnavailable
	}
	if s := res.Status; !s.Allowed || s.Denied || s.EvaluationError != "" {
		return ErrAuthorization
	}
	return nil
}

// accessReview returns the SubjectAccessReview of user for the ingress of the Agent. It copies the
// groups and the extra values, thus a change to the review does not change user.
func accessReview(user authenticationv1.UserInfo, namespace, agent string) *authorizationv1.SubjectAccessReview {
	var extra map[string]authorizationv1.ExtraValue
	if user.Extra != nil {
		extra = make(map[string]authorizationv1.ExtraValue, len(user.Extra))
		for k, vs := range user.Extra {
			extra[k] = authorizationv1.ExtraValue(slices.Clone(vs))
		}
	}
	return &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
		User:   user.Username,
		UID:    user.UID,
		Groups: slices.Clone(user.Groups),
		Extra:  extra,
		ResourceAttributes: &authorizationv1.ResourceAttributes{
			Namespace:   namespace,
			Verb:        reviewVerb,
			Group:       reviewGroup,
			Resource:    reviewResource,
			Subresource: reviewSubresource,
			Name:        agent,
		},
	}}
}
