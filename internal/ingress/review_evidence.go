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
	"net/http"
	"strings"

	apivalidation "k8s.io/apimachinery/pkg/api/validation"
)

// reviewEvidenceClass is the fixed class of 1 review. It comes only from the step that stopped and
// its fixed error. It never comes from an error text or a review status text. 0 is not a class.
// Allowed: both reviews passed. Route: ErrRoute, no review ran. Authentication and Authorization:
// ErrAuthentication and ErrAuthorization, a completed refusal. Unavailable: ErrReviewUnavailable,
// not a refusal.
type reviewEvidenceClass int

const (
	reviewEvidenceAllowed reviewEvidenceClass = iota + 1
	reviewEvidenceRoute
	reviewEvidenceAuthentication
	reviewEvidenceAuthorization
	reviewEvidenceUnavailable
)

// reviewEvidenceIdentity is the log-only identity that a successful TokenReview proved: the
// ServiceAccount namespace, name and UID. It is not an authorization. It gives no access, and the
// handler must not use it for a target lookup. Only an authorized Caller permits that.
type reviewEvidenceIdentity struct{ namespace, serviceAccount, uid string }

// reviewEvidence is the log-only result of 1 review. identity is zero until TokenReview succeeds.
// It holds no group, extra value, credential, review object, status text, route value or error.
type reviewEvidence struct {
	class    reviewEvidenceClass
	identity reviewEvidenceIdentity
}

// reviewEvidenceCopy returns a copy of the identity of c. It shares no memory with a review reply.
func reviewEvidenceCopy(c Caller) reviewEvidenceIdentity {
	return reviewEvidenceIdentity{strings.Clone(c.Namespace), strings.Clone(c.Name), strings.Clone(c.UID)}
}

// reviewEvidenceStop returns the fixed class and error of a failed review step. It compares err
// only with the fixed errors. Each other error gives ErrReviewUnavailable.
func reviewEvidenceStop(err error) (reviewEvidenceClass, error) {
	switch err {
	case ErrAuthentication:
		return reviewEvidenceAuthentication, ErrAuthentication
	case ErrAuthorization:
		return reviewEvidenceAuthorization, ErrAuthorization
	}
	return reviewEvidenceUnavailable, ErrReviewUnavailable
}

// reviewWithEvidence is Review, and it also returns the log-only evidence of the review. On each
// error, the Caller is empty. After a successful TokenReview, the evidence keeps the proved
// identity, also if authorization then fails. It writes no log, event, metric or status.
func (v *Reviewer) reviewWithEvidence(ctx context.Context, r *http.Request,
	namespace, agent string) (Caller, reviewEvidence, error) {
	if len(apivalidation.ValidateNamespaceName(namespace, false)) > 0 ||
		len(apivalidation.NameIsDNSSubdomain(agent, false)) > 0 {
		return Caller{}, reviewEvidence{class: reviewEvidenceRoute}, ErrRoute
	}
	user, caller, err := v.authenticate(ctx, r)
	if err != nil {
		class, fixed := reviewEvidenceStop(err)
		return Caller{}, reviewEvidence{class: class}, fixed
	}
	proved := reviewEvidenceCopy(caller)
	if err := v.authorize(ctx, user, namespace, agent); err != nil {
		class, fixed := reviewEvidenceStop(err)
		return Caller{}, reviewEvidence{class: class, identity: proved}, fixed
	}
	return caller, reviewEvidence{class: reviewEvidenceAllowed, identity: proved}, nil
}
