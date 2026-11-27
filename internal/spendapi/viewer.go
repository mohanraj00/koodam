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

package spendapi

import (
	"context"
	"errors"
	"slices"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	authenticationclient "k8s.io/client-go/kubernetes/typed/authentication/v1"
	authorizationclient "k8s.io/client-go/kubernetes/typed/authorization/v1"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

var (
	errNotAuthenticated = errors.New("the Kubernetes token is not valid")
	errReview           = errors.New("the Kubernetes review is not available")
)

// Viewers identifies each caller with the Kubernetes API server. A caller sees only the
// namespaces where its own identity can list Agents. The API never impersonates the caller.
type Viewers struct {
	Tokens authenticationclient.TokenReviewInterface
	Access authorizationclient.SubjectAccessReviewInterface
	// Audiences, if set, are the token audiences that the API server must accept.
	Audiences []string
}

func (v *Viewers) valid() bool {
	return v.Tokens != nil && v.Access != nil && !slices.Contains(v.Audiences, "")
}

// user does a TokenReview. The result never holds the token.
func (v *Viewers) user(ctx context.Context, token string) (authenticationv1.UserInfo, error) {
	review, err := v.Tokens.Create(ctx, &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{Token: token, Audiences: v.Audiences},
	}, metav1.CreateOptions{})
	if err != nil {
		return authenticationv1.UserInfo{}, errReview
	}
	s := review.Status
	if !s.Authenticated || s.User.Username == "" {
		return authenticationv1.UserInfo{}, errNotAuthenticated
	}
	if len(v.Audiences) != 0 && !slices.ContainsFunc(s.Audiences, func(a string) bool {
		return slices.Contains(v.Audiences, a)
	}) {
		return authenticationv1.UserInfo{}, errNotAuthenticated
	}
	return s.User, nil
}

// listAgents is the review of each path.
var listAgents = authorizationv1.ResourceAttributes{Verb: "list", Group: corev1alpha1.GroupVersion.Group, Resource: "agents"}

// allowed does one SubjectAccessReview for each check in each namespace. A namespace is in the
// result only if each check permits the caller.
func (v *Viewers) allowed(ctx context.Context, user authenticationv1.UserInfo, namespaces []string,
	checks []authorizationv1.ResourceAttributes) ([]string, error) {
	extra := make(map[string]authorizationv1.ExtraValue, len(user.Extra))
	for k, values := range user.Extra {
		extra[k] = authorizationv1.ExtraValue(values)
	}
	n := len(checks)
	ok := make([]bool, len(namespaces)*n)
	err := parallel(ctx, len(ok), func(ctx context.Context, i int) error {
		attributes := checks[i%n]
		attributes.Namespace = namespaces[i/n]
		review, err := v.Access.Create(ctx, &authorizationv1.SubjectAccessReview{
			Spec: authorizationv1.SubjectAccessReviewSpec{
				User: user.Username, UID: user.UID, Groups: user.Groups, Extra: extra,
				ResourceAttributes: &attributes,
			},
		}, metav1.CreateOptions{})
		if err != nil {
			return errReview
		}
		ok[i] = review.Status.Allowed && !review.Status.Denied
		return nil
	})
	if err != nil {
		return nil, err
	}
	var result []string
	for i, namespace := range namespaces {
		if !slices.Contains(ok[i*n:(i+1)*n], false) {
			result = append(result, namespace)
		}
	}
	return result, nil
}
